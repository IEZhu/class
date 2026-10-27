# Этап 1 — Календарь, LiveKit, записи в S3 (оценка: 1–2 нед)

**Цель:** урок живёт полным циклом: событие в календарях участников →
видеокомната → запись в S3 → плеер на странице урока. Плюс появляются
worker с очередью, presigned-механика и бэкапы.

Доки: [google-calendar](../architecture/integrations/google-calendar.md) ·
[livekit](../architecture/integrations/livekit.md) ·
[S3](../architecture/05-storage-s3.md) ·
[жизненный цикл урока](../architecture/flows/lesson-lifecycle.md)

## Входные артефакты

| Артефакт | Откуда |
|---|---|
| `lessons` (+status), `users.email` участников | S0-2, S0-4 |
| `/lesson/{id}` state machine с заглушками «Войти» и плеера | S0-5 |
| Auth-middleware, роли | S0-3 |
| compose + `.env.example` + Makefile | S0-1 |

## Задачи

### S1-1 Google OAuth2 для преподавателей
GCP-приложение (**сразу Production** — риск 7-дневного refresh token),
scope `calendar.events`; `POST /auth/google/callback`; refresh token
шифрованно в `users.google_refresh_token` (примитив шифрования → ADR).
**DoD:** преподаватель подключает календарь из UI; токен переживает неделю.

### S1-2 Worker и очередь jobs
Сервис worker в compose (тот же Go-модуль, `cmd/worker`); таблица `jobs` +
`FOR UPDATE SKIP LOCKED`, ретраи с backoff. Первая джоба `calendar_sync`:
`events.insert/update/delete`, `attendees` = email участников,
`sendUpdates: all`, description = ссылка на урок; `gcal_event_id` в lessons;
идемпотентность.
**DoD:** создание/перенос/отмена урока отражаются в календарях студентов.

### S1-3 S3 + presigned + lifecycle
Бакет (приватный, CORS GET для домена), IAM-ключи; presigned GET/PUT в api;
`GET /media/{lesson_id}/url` (TTL 30 мин, проверка участия); lifecycle
policy по [05-storage-s3.md](../architecture/05-storage-s3.md). Закрывает
долг D-4: `materials.s3_key` + загрузка файлов материалов.
**DoD:** приватный файл отдаётся браузеру только по presigned URL.
*Объём сужен по ходу:* S1-3 сделал presigned GET и `GET /media/{lesson_id}/url`;
presigned PUT и файлы материалов остались долгом D-4 и закрыты отдельной
работой по долгам D-4/D-5 ([ADR-024](../architecture/decisions.md), «Факт»).

### S1-4 LiveKit Cloud: комнаты
Ключи в `.env`; `GET /lessons/{id}/room-token` (identity = user_id,
комната `lesson-{id}`); embed комнаты в `/lesson/{id}` (состояние live) —
заглушка S0-5 оживает.
**DoD:** два браузера видят/слышат друг друга в комнате урока.

### S1-5 Egress → S3 + webhook
Запуск Egress при старте комнаты (room composite; экономно — audio-only);
`POST /webhooks/livekit` с проверкой подписи: `egress_ended` →
`recording_s3_key`, `status=processing` → (пока ASR нет) сразу `done`.
Точка вставки ASR для этапа 2 — оставить явной (джоба-плейсхолдер).
**DoD:** после урока в S3 лежит запись, статус дошёл до done.

### S1-6 Плеер записи
Plyr в `/lesson/{id}` (done) по presigned GET; перемотка (Range) работает.
**DoD:** участник смотрит запись своего урока; чужой получает 403.

### S1-7 Бэкапы
Nightly `pg_dump | zstd` → `s3://…/backups/pg/`, retention 7/30;
скрипт в `deploy/`, расписание (cron/systemd-timer на VPS).
**DoD:** бэкап появляется в S3; восстановление проверено на копии БД.

### S1-8 Роль admin и управление учётками (api)
Пробел проектирования: до этой задачи завести человека можно было только
через `seed.sql` — `POST /groups/{id}/members` добавляет лишь существующего.
Третья роль `admin` (миграция `0003_roles`), эндпоинты `/users`
(список, заведение со стартовым паролем, правка имени/роли, сброс пароля),
`PATCH /auth/me`, `POST /auth/password`, удаление участника из группы.
Границы прав — [ADR-007](../architecture/decisions.md): admin всё,
teacher — только студенты своих групп (состав групп меняет только admin),
любой — своё имя и пароль.
**DoD:** админ заводит студента запросом и тот логинится; преподаватель
получает 403 на чужого студента, на заведение не-студента и на правку
состава групп.

### S1-9 Админ-кабинет и своя учётная запись (web)
`/admin` — люди (список с группами, заведение, сброс пароля, смена роли)
и группы (создание, состав, добавить/убрать участника); `/account` — своё
имя и смена пароля; ссылки в шапке `UserBar` по роли.
**DoD:** преподаватель заводит студента из UI, тот входит и меняет пароль
у себя в кабинете; студенту `/admin` отвечает «нет доступа».

### S1-10 Одноразовые ссылки-приглашения
Таблица `invites` (миграция `0004_invites`, только sha256-дайджест токена),
выпуск/список/отзыв под staff, публичные `GET/POST /signup/{token}`,
страница `/invite/{token}`. Границы — [ADR-008](../architecture/decisions.md):
преподаватель зовёт только студентов и без зачисления в группу.
**DoD:** приглашённый переходит по ссылке, задаёт пароль и попадает
в кабинет залогиненным; повторный переход по той же ссылке — 410.

