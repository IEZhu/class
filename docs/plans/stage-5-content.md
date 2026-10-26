# Этап 5 — Контент: субтитры, Tatoeba, LLM, практика (оценка: 2–3 нед)

**Цель:** обучающий контур на внешнем контенте: сборщик субтитров + FTS,
корпус Tatoeba, LLM-генерация текстов, видео- и тексто-практика по «забытым»
словам. Закрываются долги этапа 3 (D-1, D-2).

Доки: [сборщик субтитров](../architecture/flows/subtitle-harvester.md) ·
[движок практики](../architecture/flows/practice-engine.md) ·
[LLM](../architecture/integrations/llm.md)

## Входные артефакты

| Артефакт | Откуда |
|---|---|
| Worker + очередь `jobs` | S1-2 |
| Лемматизация (en) | S3-2 |
| `user_terms` статусы + FSRS due/lapses («забытые слова») | S3-5, S4-1 |
| `<ClickableText>` | S3-1 |
| Cloze-генератор (для LLM-дистракторов) | S4-4 |
| Попап слова с заглушками Tatoeba/«объясни» (долги D-1, D-2) | S3-2 |

## Задачи

### S5-1 Источники и RSS
Таблица `subtitle_sources` (каналы/плейлисты: TED, TED-Ed, BBC Learning
English, VOA Learning English, …); джоба опроса RSS-фидов каналов
(без квоты YouTube Data API) → новые `videos`.
**DoD:** новые ролики каналов появляются в videos автоматически.

### S5-2 Сборщик субтитров (yt-dlp)
`yt-dlp --skip-download --write-subs --write-auto-subs --sub-langs en`;
рейт 1 видео / 15–30 с + jitter + backoff, cookies по необходимости;
manual приоритетнее auto; парсер VTT → `subtitle_segments` (tsvector
генерируется сам). Бинарь запускаем и с домашней машины в ту же PG
(VPN/Tailscale) — предусмотрено конфигом подключения.
**DoD:** по каналу из seed собраны сегменты; FTS-запрос находит слова.

### S5-3 TED-дампы
Разовый импорт готовых транскриптов TED в `videos`/`subtitle_segments`.
**DoD:** корпус заметно расширен без обращения к YouTube.

### S5-4 Импорт Tatoeba
`tatoeba_sentences` + `tatoeba_links` (пары en↔ru); подключить 1–2 примера
в попап клика по слову — **закрывает долг D-1**.
**DoD:** у частотных слов в попапе есть примеры с переводом.

### S5-5 Видео-практика
«Забытые» слова (status/lapses/due) → FTS по `subtitle_segments` →
ранжирование (покрытие списка × плотность вхождений/мин) → YouTube iframe +
своя панель субтитров (`<ClickableText>`) с подсветкой целевых слов и
прыжками по таймкодам. `GET /practice/videos?words=…`.
**DoD:** для списка слов подбирается релевантное видео, целевые слова
подсвечены, клик по таймкоду мотает.

### S5-6 LLM-модуль + генерация текстов
Провайдер-абстракция (Anthropic/OpenAI/DeepSeek, конфиг);
`POST /practice/generate-text` {слова, CEFR, жанр, длина} → валидатор
покрытия (лемматизация; каждое слово ≥ 2 раз, ретрай с перечнем
недостающих) → `generated_texts` → рендер как кликабельный материал.
**DoD:** сгенерированный текст проходит валидатор и кликается.

### S5-7 Тексто-практика
`GET /practice/texts?words=…`: поиск по `generated_texts` и Tatoeba.
**DoD:** по списку слов находятся тексты/предложения с этими словами.

### S5-8 LLM-дистракторы и «объясни»
Дистракторы cloze через LLM (апгрейд S4-4); кнопка «объясни» в попапе —
LLM с контекстом предложения — **закрывает долг D-2**.
**DoD:** дистракторы правдоподобны; «объясни» отвечает с учётом контекста.

