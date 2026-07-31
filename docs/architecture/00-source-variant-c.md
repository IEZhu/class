# Вариант C — гибридная архитектура (ревизия под self-hosted VPS)

> **Снапшот исходного архитектурного документа**, зафиксирован 2026-07-31.
> Этот файл не редактируется — рабочие уточнения живут в остальных доках
> `docs/architecture/` и в `decisions.md`.

Платформа для группы изучающих английский: 5 преподавателей / 50 студентов, hobby MVP.
Принцип: **ядро и модель данных — свои** (монолит на VPS), **тяжёлые подсистемы — managed или готовые OSS-блоки** (WebRTC, ASR, доска, перевод). Видео и аудио никогда не проходят через VPS.

---

## 1. Топология

```text
                        ┌──────────────────────────────────────────┐
  Google Calendar ◄─────┤              VPS (Docker Compose)        │
  (OAuth2, refresh      │                                          │
   token преподавателя) │  caddy ── TLS, reverse proxy             │
                        │   ├─ web        Next.js (SSR/React)      │
 Teacher/Student ──────►│   ├─ api        Go (бизнес-логика, FSRS) │
   browser              │   ├─ worker     Go (фоновые задачи)      │
      │                 │   ├─ postgres   основная БД + FTS        │
      │                 │   ├─ redis      (опц.: очереди/кэш)      │
      │  WebRTC         │   ├─ libretranslate  (--load-only en,ru) │
      │                 │   └─ excalidraw-complete (доска, Go)     │
      │                 └───────────────┬──────────────────────────┘
      ▼                                 │ presigned URLs, webhooks
 LiveKit Cloud ───── Egress ─────► AWS S3 (записи, VTT, бэкапы)
 (комнаты уроков)                       │      ▲
                                        ▼      │ presigned GET
 AssemblyAI ◄─── presigned S3 URL ── browser (плеер Plyr)
 (ASR + diarization, webhook)

 LLM API (генерация текстов)     YouTube RSS + yt-dlp / TED-дампы
                                 (фоновый сборщик субтитров → PG)
```

Что **не** живёт на VPS: медиапотоки уроков (LiveKit Cloud), файлы записей (S3), транскрибация (AssemblyAI), раздача видео (S3 → браузер напрямую).

---

## 2. Сервисы docker-compose