### S1-11 Открытые события и гостевые ссылки
Пробел проектирования: звать людей со стороны, не заводя им учёток.
Событие — урок без группы (`lessons.group_id` nullable, `title`), гость —
четвёртая роль `guest` (миграции `0005_open_events`, `0006_event_constraints`).
Многоразовая гостевая ссылка (в БД только sha256), выпуск, перевыпуск
и отзыв под организатором (admin или teacher события), публичные
`GET/POST /join/{token}`: имя → участник события и сессия до конца события
плюс 2 часа. Границы — [ADR-010](../architecture/decisions.md), уточнения
ревью — ADR-032.
**DoD:** организатор выпускает ссылку, человек заходит по ней, назвав имя,
и попадает в комнату события; отозванная или перевыпущенная ссылка — 404,
после конца события — 410; у урока с группой ссылки нет (409 в api,
`CHECK` в БД).

### S1-12 UI события и гостевой ссылки
Форма на `/lessons`: без группы — открытое событие с обязательным
названием, с группой — обычный урок. На странице события организатору —
выпуск, копирование, перевыпуск и отзыв ссылки, пока событие не кончилось.
Публичная страница `/join/{token}` предупреждает о записи до ввода имени.
**DoD:** преподаватель создаёт событие из UI и выдаёт ссылку; гость
на `/join/{token}` видит название, время и предупреждение о записи, вводит
имя и попадает на страницу события; перезагрузка страницы организатором
не ломает уже разосланную ссылку.

## Выходные артефакты

| Артефакт | Потребитель |
|---|---|
| worker + таблица `jobs` (очередь, ретраи) | S2-1 (ASR-джоба), S5-2 (сборщик), S5-6 (LLM) |
| S3 + presigned механика в api | S2-1 (ссылка для AssemblyAI), S2-3 (VTT), S3-6 (сцены досок) |
| `recording_s3_key` + точка вставки ASR после `egress_ended` | S2-1 |
| Паттерн webhook-эндпоинта с верификацией | S2-2 |
| Комнаты LiveKit + room-token | — (готово для уроков) |
| Плеер в `/lesson/{id}` | S2-4 (синхронизация с транскриптом) |
| Бэкапы PG | эксплуатация |
| Открытые события (урок без группы, `title`), роль `guest`, `GET/POST /join/{token}` и страница `/join/{token}` | S2-5 (гость — обычный участник с `user_id`), эксплуатация |

## Definition of Done этапа

- [ ] S1-1…S1-12 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: урок в календаре → комната → запись в S3 → плеер
- [ ] «Факт» заполнен (ключи `.env`, имена джоб, пути S3)

## Факт (заполняется по ходу реализации)

### S1-8 Роль admin и управление учётками (2026-08-02)

- Миграция `backend/migrations/0003_roles.up.sql` — CHECK `users.role`
  расширен до `admin | teacher | student`; откат понижает админов
  до teacher. `seed.sql` заводит `admin@lingua.local` (роль admin).
- `backend/internal/store/users.go` — `UserByID`, `CreateUser`,
  `ListUsers`, `ListUsersOfTeacherGroups`, `TeacherManagesUser`,
  `UpdateUserProfile`, `SetPassword` (в одной транзакции с удалением
  сессий владельца), `RemoveGroupMember`.
- `backend/internal/httpapi/users.go` — хендлеры; `middleware.go` —
  `requireAnyRole` вместо `requireRole`; в `api.go` чтение групп — `staff`
  (admin + teacher), правка состава — только `admin`.
- Эндпоинты (в [04-api.md](../architecture/04-api.md)): `GET/POST /users`,
  `PATCH /users/{id}`, `POST /users/{id}/password`, `PATCH /auth/me`,
  `POST /auth/password`, `DELETE /groups/{id}/members/{user_id}`.
- Минимальная длина пароля — 8 символов (`minPasswordLen`); дубль email
  → 409; смена пароля себе выдаёт свежий cookie взамен погашенных сессий.

### S1-9 Админ-кабинет и своя учётная запись (2026-08-02)

- `web/app/admin/page.tsx` + `admin-panel.tsx` — «Позвать человека»,
  список людей с группами (сброс пароля, смена роли), группы
  (создание, состав, добавить/убрать); 403 → «Нет доступа».
- `web/app/account/page.tsx` + `account-forms.tsx` — имя и смена пароля.
- `web/app/forms.tsx` — общий каркас форм (`request`, `useSubmit`,
  `FormStatus`) для обоих кабинетов.
- `web/lib/roles.ts` — тип `Role` и подписи ролей отдельно от
  `lib/api.ts`: тот тянет `next/headers` и в клиентские компоненты
  не импортируется.
- `UserBar` — ссылки «Люди и группы» (не студенту) и «Моя учётная запись».

### S1-10 Одноразовые ссылки-приглашения (2026-08-02)

- Миграция `backend/migrations/0004_invites.up.sql` — таблица `invites`
  (sha256-дайджест токена, `group_id` опционален, частичный индекс
  по ожидающим). `backend/internal/store/invites.go` — `CreateInvite`,
  `ListPendingInvites`, `InviteByTokenHash`, `DeleteInvite`, `AcceptInvite`
  (одна транзакция: гашение → создание учётки → зачисление в группу).
- `backend/internal/httpapi/invites.go` — выпуск/список/отзыв под staff,
  публичные `GET/POST /signup/{token}`; TTL ссылки — `inviteTTL` = 7 дней.
- `httpapi.New` принимает `httpapi.Config{PublicBaseURL}`; `cmd/api/main.go`
  собирает его из **обязательного теперь** ключа `.env` `DOMAIN`.
  `newSessionToken` переименован в `newToken` — общий генератор для сессий
  и приглашений.
- `web/app/invite/[token]/` — публичная страница: предпросмотр (кто зовёт,
  какая роль и группа) и форма пароля; 404/410 дают понятный экран.
- `/admin` — секция «Позвать по ссылке» с выбором группы (только админ),
  показ выпущенной ссылки с копированием, список ожидающих с отзывом;
  заведение со стартовым паролем убрано под `<details>` как запасной путь.
- `web/app/forms.tsx` — добавлен `requestJson` (ответ нужен целиком).

### S1-4 LiveKit Cloud: комната урока (2026-08-02)