## Выходные артефакты

| Артефакт | Потребитель |
|---|---|
| Корпус субтитров + FTS | видео-практика (постоянно пополняется) |
| Корпус Tatoeba | попап, тексто-практика, cloze |
| LLM-модуль | все будущие LLM-фичи |
| `generated_texts` | тексто-практика, cloze |
| Полный контур практики | продукт целиком |

## Definition of Done этапа

- [ ] S5-1…S5-8 закрыты в [INDEX](../tasks/README.md)
- [ ] Долги D-1, D-2 закрыты и отмечены в INDEX
- [ ] Сквозной сценарий: забытые слова → видео с подсветкой → сгенерированный текст → cloze
- [ ] «Факт» заполнен

## Факт (заполняется по ходу реализации)

### S5-1 Источники и RSS (2026-10-26)

- Миграция `backend/migrations/0011_subtitles.up.sql` (откат — `.down.sql`):
  `subtitle_sources` (+ `polled_at`, `last_error`, `created_at`; UNIQUE
  `(kind, external_id)`) с seed из четырёх каналов — TED
  `UCAuUUnT6oDeKwE6v1NGQxug`, TED-Ed `UCsooa4yRKGN_zEE8iknghZA`, BBC Learning
  English `UCHaHD477h-FeBbVh9Sh7syA`, VOA Learning English
  `UCKyTokYo0nK2OA-az-sDijA` (id сверены с их RSS-фидами); `videos`
  (+ состояние сборщика, UNIQUE `(source, external_id)`); `subtitle_segments`
  (PK `(video_id, idx)`, `tsv` GENERATED `to_tsvector('english', text)`
  STORED + GIN `subtitle_segments_tsv_idx`); `job_schedules` (ADR-021).
- Первый планировщик периодических джоб — `backend/internal/jobs/schedule.go`
  (`jobs.NewScheduler`, `Every`, `Tick`, `Run`; ADR-021).
- Джоба `rss_poll` (раз в час) — `harvester.PollFeeds`, фид —
  `backend/internal/harvester/rss.go` (Atom YouTube, Shorts пропускаются).
  Store (`backend/internal/store/subtitles.go`): `EnabledSubtitleSources`,
  `AddFeedVideos` (`ON CONFLICT DO NOTHING`), `SetSourcePolled`.
- Проверено живьём на одноразовой базе: роль harvester по расписанию
  опросила четыре seed-фида и добавила 51 видео (60 записей минус 9 Shorts).

### S5-2 Сборщик субтитров yt-dlp (2026-10-26) — ◐

- Джоба `subtitles_fetch` (раз в 5 мин; пакет, пока хватает потолка джобы
  15 мин) — `harvester.FetchSubtitles`: темп 15–30 с и backoff
  30 мин → сутки — `pace.go`; вызов yt-dlp — `ytdlp.go` (`YTDLP`,
  `ErrBlocked`, `ErrNoRunner`; manual/auto — по `info.json`, урезанное
  окружение); VTT → сегменты — `vtt.go` (`ParseVTT`, без повторов
  катящихся автосубтитров). Store: `NextVideoToHarvest`, `SaveSubtitles`
  (замена сегментов одной транзакцией), `FailHarvest`, поиск для S5-5 —
  `SearchSubtitles(words, limit)` (FTS по `tsv`, слова через `english`).
- Регистрация — `worker.RegisterHarvester` (`backend/internal/worker/worker.go`);
  роль процесса — `WORKER_ROLE` в `backend/cmd/worker/main.go`
  (`core` по умолчанию | `harvester`).
- Образ — target `harvester` в `backend/Dockerfile`: yt-dlp 2026.10.21 и
  yt-dlp-ejs 0.8.0 по sha256 из `backend/ytdlp/requirements.txt`, Node.js
  из Alpine 3.24, пользователь 10001, `WORKER_ROLE=harvester`. Compose —
  сервис `harvester` под профилем `harvester` (включается явно).
  `scripts/security/smoke_runtime.sh harvester` и джоба `harvester`
  в `.github/workflows/security.yml`; в блокирующий скан образ не входит
  (ADR-022, долг D-12).