| Сервис | Образ / стек | RAM (оценка) | Роль |
|---|---|---|---|
| caddy | caddy:2 | ~30 MB | TLS (Let's Encrypt), reverse proxy, единая точка входа |
| web | Next.js | 300–500 MB | UI: страницы уроков, плеер, словарь, практика, доска-обёртка |
| api | Go | 50–150 MB | REST/JSON API, auth (sessions/JWT), FSRS (go-fsrs), выдача presigned URL, LiveKit tokens |
| worker | Go | 100–300 MB | очереди: календарь, ASR-джобы, сборщик субтитров, импорт Tatoeba, LLM-генерация, VTT-рендер |
| postgres | postgres:16 | 300–500 MB | всё состояние + full-text search субтитров/текстов |
| redis | redis:7 (опц.) | 50 MB | кэш переводов, rate-limit; очереди можно держать и в PG (SKIP LOCKED) |
| libretranslate | libretranslate | 1–1.5 GB | перевод en↔ru; обязательно `--load-only en,ru`, ограничить threads |
| excalidraw | PatWie/excalidraw-complete | 50–100 MB | доска: фронт + storage + realtime в одном Go-бинаре; сцены привязаны к lesson_id |

Итого ~2.5–3 GB RAM, 2 vCPU достаточно (пики CPU — только парсинг субтитров в worker). Комфортная VPS: 4 vCPU / 8 GB, минимальная: 2 / 4.

---

## 3. Модель данных (скетч ключевых таблиц)

```sql
users(id, email, role /*teacher|student*/, name, google_refresh_token /*только у teachers*/)
groups(id, name, level /*CEFR*/);  group_members(group_id, user_id)

lessons(id, group_id, teacher_id, starts_at, ends_at, status
        /*scheduled|live|processing|done*/, gcal_event_id,
        livekit_room, recording_s3_key, transcript_id)
lesson_participants(lesson_id, user_id, attended bool)
materials(id, lesson_id, kind /*homework|material*/, title, body_md, s3_key)

-- словарь
terms(id, lang, lemma, surface, translation_cache jsonb)
user_terms(user_id, term_id, status /*1..5: new..known*/, source_lesson_id, added_at)
lesson_term_candidates(lesson_id, term_id, source /*click|whiteboard|manual*/, added_by)
lesson_terms(lesson_id, term_id)          -- подтверждённый «словарь урока»

-- SRS (FSRS)
srs_cards(user_id, term_id, due_at, stability, difficulty,
          reps, lapses, state, last_review_at, PRIMARY KEY(user_id, term_id))
srs_reviews(id, user_id, term_id, rating /*again|hard|good|easy*/, reviewed_at, source /*card|cloze*/)

-- транскрипты
transcripts(id, lesson_id, provider, status, vtt_s3_key)
utterances(transcript_id, idx, speaker_label, user_id nullable /*ручной маппинг A→Иван*/,
           start_ms, end_ms, text)

-- индекс субтитров (практика-видео)
videos(id, source /*youtube|ted*/, external_id, title, channel, duration_s, lang, added_at)
subtitle_segments(video_id, idx, start_ms, end_ms, text,
                  tsv tsvector GENERATED /*to_tsvector('english', text)*/)
-- + GIN index on tsv

-- корпус Tatoeba
tatoeba_sentences(id, lang, text, owner_skill smallint)
tatoeba_links(sentence_id, translation_id)

-- практика
generated_texts(id, group_id, level, genre, body_md, target_term_ids int[], model, created_at)
cloze_tests(id, source /*lesson|generated_text*/, source_id, term_ids int[])
cloze_items(test_id, idx, sentence, answer_term_id, distractor_term_ids int[])
cloze_attempts(id, test_id, user_id, answers jsonb, score, finished_at)

whiteboards(lesson_id, scene_s3_key, updated_at)   -- бэкап JSON-сцены Excalidraw
```

Инвариант: **любой текст в системе** (материал, транскрипт, сгенерированный текст, предложение Tatoeba, субтитр) рендерится одним компонентом `<ClickableText>`, который токенизирует, красит слова по `user_terms.status` и вешает клик-попап.

---

## 4. Ключевые потоки

### 4.1 Жизненный цикл урока
1. Преподаватель создаёт урок (группа, дата, время) → `POST /lessons`.
2. worker создаёт событие в Google Calendar (`events.insert`, `attendees` = email участников, `sendUpdates: all`, description = `https://platform/lesson/{id}`). Событие прилетает в личные календари студентов автоматически.
3. Преподаватель прикрепляет домашку/материалы. Страница `/lesson/{id}` **до старта** показывает их (state machine по времени + статусу).
4. В момент урока страница показывает кнопку «Войти в класс»: api выдаёт LiveKit access token, комната `lesson-{id}`. Рядом — вкладка доски (`excalidraw?room=lesson-{id}`) и панель «словарь урока» (клики по словам из материалов летят в `lesson_term_candidates`).
5. При старте комнаты api запускает LiveKit Egress (room composite → `s3://…/recordings/{id}/room.mp4`; экономный вариант — audio-only).
6. Урок кончился → webhook `egress_ended` → status=processing → worker: presigned GET на запись → AssemblyAI (`speaker_labels: true`, `language_code: en`, webhook).
7. Webhook AssemblyAI → сохраняем utterances → рендерим WebVTT (`<v A>…`) в S3 → status=done.
8. **Post-lesson flow (выбор слов):** преподавателю показывается чеклист кандидатов: клики за урок + текст-элементы, распарсенные из JSON-сцены Excalidraw (`type: "text"`), + ручной ввод. Отмеченные → `lesson_terms` → назначение всем/выборочно: создаются `user_terms(status=1)` + `srs_cards` (FSRS init).
9. Та же ссылка `/lesson/{id}` теперь показывает: плеер записи + скользящий транскрипт по спикерам (клик по реплике = seek) + словарь урока + материалы. Спикеров A/B/C преподаватель может одним селектом смаппить на имена.

### 4.2 Клик по слову (везде)
`POST /terms/click {surface, context_sentence}` → лемматизация → кэш переводов в `terms.translation_cache` → miss: LibreTranslate (+ дефиниция из локального словарного датасета; опц. кнопка «объясни» → LLM с контекстом предложения) → попап: перевод, дефиниция, 1–2 примера из Tatoeba, кнопка «в мой словарь».

### 4.3 Движок практики
- `GET /practice/queue` — due-карточки по FSRS.
- **Карточки**: term → перевод/пример; рейтинг → `srs_reviews` → пересчёт FSRS.
- **Видео**: забытые слова → FTS-запрос по `subtitle_segments` → ранжирование видео (покрытие списка × плотность вхождений/мин) → YouTube iframe embed + своя панель субтитров с подсветкой целевых слов и прыжками по таймкодам.
- **Тексты**: (а) поиск в `generated_texts`/Tatoeba по словам; (б) генерация LLM: промпт {слова, CEFR, жанр, длина} → валидатор покрытия (лемматизация, каждое слово ≥2 раз, иначе ретрай с перечнем недостающих) → сохранение как кликабельный материал.
- **Cloze-финал**: предложения из урока/сгенерированного текста/Tatoeba, пропуск = целевое слово, дистракторы из того же списка. Результат мапится на FSRS-рейтинги (верно+быстро=easy, верно=good, неверно=again) — так тест «проверяет необходимость повторов».

### 4.4 Фоновый сборщик субтитров (offline, без транскрайбинга)
1. Кураторский seed: каналы/плейлисты (TED, TED-Ed, BBC Learning English, VOA Learning English, …) в конфиг-таблице.
2. Список видео канала — через бесплатный RSS-фид канала (без квоты YouTube Data API).
3. `yt-dlp --skip-download --write-subs --write-auto-subs --sub-langs en` c низким рейтом (1 видео / 15–30 с, jitter, backoff). Manual-субтитры приоритетнее auto.
4. Парсинг VTT → `subtitle_segments` → tsvector автоматом. TED — разовый импорт готовых дампов транскриптов.
5. Митигация IP-блокировок: cookies, ретраи, при необходимости — запуск сборщика с домашней машины (тот же worker-бинарь, пишет в PG через VPN/Tailscale).

---

## 5. API-эскиз

```text
POST   /auth/login | /auth/google/callback
POST   /lessons                       GET /lessons/{id}
POST   /lessons/{id}/materials        POST /lessons/{id}/homework
GET    /lessons/{id}/room-token       POST /lessons/{id}/finish
POST   /lessons/{id}/terms/confirm    {term_ids[], assign_to[]}
POST   /webhooks/livekit              (egress_ended → ASR job)
POST   /webhooks/assemblyai           (completed → utterances, VTT)
POST   /terms/click                   POST /user-terms
GET    /practice/queue                POST /reviews
GET    /practice/videos?words=…       GET /practice/texts?words=…
POST   /practice/generate-text        POST /cloze/{id}/attempt
GET    /media/{lesson_id}/url         (presigned GET, TTL 30 мин)
```

---

## 6. Интеграции — конкретика

**Google Calendar.** OAuth2 web flow для преподавателей (scope `calendar.events`), refresh token в БД (шифровать). Приложение в GCP перевести из Testing в Production, иначе refresh token живёт 7 дней; unverified + sensitive scope = warning-экран и лимит ~100 пользователей — для нас ок. Перенос урока → `events.update`; отмена → `events.delete`.

**LiveKit Cloud.** Комната на урок, серверный SDK для token'ов и Egress API (S3 credentials в конфиге egress-запроса). Free tier ≈ 5 000 участнико-минут/мес: урок 60 мин × 4 чел = 240 → ~20 уроков/мес. Пороги: пилот — free; полная загрузка → Ship $50/мес **или** self-hosted `livekit-server` на этой же VPS (открыть UDP-диапазон + TURN/TLS; для записи добавить egress-контейнер — прожорлив, на слабой VPS писать audio-only, для транскрипта этого достаточно).

**AssemblyAI.** Async transcription: `audio_url` = presigned S3 GET (аудио не проходит через VPS), `speaker_labels: true`, опц. `speakers_expected`, `language_code: en`, `webhook_url`. Цена ~$0.15/ч + $0.02/ч диаризация → 60 ч/мес ≈ $10. Caveat: перекрывающаяся речь и RU/EN code-switching деградируют качество — это свойство задачи, не сервиса.

**LibreTranslate.** Только пара en↔ru, интерфейс — внутренний HTTP из api. Кэш переводов в `terms` делает повторные клики бесплатными. Дефиниции — локальный импорт открытого словарного датасета (Wiktionary-извлечения / kaikki.org дампы) в PG; LLM-объяснение — по явной кнопке.

**LLM.** Один worker-модуль с провайдер-абстракцией (Anthropic/OpenAI/DeepSeek). Задачи: генерация текстов по словам, дистракторы для cloze, объяснения слов. Бюджет — единицы $ / мес.

---

## 7. S3: layout, доступ, стоимость

```text
s3://lingua/
  recordings/{lesson_id}/room.mp4        (или audio.ogg)
  transcripts/{lesson_id}.vtt
  whiteboards/{lesson_id}.json
  materials/{lesson_id}/…
  backups/pg/{date}.dump.zst
```

- Доступ только через presigned URL от api (bucket приватный, CORS на GET для домена платформы). S3 отдаёт Range → перемотка в Plyr работает.
- Запись 720p ≈ 0.7–1.1 GB/час → 80 уроков/мес ≈ 60–90 GB прироста (~$2/мес хранение).
- **Lifecycle policy обязательно**: recordings → Standard-IA через 30 дней → Glacier Instant Retrieval через 90 (или удалять видео через N мес., оставляя аудио+VTT — транскрипт и словарь ценнее сырого видео).
- Egress S3→Internet: первые 100 GB/мес бесплатно — при 50 студентах почти наверняка $0; порог для CloudFront — если счёт за egress стабильно > $15–20/мес.
- Бэкапы: nightly `pg_dump | zstd` → S3 (+ 7/30 retention), сцены досок и VTT уже в S3.

---

## 8. Смета (месяц, полная загрузка)

| Статья | Оценка |
|---|---|
| VPS | уже есть → $0 |
| LiveKit Cloud | $0 (пилот) → $50 (Ship) или $0 self-hosted |
| AssemblyAI (~60 ч) | ~$10 |
| S3 (хранение + запросы) | $3–8 (растёт со временем, гасится lifecycle) |
| S3 egress | $0 (в пределах 100 GB free) |
| LLM API | $2–5 |
| DeepL Free / LibreTranslate | $0 |
| **Итого** | **~$15–25/мес** (пилот) / **~$65–75** при LiveKit Ship |

---

## 9. Roadmap

| Этап | Содержание | Оценка |
|---|---|---|
| 0 | Скелет: compose на VPS, auth, роли, CRUD уроков, `/lesson/{id}` state machine | 1–2 нед |
| 1 | Google Calendar OAuth2 + события с attendees; LiveKit Cloud embed; Egress → S3; плеер по presigned | 1–2 нед |
| 2 | AssemblyAI-пайплайн (webhooks), utterances, VTT, транскрипт-вьюер по спикерам с кликабельными словами | 1 нед |
| 3 | `<ClickableText>` + LibreTranslate + словарный датасет; user_terms/статусы; Excalidraw + post-lesson word picker (клики + текст с доски) | 2 нед |
| 4 | go-fsrs, карточки, cloze-генератор, очередь повторений | 1–2 нед |
| 5 | Сборщик субтитров (RSS + yt-dlp + TED-дампы), видео-практика с подсветкой; импорт Tatoeba; LLM-генерация текстов с валидатором | 2–3 нед |

Каждый этап — самостоятельно полезен: после этапа 2 платформа уже закрывает «календарь → урок → запись → транскрипт», всё дальше — обучающий контур.

---

## 10. Риски и решения

| Риск | Митигация |
|---|---|
| Refresh token умирает (Testing mode) | Перевести GCP-app в Production сразу |
| LiveKit free tier мал (≈20 уроков/мес) | Порог: Ship $50 или self-hosted livekit-server на VPS (audio-only egress на слабом железе) |
| YouTube блокирует IP VPS | Низкий рейт + cookies + backoff; fallback — сборщик с домашней машины в ту же PG |
| RU/EN code-switching в ASR | `language_code: en`, принять деградацию русских вставок; ручная правка utterances в UI |
| LibreTranslate качество на редких словах | Кэш + словарный датасет для дефиниций + DeepL Free (500k знаков/мес) как «второе мнение» |
| Рост S3 до TB за год | Lifecycle: IA→Glacier/удаление видео, аудио+VTT хранить вечно |
| Один VPS = единая точка отказа | Nightly-бэкапы в S3; RTO «пересоздать compose за час» для hobby приемлем |