- `GET /lessons/{id}/room-token` (`backend/internal/httpapi/rooms.go`):
  комната `lesson-{id}`, identity = `user_id` (по нему S2-5 сопоставит
  спикеров с учётками), имя участника — из профиля, TTL токена 3 часа.
  Гранты — только `RoomJoin`/`CanPublish`/`CanSubscribe`: административные
  не выдаются никому.
- Доступ: участник или преподаватель урока (тот же `canSeeLesson`, что
  у `GET /lessons/{id}`); чужой — 403, несуществующий — 404,
  `processing`/`done` — 409 «урок уже завершён».
- Ключи `.env`: `LIVEKIT_URL`, `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET`
  (шаблон — `deploy/.env.example`, проброс — в compose). **Опциональны**:
  без них эндпоинт отдаёт 503, остальной стенд работает.
- Зависимости: `github.com/livekit/protocol v1.50.4` (требует Go ≥ 1.26 —
  билд-образ в `backend/Dockerfile` поднят до `golang:1.26.5-alpine`);
  в web — `@livekit/components-react`, `@livekit/components-styles`,
  `livekit-client`.
- `web/app/lesson/[id]/lesson-room.tsx` — заглушка «Войти в класс» ожила:
  токен запрашивается по клику, а не при загрузке страницы (открытая
  вкладка не должна жечь участнико-минуты free tier), дальше
  `<LiveKitRoom><VideoConference/></LiveKitRoom>`.
- Первые Go-тесты репозитория: `rooms_test.go` проверяет гранты выпущенного
  токена тем же SDK и отвергает чужой секрет.

### S1-3 S3 + presigned (2026-08-03)

- `backend/internal/storage/s3.go` — клиент поверх `aws-sdk-go-v2`:
  `PresignGet` (TTL 30 мин, константа `PresignTTL`), `Exists`,
  `RecordingKey(lessonID, ext)` по раскладке 05-storage-s3.md. Для R2
  включается `UsePathStyle` — иначе запрос уходит на `bucket.<account>.r2…`
  и объект не находится. Без ключей `New` возвращает `nil` без ошибки:
  стенд поднимается, а медиа-эндпоинты отвечают 503. Пустой `S3_REGION`
  означает `auto` только при заданном `S3_ENDPOINT` (R2); без endpoint
  (AWS) api с пустым регионом не стартует.
- `GET /media/{lesson_id}/url` (`httpapi/media.go`) — presigned-ссылка
  участнику урока: та же граница `canSeeLesson`, что у страницы урока;
  нет записи → 404 `no_recording`, чужой урок → 403, без входа → 401.
- Ключи `.env`: `S3_BUCKET`, `S3_REGION`, `S3_ENDPOINT`,
  `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` (проброс — в compose).
- Бакет настраивается `deploy/s3-bootstrap.sh` (CORS, lifecycle).

### S1-5 Egress → R2 + webhook (2026-08-03)

- `backend/internal/livekitapi/egress.go` — тонкий Twirp-клиент
  (`POST /twirp/livekit.Egress/StartRoomCompositeEgress`, protojson,
  токен с грантом `RoomRecord`). `server-sdk-go` не берём: оттуда нужен
  один вызов, а тянет он полный стек pion/webrtc.
- `POST /webhooks/livekit` (`httpapi/webhooks.go`) — подпись проверяется
  `webhook.ReceiveWebhookEvent`; без неё и с мусорной — 401, без любого
  из `LIVEKIT_URL`/`LIVEKIT_API_KEY`/`LIVEKIT_API_SECRET` — 503.
  `room_started` и `participant_joined` → `StartLessonRecording`: переход
  scheduled → live и запуск Egress с выводом в R2 в одной транзакции
  (`FOR UPDATE SKIP LOCKED`, ADR-012). Повторная или параллельная доставка
  второй Egress не запустит; сорванный запуск откатывает переход, вебхук
  отвечает 503, и запись запускает повтор доставки или следующий вошедший.
  `egress_ended` → путь записи в `lessons.recording_s3_key` и статус.
  Провалившийся Egress (`EGRESS_FAILED`) и чужая комната игнорируются.
- `EGRESS_AUDIO_ONLY=1` в `.env` переключает запись на звук (OGG вместо
  MP4, на порядок меньше объём). Ключи хранилища уходят в LiveKit в теле
  egress-запроса — отсюда требование ADR-009 к узкому токену.
- Точка вставки ASR — `onEgressEnded`: сейчас урок сразу `done`, с S2-1
  туда встанет `processing` и джоба `asr_submit`.

### S1-6 Плеер записи (2026-08-03)

- `web/app/lesson/[id]/lesson-player.tsx` — Plyr поверх обычного `<video>`
  в фазе `done`: ссылка берётся из `GET /media/{lesson_id}/url`, файл идёт
  хранилище ↔ браузер, минуя VPS. Перемотка работает на Range-запросах,
  которые R2 отдаёт сам, — стриминговая обвязка не нужна.
- Plyr подключается динамическим `import()`: он нужен только на странице
  с записью, и в типах объявлен через `export =`, без default-импорта.
  Не загрузился чанк или Plyr упал — запись играет во встроенном `<video>`
  с подсказкой под ним.
- Ссылка живёт 30 минут и может истечь посреди длинной записи — по `onError`
  плеер берёт свежую, меняет только `src` (Plyr не пересоздаётся) и после
  `loadedmetadata` нового источника возвращается на ту же позицию,
  продолжая воспроизведение. Не открылась и свежая — просит обновить
  страницу, без бесконечных повторов.
- Скорость воспроизведения 0.75–2× (разбор урока), подписи управления
  по-русски.

### S1-11 Открытые события и гостевые ссылки (2026-08-10)

- Миграция `0005_open_events`: `lessons.group_id` стал nullable, добавлены
  `title` и `guest_token_hash` (UNIQUE, только sha256), `CHECK`, что
  название берётся из группы или из title; роль `guest` в `users.role`.