- Окружение: в контейнере `WORKER_ROLE`, `HARVESTER_COOKIES` (путь к cookies;
  пусто или пустой файл — без cookies); новый ключ `deploy/.env` —
  `HARVESTER_COOKIES_FILE` (файл на хосте, по умолчанию `/dev/null`).
  Нечитаемый файл cookies — `ErrNoRunner`: попытки видео не тратятся.
- Правки ревью волны A (ADR-022): ключ `deploy/.env`
  `HARVESTER_DATABASE_URL` — DSN своей роли сборщика, compose отдаёт его
  в `DATABASE_URL` (пусто — DSN владельца); роль создаёт идемпотентный
  `deploy/harvester-db-role.sh` (только `subtitle_sources`, `videos`,
  `subtitle_segments`, `jobs`, `job_schedules`). Проверено на одноразовом
  Postgres 16 с миграциями 0001–0013: два запуска подряд, роли закрыты
  `users`, `sessions`, `lessons`, `transcripts`, а настоящий worker
  с `WORKER_ROLE=harvester` под ней поставил джобы расписания, собрал
  видео поддельным yt-dlp и записал сбой второго. Сервис `harvester` —
  `read_only`, tmpfs `/tmp` и `/home/app`, `cap_drop: [ALL]`,
  `no-new-privileges`, `mem_limit: 512m`, `cpus: 1` (потолки до замера);
  образ под этими флагами запускает yt-dlp и Node.js.
- Тесты: `backend/internal/harvester/*_test.go` (VTT, RSS, темп и backoff
  на поддельных часах, `YTDLP` поверх `testdata/fake-yt-dlp`),
  `backend/internal/jobs/schedule_test.go`,
  `backend/internal/httpapi/subtitles_test.go` (миграция, FTS, обе джобы
  на общей тестовой базе; нечитаемые cookies не трогают `harvest_attempts`).
  Харнесс тестовой базы: накат миграций
  сериализован advisory-блокировкой — параллельные пакеты тестов падали
  на одновременном `CREATE EXTENSION pgcrypto`.
- **Не подтверждено живьём.** Единственный пробный вызов с VPS (видео TED
  `cTHwSER_l-8`, одноразовая база) упёрся в таймаут yt-dlp 2 мин; JS-движком
  тогда был quickjs-ng 0.11, которому, по вики yt-dlp, нужны минуты.
  Образ переведён на Node.js, потолок вызова — 5 мин, в ошибку таймаута
  теперь попадает хвост вывода yt-dlp. Без сети образ проходит полный
  цикл: джобы по расписанию, сбои фидов и видео записаны в базу.
  Осталось для ☑: включить профиль (или прогнать образ на одноразовой
  базе) и убедиться, что по seed-каналу собраны сегменты и FTS их находит.

### S5-3 TED-дампы — не сделано (ADR-022)

- Открытого дампа транскриптов TED с таймкодами под плеер практики нет:
  TED-LIUM 3 (CC BY-NC-ND 3.0) — автоматически выровненные STM одним
  архивом ~50 ГБ со звуком и без id видео YouTube; остальные корпуса —
  по регистрации или без таймкодов. Видео TED и TED-Ed собирает S5-2
  (каналы в seed). DoD «без обращения к YouTube» без источника недостижим:
  решение владельца — бэкфилл каналов TED/TED-Ed тем же сборщиком
  или другой источник.

## Риски этапа

Блокировка IP YouTube (рейт/cookies/backoff, fallback — домашняя машина,
[07-risks.md](../architecture/07-risks.md)); объём Tatoeba/kaikki-дампов —
импортировать только en↔ru срез; расход LLM держать в смете
([06-budget.md](../architecture/06-budget.md)).
