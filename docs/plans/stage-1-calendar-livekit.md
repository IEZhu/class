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

## Definition of Done этапа

- [ ] S1-1…S1-7 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: урок в календаре → комната → запись в S3 → плеер
- [ ] «Факт» заполнен (ключи `.env`, имена джоб, пути S3)

## Факт (заполняется по ходу реализации)

_пока пусто_

## Риски этапа

GCP Testing mode (лечится сразу Production); free tier LiveKit ≈ 20 уроков/мес —
для пилота ок, пороги в [06-budget.md](../architecture/06-budget.md);
composite-egress прожорлив — на слабой VPS-конфигурации выбирать audio-only.