- `store/guests.go` — `SetGuestToken` (выпуск/перевыпуск/отзыв одной
  функцией), `GuestEventByToken` (ErrEventOver для прошедшего),
  `JoinAsGuest` (гость, участие и сессия одной транзакцией).
- `httpapi/guests.go` — публичные `GET/POST /join/{token}`, выпуск и отзыв
  под организатором события (`lessonForOwner`: админ или преподаватель).
  Ссылка **многоразовая**, в отличие от приглашений S1-10. Сессия гостя
  живёт до конца события плюс 2 часа.
- Гости скрыты из `ListUsers`: 50 студентов не должны тонуть в посетителях.
- Email гостя синтетический (`guest+<32 случайных hex>@guests.local`):
  колонка UNIQUE NOT NULL, а адреса у гостя мы не спрашиваем.
- По ревью PR #11 — миграция `0006_event_constraints` (0005 уже на стенде):
  `lessons_named` требует непустой `title` у события,
  `lessons_title_event_only` запрещает `title` у урока с группой,
  `lessons_guest_link_event_only` — гостевую ссылку у него же.
  `POST /lessons` отвечает 400 на `title` вместе с `group_id`; выпуск
  и отзыв ссылки у урока с группой — 409 (`LessonTeacherAndGroup`).
- `JoinAsGuest` читает урок `FOR SHARE`: отзыв или перевыпуск, начатый
  раньше входа, побеждает — старый токен гостя не заводит (404).
- `httpapi/guests_test.go` — на схеме `security_test`: статусы
  `GET/POST /join/{token}` (200/201, 400, 404, 410), выпуск, перевыпуск
  и отзыв под организатором, админом, чужим преподавателем, студентом
  и гостем, гонка входа с перевыпуском и отзывом, CHECK из 0006.

### S1-12 UI события и гостевой ссылки (2026-08-10)

- `web/app/lessons/new-event.tsx` — форма создания: без группы получается
  открытое событие (тогда обязательно название), с группой — обычный урок:
  поле названия очищается и отключается, `title` в api не уходит.
  `datetime-local` читается как местное время и уходит в api в UTC.
- `web/app/lesson/[id]/guest-link.tsx` — выпуск, копирование, перевыпуск
  и отзыв ссылки; видит только организатор, только у события без группы
  и только до конца события.
- `web/app/join/[token]/` — публичная страница: название, время, кто ведёт,
  **предупреждение о записи** и поле имени. Отозванная и прошедшая ссылка
  дают понятный экран.
- `web/lib/lesson-title.ts` — подпись урока в одном месте: событие берёт
  `title`, урок — имя группы.

### S1-2 Worker и очередь jobs (2026-10-26)

- Миграция `backend/migrations/0007_jobs.up.sql` — таблица `jobs` по модели
  данных плюс `failed_at` (попытки исчерпаны) и `created_at`; частичный
  индекс `jobs_ready_idx (run_at, id) WHERE failed_at IS NULL` под запрос
  захвата. Номер 0006 занят параллельной веткой S1-11.
- `backend/internal/jobs/` — `Enqueue(ctx, db, kind, payload)` в пуле или
  транзакции вызывающего; `Runner` (`NewRunner`, `Handle`, `Run`,
  `RunOnce`): захват `FOR UPDATE SKIP LOCKED`, ретраи 1 мин → удвоение →
  1 ч, 8 попыток, подбор захватов старше 10 мин, паника хендлера —
  обычная неудачная попытка. Семантика — [ADR-013](../architecture/decisions.md).
- `backend/internal/worker/` — хендлеры по kind; `backend/cmd/worker` —
  бинарь: `DATABASE_URL`, SIGTERM — новые джобы не берутся, текущей 20 с,
  потом она возвращается в очередь.
- Джоба `recording_ready` (`store.JobRecordingReady`, payload
  `{"lesson_id": N}`) — **точка вставки ASR для S2-1**: `onEgressEnded`
  через `store.FinishLessonRecording` пишет `recording_s3_key`,
  `status=processing` и ставит джобу одной транзакцией (условие на статус
  гасит повторную доставку вебхука); хендлер `FinishLessonProcessing`
  переводит урок в `done`. Строка S1-5 «урок сразу done» больше не верна.
- `backend/Dockerfile` — target `worker` (api остаётся последней стадией);
  сервис `worker` в `deploy/docker-compose.yml` без портов и healthcheck,
  `stop_grace_period: 30s`. Новых ключей `.env` нет.
- CI: `ci.yml` собирает все `cmd`; `security.yml` сканирует образ worker
  (матрица, `smoke_runtime.sh worker` — uid 10001 и отказ без
  `DATABASE_URL`, `check_scan_inventory.py`).
- Сбой транзакции `egress_ended` — ответ 503, LiveKit повторяет доставку;
  200 остаётся для чужой комнаты, `EGRESS_FAILED`, пустого результата
  и уже принятой записи (ADR-013).
- Порядок выката: `make migrate` до api — без таблицы `jobs` вебхук
  `egress_ended` откатывает транзакцию, а повторы LiveKit ограничены:
  исчерпав их, путь записи теряется.
- Тесты на базе — общий harness `backend/internal/testdb` (httpapi и jobs):
  миграции под `pg_advisory_lock`, `pgcrypto` один раз в `public`, иначе
  параллельные пакеты `go test ./...` падали на `CREATE EXTENSION` (23505).
- calendar_sync не сделан — долг D-7, закрывает S1-1 (вне MVP).
- Глобальная уборка протухших сессий (ADR-006) не сделана — нет
  периодических джоб, долг D-15.

### S1-7 Бэкапы Postgres (2026-10-26)

