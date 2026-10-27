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

### S5-4 Импорт Tatoeba (2026-10-26) — ◐, закрывает D-1

- Миграция `backend/migrations/0016_tatoeba.up.sql` (откат — `.down.sql`):
  `tatoeba_sentences` (id — id Tatoeba, `lang` en|ru, `text`, `author`,
  `lemmas text[]` только у en — `CHECK tatoeba_sentences_lemmas_en`,
  `import_id`; GIN `tatoeba_sentences_lemmas_idx` `WHERE lang = 'en'`),
  `tatoeba_links` (PK `(en_id, ru_id)`, `import_id`, индекс
  `tatoeba_links_ru_idx`), `tatoeba_imports` (`source_version`, `phase`
  ru|en, `lines_done`, `finished_at`, `abandoned_at` — брошен
  `-restart`; один незавершённый —
  `tatoeba_imports_running_key`). Отступления от модели данных
  (`owner_skill` нет, есть `author` и `lemmas`) — ADR-028.
- Импорт — пакет `backend/internal/tatoeba` (`Import`, `Lemmas`,
  `License`, `LicenseURL`, `SentenceURL`) и команда
  `backend/cmd/tatoebaimport` (в образе worker'а,
  `/usr/local/bin/tatoebaimport`; флаги `-links`, `-english`, `-russian`,
  `-restart`, `-max-runes` 150, `-max-translations` 1), make-цель
  `tatoeba-import` (`TATOEBA_DIR` — локальные файлы). Источник по
  умолчанию — поязыковые выгрузки tatoeba.org `eng-rus_links.tsv.bz2`,
  `eng_sentences_detailed.tsv.bz2`, `rus_sentences_detailed.tsv.bz2`
  (формат сверен с выгрузкой 2026-10-24). Загрузка и чтение дампов —
  общий с dictimport пакет `backend/internal/dumpfile` (`Open`,
  `Decompress` — gzip и bzip2, `ReadLine`), вынесен из S3-4; для
  импорта Tatoeba добавлены `Stat` и `OpenLazy` — файл выгрузки
  открывается при первом чтении, а не все три сразу.
- `POST /terms/click` наполняет `examples`: до 2 предложений с леммой
  термина и русским переводом — `{text, translation, source: "tatoeba",
  attribution: {author, url, translation_author, translation_url,
  license: "CC BY 2.0 FR", license_url}}`; store —
  `Examples` (`backend/internal/store/tatoeba.go`). Без корпуса или при
  сбое запроса — `examples: []`, клик 200. Новых ключей `.env`, джоб
  и путей S3 нет; smoke worker-образа проверяет и `tatoebaimport`
  (`scripts/security/smoke_runtime.sh`).
- Тесты: `backend/internal/tatoeba/tatoeba_test.go` (разбор строк,
  леммы), `backend/internal/dumpfile/dumpfile_test.go` (в том числе
  `OpenLazy`: версия до открытия, сменившийся файл),
  `backend/internal/httpapi/tatoeba_test.go` на фикстуре
  `backend/internal/tatoeba/testdata/` (повтор — тот же корпус,
  испорченные строки, продолжение после обрыва в каждой фазе, смена
  версии и потолков, `-restart` — и с меньшим потолком, пустая и недокачанная выгрузка, уборка
  устаревшего, один импорт за раз, обратимость 0016; клик: 2 кратчайших
  примера в любой форме слова, подпись, пустой список без корпуса и без
  таблиц, ни одного HTTP-запроса).