- `deploy/backup-pg.sh` — `pg_dump --format=custom --compress=0` внутри
  контейнера postgres (локальный сокет, без пароля) → `zstd` на хосте →
  `s3://<бакет>/backups/pg/<дата UTC>.dump.zst`. До выгрузки — `zstd -t`
  и оглавление `pg_restore --list`; выгрузка — во временный
  `backups/pg/incoming/<дата>.<pid>.dump.zst`, затем скачивание обратно
  и сверка sha256 (он же в метаданных объекта, `sha256`); только после
  неё `s3api copy-object` в ночной ключ и строка `<ключ> <sha256> <размер>`
  в манифест `/var/lib/lingua-backup/manifest` (root, `0600`), временный
  объект удаляется и при сбое. Затем чистка по retention;
  `--prune-only [--dry-run]` — только чистка. Любой сбой — код ≠ 0.
- Retention 7/30 ([ADR-014](../architecture/decisions.md)): все копии
  за 7 суток, дальше по одной на ISO-неделю (самая свежая) до 30 суток,
  старше — удаляются; ~11 копий. Lifecycle на `backups/pg/` не ставится
  (комментарий в `deploy/s3-bootstrap.sh`), ручные копии с другими
  именами чистка не трогает, без копии моложе 7 суток — не чистит;
  ночные ключи с датой позже сегодняшней не удаляет, но называет
  и завершается с кодом 1. Решение — чистая функция `retention_plan`
  в `deploy/backup-lib.sh`, тесты — `scripts/test_backup_retention.py`
  (в `unittest discover` security CI).
- `deploy/restore-pg.sh` — `--list`; `--db БАЗА [--date | --key] [--force]
  [--insecure-no-verify]`: без `--date`/`--key` — самая свежая ночная
  копия не позже сегодня (UTC), в `POSTGRES_DB` — только явная; копия
  скачивается, сверяется по sha256 и размеру с манифестом (манифеста
  нет — с метаданными; ключа нет в манифесте или sha256 нет — отказ без
  `--insecure-no-verify`), разворачивается `pg_restore
  --single-transaction --exit-on-error --no-owner --no-privileges`;
  базы нет — создаётся, непустая — только с `--force` (`--clean`).
- `deploy/backup-lib.sh` — общая часть: чтение `deploy/.env` без
  исполнения, `BACKUP_S3_*` → `S3_*`, aws cli из `amazon/aws-cli:2.32.7`
  (закреплён digest'ом) с ключами только через окружение; манифест;
  переменная процесса `BACKUP_MANIFEST` (по умолчанию
  `/var/lib/lingua-backup/manifest`).
- `deploy/systemd/lingua-backup-pg.service` + `.timer` — ежедневно
  03:30 UTC, `Persistent=true`, `StateDirectory=lingua-backup` (`0700`);
  установка и восстановление —
  [deploy/README.md](../../deploy/README.md). **На VPS не установлены.**
- Ключи `.env` (опциональны, все пусты — из `S3_*` с предупреждением):
  `BACKUP_S3_BUCKET`, `BACKUP_S3_REGION`, `BACKUP_S3_ENDPOINT`,
  `BACKUP_S3_ACCESS_KEY_ID`, `BACKUP_S3_SECRET_ACCESS_KEY`. Задан хоть
  один — обязателен `BACKUP_S3_BUCKET`, не равный `S3_BUCKET` на том же
  эндпоинте; пара ключей — только целиком.
- Безопасность: токен `S3_*` уходит в LiveKit Egress и читает и удаляет
  дампы (хэши паролей, email). Решение — свой бакет и свой токен через
  `BACKUP_S3_*`; до этого — работа под общим токеном с предупреждением
  в журнале, долг D-8. Клиентское шифрование отложено (ADR-014).
- Проверено на одноразовом стенде (postgres:16-alpine с миграциями
  0001–0005, seed и выборкой ~2000 строк; SeaweedFS 3.97 как
  S3-совместимое хранилище — образы MinIO с Docker Hub и quay.io больше
  не раздаются): копия появляется в `backups/pg/2026-10-26.dump.zst`
  с `sha256` в метаданных; восстановление в новую базу того же сервера,
  повтор с `--force` поверх испорченной копии и разворот в пустой
  `POSTGRES_DB` второго сервера дают те же число строк и md5 содержимого
  всех 9 таблиц, расширения, последовательности, индексы и ограничения.
  Чистка на 45 датированных объектах оставила ровно 7 дневных и 4 недельных
  (2026-10-18, -11, -04, 09-27), ручную копию и чужие префиксы; при
  пропущенном воскресенье недельной стала суббота. Неверный секрет, остановленный
  контейнер, ошибка pg_dump, порча скачанного объекта и неполная пара
  `BACKUP_S3_*` завершаются кодом 1; секретов нет ни в журнале, ни
  в аргументах docker. Первый прогон на R2 стенда — после ревью.
- Правки ревью (раунд 1) проверены так же (SeaweedFS 3.97 с двумя
  токенами, два postgres:16-alpine): только ключи или только регион
  в `BACKUP_S3_*` и `BACKUP_S3_BUCKET` = бакет записей — отказ, полная
  замена — без предупреждения, все пусты — общий режим с предупреждением;
  подложенная копия `2099-12-31` с «своим» sha256 restore не выбирает,
  чистка называет её и завершается кодом 1, удалив устаревшие; подмена
  копии дня с «верным» sha256 в метаданных отвергнута по манифесту (даже
  с `--insecure-no-verify`); ручная копия без sha256 — только
  с `--insecure-no-verify`; `--db lingua` без `--date` — отказ, с `--date`
  на пустой `POSTGRES_DB` второго сервера — данные совпали; сбой сверки
  выгрузки не трогает ночной ключ и не оставляет `incoming/`.
  CI: job `shell` (shellcheck) в `ci.yml`.

### S1-1 Google OAuth2 и calendar_sync (2026-10-26)

- Миграция `backend/migrations/0012_google_calendar.up.sql` — колонка
  `users.google_refresh_token bytea` (только шифртекст); откат удаляет
  её и ожидающие `calendar_sync`. Номера 0008–0011 и 0013 заняты
  параллельными ветками.
- `backend/internal/gcal/` — `gcal.New` (nil без любого ключа, ошибка
  на битый ключ), `AuthCodeURL` (state, PKCE S256, `access_type=offline`,
  `prompt=consent`), `Exchange` (проверка выданного scope), `Seal`/`Open`
  (AES-256-GCM, id пользователя в associated data), `SealFlow`/`OpenFlow`
  (cookie подключения: state, verifier и срок, тот же AES-GCM с
  `google_oauth:<id>` в associated data), `Revoke`, `Calendar` →
  `Insert`/`Update`/`Delete` (Calendar API v3, календарь `primary`,
  `sendUpdates=all`, у каждого вызова свой таймаут 15 с; у клиента API
  нет `http.Client.Timeout`). Зависимость — `golang.org/x/oauth2` v0.37.0
  ([ADR-023](../architecture/decisions.md)).
- Эндпоинты (`httpapi/google.go`; в [04-api.md](../architecture/04-api.md)):
  `GET /auth/google`, `POST /auth/google/start`, `DELETE /auth/google` —
  под teacher; `GET /auth/google/callback` (вместо `POST` из плана: Google
  возвращает браузер GET-ом) — без middleware: сессию проверяет сам
  (`sessionUser` в `httpapi/middleware.go`) и всегда отвечает 303, без
  сессии — на `/login`. Cookie `__Host-google_oauth` (запечатанный
  `SealFlow`, 10 мин, Path `/`, Secure, HttpOnly, SameSite=Lax).
  `DELETE /auth/google` отвечает `200 {"revoked": bool}`, отзыв — на
  `context.WithoutCancel`. Redirect URI —
  `https://<DOMAIN>/api/auth/google/callback`.
- Джоба `calendar_sync` (`backend/internal/store/calendar.go`:
  `store.JobCalendarSync`, payload `{"lesson_id": N}`, у отмены ещё
  `"teacher_id"` и `"event_id"`) — в транзакциях `CreateLesson`,
  `RescheduleLesson` при подключённом календаре организатора,
  в `CancelLesson` всегда, когда у урока есть `gcal_event_id`, и
  в `SetGoogleToken` для его будущих уроков; там же `SetGoogleToken`
  возвращает в работу ждавшие удаления этого преподавателя (`attempts = 0`,
  `failed_at = NULL`). `lessons.gcal_event_id` назначается там же (32 hex
  из `gen_random_uuid()`); хендлер — `worker.RegisterCalendar`
  (`internal/worker/calendar.go`): insert, при 409 — update, удаление —
  404/410 тоже успех. Без рабочего токена (нет ключей, не подключён, токен
  не расшифровался или отозван — `invalid_grant` снимает подключение)
  создание и перенос пропускаются со строкой в журнале, а удаление
  возвращает ошибку «ждёт повторного подключения календаря» и остаётся
  в очереди.
- Web: `web/app/account/google-calendar.tsx` — раздел «Google Календарь»
  на `/account` только у преподавателя: подключить/отключить, итог
  возврата из `?google=` (и `not_revoked`, если отключение не отозвало
  токен в Google), без ключей — «недоступно». Типы `GoogleCalendarStatus`
  и `GoogleDisconnectResult` в `web/lib/api.ts`.
- Ключи `.env` (опциональны, только все три вместе): `GOOGLE_CLIENT_ID`,
  `GOOGLE_CLIENT_SECRET`, `GOOGLE_TOKEN_KEY` (`openssl rand -base64 32`);
  проброс в api и worker, worker получает ещё `DOMAIN` (ссылка на урок
  в описании события). Создание OAuth-клиента и перевод в Production —
  [deploy/README.md](../../deploy/README.md#google-calendar-s1-1).
- Тесты на httptest-фейке Google и схеме `security_test`:
  `httpapi/google_test.go` (роли, state/PKCE/отказ/снятый scope, в колонке
  нет открытого токена, отключение с отзывом и `revoked:false` при сбое,
  смене ключа и отменённом запросе, подложенный, чужой и истёкший cookie
  без обмена кода, callback без сессии и не преподавателем, без ключей,
  откат 0012), `httpapi/calendar_sync_test.go` (insert/update/delete,
  повторы, сбой Google, отмена до синхронизации, отключённый и отозванный
  календарь, отмена после отключения, `invalid_grant` и смены ключа
  удаляет событие после переподключения, отмена посреди insert,
  CAS `ClearGoogleToken`), `gcal/gcal_test.go` (в том числе cookie
  подключения и таймаут вызова API).
- Порядок выката: `make migrate` до api — `CreateLesson`/`RescheduleLesson`
  читают `users.google_refresh_token` и без ключей Google; без `0012`
  создание и перенос урока отвечают 500.
- Долг D-7 закрыт. С настоящим Google не проверено — OAuth-клиента нет,
  долг D-13; DoD «токен переживает неделю» и «отражается в календарях
  студентов» — после его создания.

### Долги D-4 и D-5: файлы материалов и архив учёток (2026-10-26)

- Миграция `backend/migrations/0013_material_files_archive.up.sql` (номер
  из резерва параллельных веток): рядом с `materials.s3_key` —
  `file_name`, `file_content_type`, `file_size`, `file_uploaded_at`,
  `file_etag`; `CHECK` `materials_file_complete` (с явными `IS NOT NULL`,
  подтверждение и ETag вместе) и `materials_file_key_layout` (ключ только
  под `materials/{lesson_id}/`), уникальный `materials_s3_key_idx`;
  `users.deactivated_at`. Откат обнуляет `s3_key`, объекты в бакете
  остаются. Решения — [ADR-024](../architecture/decisions.md).
- `backend/internal/storage/s3.go` — `PresignPut` (тип, длина и
  `Content-Disposition` в подписи; `Host` и `Content-Length` браузеру
  не отдаются), `Stat` (размер, тип, ETag; заменил неиспользуемый
  `Exists`), `Copy` (CopyObject внутри бакета), `Delete`,
  `NewMaterialKey` → `materials/{lesson_id}/<32 hex>.<ext>`,
  `PendingMaterialKey` → `materials-pending/{lesson_id}/<32 hex>.<ext>`,
  `MaterialFileKeys`.
- `backend/internal/httpapi/materials.go` — `POST /lessons/{id}/materials`
  и `/homework` с полем `file` (PUT подписывается на `materials-pending/`,
  расширение имени — по типу, Unicode Cc/Cf в имени — 400),
  `POST /materials/{id}/confirm` (сверка, копия на постоянный ключ, ETag),
  `GET /materials/{id}/url` (HEAD и сверка с ETag перед подписью, 409),
  `DELETE /materials/{id}`; белый список `materialFileTypes`,
  `maxMaterialFileBytes` = 50 MiB, `uploadURLTTL` = 10 мин. Хранилище
  в api — за интерфейсом `objectStore` (`api.go`). `GET /lessons/{id}`
  прячет неподтверждённые файлы от участников.
- Джоба `material_files_delete` (`store.JobMaterialFilesDelete`, payload
  `{"keys": [постоянные ключи], "objects": [прочие объекты урока]}` —
  `objects` с ревью волны A: снимок доски отменённого урока, удаляется как
  есть, ADR-020): ставят `store.CancelLesson` в транзакции
  отмены урока и `store.DeleteMaterial`, когда api не удалось удалить
  объект; хендлер — `worker.RegisterMaterialFiles` в
  `backend/internal/worker/worker.go` (оба объекта каждого ключа),
  подключается в `backend/cmd/worker/main.go`, только если заданы `S3_*`;
  `deploy/docker-compose.yml` передаёт worker'у те же `S3_*`.
- Архив: `store.DeactivateUser` (отметка и `DELETE FROM sessions` одной
  транзакцией), `ReactivateUser`; `UserBySessionTokenHash` отсекает
  архивных. `POST /users/{id}/deactivate` и `/reactivate` (права —
  `authorizeManage`, себя — 403), `GET /users?archived=1`, логин
  архивного — 403 `account_deactivated`. `CreateLesson` архивных
  в участники не берёт; в составе групп и участниках урока — флаг `archived`.
  После архивации — `RemoveParticipant` из комнат идущих уроков
  (`store.LiveLessonsOf`, `backend/internal/livekitapi/room.go`), без
  ключей LiveKit — строка в журнале.
- Web: `web/app/lesson/[id]/material-files.tsx` — «Прикрепить файл»
  в материалы или домашку (заявка → PUT в R2 → confirm; сбой — заготовка
  удаляется), «Скачать», «Удалить»; `/admin` — «В архив» у человека,
  секция «Архив» с возвратом, отметка «в архиве» в составе групп.
- `deploy/s3-bootstrap.sh` — одно CORS-правило на `GET`, `HEAD`, `PUT`
  с заголовками `Content-Type`, `Content-Disposition`, `Range` и
  lifecycle-правило `materials-pending-expire` (сутки). **На бакете стенда
  не применено** — долг D-14, порядок — [deploy/README.md](../../deploy/README.md).
- S3: префиксы `materials/{lesson_id}/` и `materials-pending/{lesson_id}/`.
  Новых ключей `.env` и зависимостей нет.
- Тесты на схеме `security_test`: `httpapi/materials_test.go` (фейк
  хранилища: лимиты, права, имя для скачивания, 409/422, равенство
  размера, charset, копия на постоянный ключ, подмена после подтверждения,
  гонка отказа с удалением, 503 без хранилища, `CHECK` 0013, джоба
  уборки с повтором после сбоя), `httpapi/archive_test.go` (сессии, логин,
  списки, снапшот, `RemoveParticipant` на httptest-фейке LiveKit,
  обратимость 0013); `storage/s3_test.go` — точный набор заголовков
  подписи PUT, `Stat`/`Copy`/`Delete` на httptest.

### Ревью раунда 1, F1: сегменты записи, окно комнаты, вебхук LiveKit (2026-10-26)

Решения — [ADR-031](../architecture/decisions.md); правки текста ADR-009,
ADR-010, ADR-012 на месте.
- Миграция `0018_lesson_recordings` (обратима: down удаляет таблицу, джобы
  и расписание `recording_sweep`): таблица `lesson_recordings` — сегмент
  на каждый запуск Egress (`egress_id` UNIQUE, `room_sid`, `s3_key`,
  `status` active/complete/failed, `size_bytes`, `error`, `ended_at`).
- S3: сегмент `recordings/{lesson_id}/{YYYYMMDDTHHMMSSZ}-{16 hex}.mp4|.ogg`
  (`storage.NewRecordingKey`; `RecordingKey` с фиксированным `room.*`
  удалён). `lessons.recording_s3_key` — выбранный сегмент.
- `store.StartLessonRecording(ctx, lessonID, roomSID, key, start)`: только
  в окне `starts_at` − 15 мин … `ends_at` + 15 мин
  (`store.RoomOpensBefore`/`RoomClosesAfter`, `store.RoomWindow`), урок
  в scheduled или live, без active-сегмента той же сессии комнаты;
  захват `FOR NO KEY UPDATE SKIP LOCKED` вместо `FOR UPDATE` из S1-5,
  а `JoinAsGuest` читает урок `FOR KEY SHARE` вместо `FOR SHARE` из S1-11:
  вход гостя больше не ждёт вызова LiveKit.
- `store.EndLessonRecording` (вместо `FinishLessonRecording`): upsert
  сегмента по `egress_id`, затем `closeLessonIfOver` — после окна и без
  active-сегментов урок → processing с самым большим complete-сегментом
  и джобой `recording_ready`, без записи → done. Условие на урок —
  `store.lessonToClose`, одно на вебхук и уборку: live, scheduled
  с сегментами (ADR-012) и, после ревью волны B, done без
  `recording_s3_key` и транскрипта с complete-сегментом — запоздавший
  `egress_ended` отдаёт уроку запись.
- Джоба `recording_sweep` (`store.SweepRecordings`,
  `worker.RegisterRecordingSweep(runner, scheduler, st, egress)`, каждые
  5 мин через `jobs.Scheduler`, только роль core): сначала
  `store.RecordingsPastWindow` и `livekitapi.Client.StopEgress`
  (`livekit.Egress/StopEgress`) для каждого active-сегмента урока
  с закрытым окном — после ревью волны B; затем закрывает уроки с закрытым
  окном, active-сегмент старше `ends_at` + 3 ч помечает failed
  «egress_ended не пришёл». Worker читает `LIVEKIT_URL`, `LIVEKIT_API_KEY`,
  `LIVEKIT_API_SECRET` (compose передаёт их и worker'у; без них остановки
  нет, строка в журнале).
- `POST /webhooks/livekit`: тело ≤ 1 MiB до проверки подписи (413);
  `egress_ended` с `EGRESS_FAILED`/`EGRESS_ABORTED` или без файлов — 200,
  сегмент failed; `lessonIDFromRoom` — только каноническое `lesson-{id}`.
- `GET /lessons/{id}/room-token`: 409 `room_not_open` до окна,
  `lesson_finished` после; TTL токена — до конца окна, не больше 3 ч.
- Хранилище: `storage.New` без `S3_ENDPOINT` отвергает пустой регион
  и `auto` (`ErrNoRegion`), `Client.Region()` — разрешённый регион, его же
  получает Egress. Compose: `S3_REGION: ${S3_REGION:-}` у api и worker.
  Новых ключей `.env` и зависимостей нет.
- Тесты (схема `security_test`): `httpapi/webhooks_test.go` — сегменты
  посреди урока, новая сессия комнаты, уборка; сбои Egress и потерянный
  сегмент; 401 на подделки без изменений; потолок тела; окно комнаты;
  вход гостя не ждёт вызова LiveKit при запуске записи; обратимость 0018;
  после ревью волны B — остановка Egress после окна
  (`TestSweepStopsEgressAfterWindow`), запоздавший сегмент после уборки
  (`TestLateSegmentAfterSweepAttachesRecording`), путь ADR-012
  (`TestScheduledLessonWithUncommittedSegment`), охранные условия
  сегментов (`TestSegmentGuards`), `livekitapi/egress_test.go`
  (`TestStopEgress`); `httpapi/media_test.go` — матрица доступа к записи;
  `egress_jobs_test.go` и `transcripts_test.go` (ASR) — на сегментах после
  окна; `storage/s3_test.go` — ключ сегмента и регион.
- Долг D-20: плеер и ASR — только самый большой сегмент.

### Ревью раунда 1: события, гости, документация (2026-10-26)

Решения — [ADR-032](../architecture/decisions.md), отложенное — долг D-21.

- `backend/migrations/0006_event_constraints.up.sql` — правка на месте
  (стенд на версии 5, `0006` там не выполнялась): до `ADD CONSTRAINT`
  `title` у урока с группой → `NULL`, гостевая ссылка урока с группой
  отзывается, событие с пустым названием получает `Событие #<id>`.
  `TestMigration0006NormalizesStandRows` накатывает 0001–0005
  (`testdb.DSNBefore`), кладёт такие строки и применяет `0006`.
- `store.JoinAsGuest` — потолок `MaxGuestsPerEvent` = 100 гостей
  на событие (счёт под блокировкой строки урока), сверх — 409 `event_full`;
  срок сессии — `ends_at` + `GuestSessionGrace` (2 ч) по заблокированной
  строке. `store.RescheduleLesson` сдвигает сессии гостей события.
  Cookie гостя — `sessionTTL`, срок держит БД. Синтетический email —
  из отдельных 128 случайных бит, а не из хэша сессии.
- `PATCH /auth/me` — гостю имя не длиннее 60 (`maxGuestNameLen`).
- `GET /lessons/{id}` — организатору события `has_guest_link`
  (`store.LessonDetail.HasGuestLink`).
- Web: `web/app/lesson/[id]/guest-link.tsx` — флаг `hasLink` с сервера,
  перевыпуск выпущенной ссылки только после подтверждения, «Отозвать»
  без нового выпуска; блок виден до `ends_at`. `web/lib/roles.ts` — роль
  `guest` («гость»); «Люди и группы» в шапке — только admin и teacher;
  `/lessons` запрашивает группы только у преподавателя.
- Plyr без cdn.plyr.io: `web/scripts/plyr-assets.mjs` (в `predev`
  и `prebuild`) копирует `node_modules/plyr/dist/plyr.svg` в git-ignored
  `web/public/plyr/`; в `lesson-player.tsx` — `iconUrl: "/plyr/plyr.svg"`,
  `blankVideo: ""`.
- Тесты в `httpapi/guests_test.go` на схеме `security_test`:
  `TestLessonDetailHasGuestLinkForOwnerOnly`,
  `TestGuestLimitsEmailAndAdminList` (потолок, `PATCH /auth/me`, email,
  гости вне `GET /users` и `?archived=1`), `TestRescheduleMovesGuestSessions`.
- Доки: `docs/security-ci.md` переписан по `security.yml` и ADR-011;
  задачи и DoD S1-11/S1-12, DoD этапа S1-1…S1-12, выходные артефакты;
  `AGENTS.md` — необязательные ключи и раскладка; ADR-009 и ADR-012 —
  `aws-sdk-go-v2` и свой Twirp-клиент; ADR-010 — срок сессии гостя.
- Новых таблиц, ключей `.env`, джоб, путей S3 и зависимостей нет.

## Риски этапа

GCP Testing mode (лечится сразу Production); free tier LiveKit ≈ 20 уроков/мес —
для пилота ок, пороги в [06-budget.md](../architecture/06-budget.md);
composite-egress прожорлив — на слабой VPS-конфигурации выбирать audio-only.