- Ожидаемый объём на стенде (оценка на синтетической выгрузке полного
  размера, ADR-028): ~544 тыс. английских, ~474 тыс. русских, ~544 тыс.
  пар, ~250 MB в PG; запрос примеров 1–8 мс. **Осталось для ☑:**
  `make migrate` и `make tatoeba-import` на стенде по
  [deploy/README.md](../../deploy/README.md#примеры-tatoeba-импорт-корпуса-s5-4)
  и проверка DoD на частотных словах. Примеры с подписью попап S3-1
  рисует с интеграции волны B.

### S5-6 LLM-модуль и генерация текстов (2026-10-26) — ◐

- Модуль LLM — `backend/internal/llm` (ADR-029): интерфейс `llm.Provider`
  (`Name`, `Model`, `Generate`), клиенты Anthropic Messages API, OpenAI
  Responses API и DeepSeek Chat Completions на `net/http`, `llm.New(cfg)`
  (nil без ключа), `llm.ConfigFromEnv`, ошибки `*llm.Error` с видами
  `ErrAuth`, `ErrRateLimited`, `ErrUnavailable`, `ErrTimeout`, `ErrRejected`,
  `ErrTruncated`, `ErrEmpty`, `ErrTooLarge`, `ErrBadResponse` и
  `llm.Temporary`. Фейк для тестов — `backend/internal/llm/llmtest`.
- Генерация и валидатор — `backend/internal/textgen` (ADR-030):
  `NewParams` (белые списки, `lemma.IsWord`), промпт со словами-данными в
  `<target_words>`, `Check` (покрытие по леммам, вхождения, доля слов вне
  словаря), `Generate` (один ретрай с `<missing_words>`).
- Миграция `backend/migrations/0017_generated_texts.up.sql` (откат —
  `.down.sql`): `generated_texts` (+ индекс `generated_texts_user_idx`) и
  `llm_usage` (суточный лимит). Store — `backend/internal/store/practice.go`:
  `ReserveLLMRequest`, `CreateGeneratedText`, `GeneratedTextsByUser`.
- Эндпоинты — `backend/internal/httpapi/practice.go` (student, teacher):
  `POST /practice/generate-text`, `GET /practice/texts` (своя история;
  `?words=` — S5-7). Без `words` — невыученные слова личного словаря
  (интеграция волны B, долг D-19 закрыт): `userTermWords` →
  `store.PracticeLemmas` (`backend/internal/store/user_terms.go`), число
  целей — `textgen.TargetsFor`; поле слов в форме web необязательно.
- Ключи `.env` (`deploy/.env.example`, compose — только api):
  `LLM_PROVIDER`, `LLM_API_KEY`, `LLM_MODEL`, `LLM_DAILY_LIMIT`; без
  `LLM_API_KEY` в журнале api строка «генерация текстов отвечает 503».
- Web — `/practice/text` (`web/app/practice/text/`): форма генерации,
  текст простыми абзацами (`renderParagraph` в `generated-text.tsx`) с
  покрытием, история; ссылка «Тексты» в шапке у студента и преподавателя.
- Тесты: `backend/internal/llm/llm_test.go` (форма запроса, заголовки,
  разбор ответа, коды ошибок, таймаут, большой ответ — на httptest),
  `backend/internal/textgen/*_test.go` (валидатор, промпт, ретрай),
  `backend/internal/httpapi/practice_test.go` (401/403, 503 без ключа, 201
  с сохранением, 400/422, 503/502 сбоя провайдера, 429 лимита, история
  только своя, откат 0017, `TestGenerateTextFromUserTerms` — цели
  из словаря: порядок, «знаю» и чужие слова не попадают, потолок по длине).
- **Для ☑ не хватает.** DoD «текст кликается» — `<ClickableText>` (S3-1)
  подключается в волне C заменой `renderParagraph`. Живой вызов
  провайдера не проверен — ключей у стенда нет: с первым `LLM_API_KEY`
  сгенерировать текст на стенде.

### S5-5 Видео-практика (2026-10-27) — ◐

- Эндпоинты — `backend/internal/httpapi/practice_videos.go` (student,
  teacher; блок S5-5 в `api.go`): `GET /practice/videos?words=&limit=&cursor=`
  — ролики корпуса субтитров, ранжированные по доле веса найденных целевых
  слов, затем по вхождениям в минуту, у каждого до 5 лучших сегментов
  с таймкодами и найденными леммами; `GET /practice/videos/{id}?words=` —
  все сегменты ролика для панели плеера. Решения — ADR-038.
- Цели — `?words=` или невыученные слова словаря: `store.LearningWords`
  (`backend/internal/store/practice_videos.go`) за интерфейсом
  `learningWordSource`; веса по `srs_cards` (S4-1), если таблица есть
  в базе, иначе по статусу; поле ответа `weighting`. Формы слова —
  `lemma.Forms` (`backend/internal/lemma/lemma.go`, неправильные формы
  OEWN). Подбор и субтитры ролика — `store.PracticeVideos`,
  `store.PracticeVideoByID`, `store.PracticeVideosArgs` (запрос для
  EXPLAIN в тесте).
- Миграции нет: подбор идёт по GIN `subtitle_segments_tsv_idx` и PK из
  0011; замер — 28–50 мс на 300 тыс. сегментов, 160–225 мс на 1 млн
  (ADR-038). Новых ключей `.env`, джоб, путей S3 и зависимостей нет.
- Web — `/practice/video` (`web/app/practice/video/page.tsx`,
  `video-player.tsx`): форма слов, подбор с лучшими местами, плеер
  `youtube-nocookie.com` через IFrame Player API, панель субтитров
  (подсветка звучащей строки, клик по времени и мимо слов — перемотка,
  строки через `<ClickableText>`, строки с целями отмечены), подпись
  ролика со ссылкой на YouTube; ссылка «Видео» в шапке у студента и
  преподавателя. `activeAt`/`clock` — общие с транскриптом в
  `web/lib/timeline.ts` (тест `web/lib/timeline.test.ts`).
- Тесты: `backend/internal/httpapi/practice_videos_test.go` (порядок по
  покрытию и плотности, лучшие места, слова из запроса, веса по статусу
  без `srs_cards` и по карточкам с ней, подмена источника весов, видео
  без субтитров не попадают, страницы и 400, 401/403, преподаватель,
  субтитры ролика и 404, план на засеянном корпусе),
  `backend/internal/lemma/lemma_test.go` (`TestForms`).
- **Для ☑ не хватает.** Корпуса на стенде нет, пока не подтверждён живой
  сбор S5-2; синхронизация панели с настоящим плеером YouTube в браузере
  не проверялась (тесты в YouTube не ходят) — на стенде открыть подбор,
  проверить перемотку по таймкоду и подсветку. Оценок FSRS видео-практика
  не пишет — долг D-26.

## Риски этапа

Блокировка IP YouTube (рейт/cookies/backoff, fallback — домашняя машина,
[07-risks.md](../architecture/07-risks.md)); объём Tatoeba/kaikki-дампов —
импортировать только en↔ru срез; расход LLM держать в смете
([06-budget.md](../architecture/06-budget.md)).
