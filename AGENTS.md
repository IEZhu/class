# Lingua Class

Lingua Class (working name) is a platform for a small English-learning group (5 teachers,
50 students; hobby MVP on one VPS): calendar, LiveKit video lessons, recordings in S3, speaker
transcripts, lesson and personal dictionaries, FSRS spaced repetition and practice. Core is a
Docker Compose monolith (Next.js + Go + Postgres); heavy subsystems are managed or OSS services.
Status: stage 0 is done, stages 1–5 are in progress; per-task status, debts and the current
focus are in `docs/tasks/README.md`. All project docs, the task index and commit descriptions
are in Russian.

## Layout

- `backend/`: Go module `github.com/IEZhu/class/backend` (Go 1.26). `cmd/api/main.go` (HTTP API),
  `cmd/worker/main.go` (background jobs), `cmd/dictimport/` and `cmd/tatoebaimport/` (one-off
  dictionary and Tatoeba imports shipped in the worker image, ADR-019, ADR-028),
  `internal/httpapi/` (routes, auth, middleware, groups, lessons, invites, rooms, users, guests
  and `/join`, media and material URLs, transcripts, word clicks, user terms,
  lesson dictionary (candidates, picker, assignment, ADR-033/034), practice texts and videos,
  review queue and reviews, cloze practice and LLM distractors (ADR-037),
  whiteboard, Google Calendar, LiveKit and AssemblyAI webhooks), `internal/store/` (Postgres via pgx),
  `internal/jobs/` (Postgres queue: enqueue in the caller's transaction, `SKIP LOCKED` claim,
  retries, ADR-013; periodic `Scheduler`, ADR-021), `internal/worker/` (job handlers by kind),
  `internal/storage/` (R2/S3 via `aws-sdk-go-v2`: presigned GET/PUT, object keys, ADR-009),
  `internal/livekitapi/` (own Twirp client for Egress, ADR-012), `internal/assemblyai/`
  (ADR-015), `internal/webvtt/`, `internal/lemma/` (ADR-017), `internal/dictionary/`
  (ADR-019), `internal/translate/` (LibreTranslate, ADR-018), `internal/gcal/` (ADR-023),
  `internal/harvester/` (subtitles, ADR-022), `internal/dumpfile/` (dump download and
  decompression shared by the imports), `internal/tatoeba/` (ADR-028), `internal/llm/` (LLM
  providers, ADR-029), `internal/textgen/` (prompt and coverage validator, ADR-030),
  `internal/lessonterms/` (lesson dictionary candidates from whiteboard text, ADR-033),
  `internal/srs/` (FSRS scheduling over go-fsrs, ADR-036), `internal/explain/` (the popup
  «объясни» prompt and output bounds, ADR-039), `internal/cloze/` (cloze sentences, blanks,
  distractors, answer rating and LLM distractor validation, ADR-037),
  `internal/testdb/` (the shared DB test harness),
  `migrations/` (golang-migrate `NNNN_*.up.sql` / `.down.sql`, plus `seed.sql`, which is not a
  migration).
- `web/`: Next.js 15 + React 19 app router (`app/login`, `app/lessons`, `app/lesson/[id]`,
  `app/admin`, `app/account`, `app/invite/[token]`, `app/dictionary`, `app/practice/text`,
  `app/practice/video` (YouTube embed and subtitle panel, ADR-038),
  `app/practice/texts`, `app/practice/cards` (FSRS review cards), `app/practice/cloze` (fill in the
  word, ADR-037), public `app/join/[token]` for
  event guests); `lib/api.ts` calls the API server-side; `lib/roles.ts`, `lib/lesson-phase.ts`,
  `lib/lesson-title.ts`, `lib/review-session.ts`, `lib/cloze-session.ts`, `lib/timeline.ts` (player cue timing shared by
  the transcript and video practice). Интерфейс (ADR-040): `app/globals.css` — единственный глобальный CSS
  (дизайн-токены светлой и тёмной темы, база, классы общих компонентов; оглавление — в шапке файла);
  `app/app-shell.tsx` — каркас с навигацией по роли, `app/user-bar.tsx` его реэкспортирует; `app/ui/` — общие
  React-компоненты (кнопка, поле, сообщение, иконки `icons.tsx`, время в поясе зрителя `local-time.tsx`); стили одной
  страницы — CSS Module рядом с ней, без inline-цветов и без новых npm-пакетов. Every text renders through
  `app/clickable-text.tsx` (`<ClickableText>`, ADR-026); its tokenizer and status cache are pure
  functions in `lib/` tested by `npm test`. `scripts/` copies the Excalidraw fonts and the Plyr
  sprite into the git-ignored `public/excalidraw/` and `public/plyr/` before dev and build.
- `deploy/`: `docker-compose.yml` (caddy, web, api, worker, postgres; `migrate` under the
  `tools` profile; `libretranslate` under the `translate` profile and its one-off model installer
  `libretranslate-models` under `translate-setup`, ADR-018; the whiteboard relay `whiteboard`
  under the `whiteboard` profile, ADR-020;
  the subtitle `harvester` under the `harvester` profile, ADR-022), `Caddyfile`, `.env.example`;
  `backup-pg.sh` / `restore-pg.sh` (+ `backup-lib.sh`, `systemd/` timer): nightly pg_dump to S3
  and restore (S1-7, ADR-014).
- `Makefile`: operations for the compose stack.
- `docs/architecture/`: topology, services, data model, API, S3, integrations, flows, ADR log
  (`decisions.md`); `00-source-variant-c.md` is a frozen snapshot.
- `docs/plans/`: stage plans 0–5 and the pipeline rules (`00-flow.md`).
- `docs/tasks/README.md`: the single index of task statuses and debts.
- `history.md`: machine-written agent session log; do not edit unless asked.

## Setup / Build / Run / Test

```bash
cp deploy/.env.example deploy/.env   # then fill it; make refuses "change-me" values
make up        # check-env, then docker compose up -d --build
make migrate   # golang-migrate up (compose service "migrate")
make seed      # migrate, then load backend/migrations/seed.sql (SEED_PASSWORD optional)
# also: make ps, make logs, make psql, make down, make build
```

- Required keys in `deploy/.env` (checked by `make check-env`): `DOMAIN`, `POSTGRES_DB`,
  `POSTGRES_USER`, `POSTGRES_PASSWORD`. Optional: `HTTP_PORT` (default 8090), `SEED_PASSWORD`,
  `LIVEKIT_URL`, `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` (without them the lesson room returns
  503), `S3_BUCKET`, `S3_REGION`, `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`
  (R2 by default, ADR-009; without them media and material file URLs return 503 and no
  recording is started; with them but without `S3_ENDPOINT` an empty or `auto` `S3_REGION`
  stops api and worker at start, ADR-031), `EGRESS_AUDIO_ONLY=1` (record OGG audio instead of MP4),
  `BACKUP_S3_*` for `deploy/backup-pg.sh` (all empty → the recordings bucket and token
  with a journal warning, debt D-8; any one set → `BACKUP_S3_BUCKET` is required),
  `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, `GOOGLE_TOKEN_KEY` (all three or the
  Google Calendar connection is unavailable and `calendar_sync` skips lesson creates and
  reschedules while event deletes wait in the queue, ADR-023),
  `ASSEMBLYAI_API_KEY` and `ASSEMBLYAI_WEBHOOK_SECRET` (without the key a recorded lesson
  goes straight to `done` without a transcript; ADR-015), `LIBRETRANSLATE_URL` with
  `COMPOSE_PROFILES=translate` (word translation; without them `POST /terms/click` answers
  `translation: null`, ADR-018), `COMPOSE_PROFILES=whiteboard` with
  `WHITEBOARD_ENABLED=1` (the lesson whiteboard; without them it returns 503; several profiles
  are comma-separated: `translate,whiteboard`),
  `HARVESTER_COOKIES_FILE` (YouTube cookies for the `harvester` profile, readable by uid 10001),
  `HARVESTER_DATABASE_URL` (the harvester's own least-privilege role, created by
  `deploy/harvester-db-role.sh`; empty → the owner DSN, ADR-022),
  `LLM_PROVIDER`, `LLM_API_KEY`, `LLM_MODEL`, `LLM_DAILY_LIMIT` (api only: text generation,
  503 without the key, ADR-029), `LLM_EXPLAIN_DAILY_LIMIT` (the popup «объясни» and cloze LLM distractors, ADR-039, ADR-037). The api also
  reads `DATABASE_URL` and `API_ADDR` (default `:8080`); the worker reads `DATABASE_URL`,
  `WORKER_ROLE` (`core` by default; `harvester` runs only the subtitle harvester jobs, plus
  `HARVESTER_COOKIES`, ADR-022) and `S3_*` (whiteboard snapshots and `material_files_delete`
  wait in the queue without storage, ADR-024), `LIVEKIT_*` (`recording_sweep` stops Egress still
  recording after the room window; without them it does not, ADR-031), plus `DOMAIN` and
  `GOOGLE_*` for `calendar_sync`; with `ASSEMBLYAI_API_KEY` it also needs the webhook
  secret, `S3_*` and `DOMAIN` (it refuses to start if one is missing); web reads
  `INTERNAL_API_URL` (default `http://api:8080`).
- Frontend alone: `cd web && npm ci && npm run dev` (scripts: `dev`, `build`, `start`, `test` —
  `node --test` over `lib/*.test.ts`; `predev` and `prebuild` copy the Excalidraw fonts and the
  Plyr sprite into the git-ignored `public/excalidraw/` and `public/plyr/`).
- Backend tests: `cd backend && go test -race -count=1 ./...`. Set `TEST_DATABASE_URL`
  to a disposable Postgres database for the HTTP/SQL security suite and the `internal/jobs`
  queue tests; they create isolated schemas and apply the real migrations through the one
  shared harness `internal/testdb` (advisory lock, `pgcrypto` in `public`). CI requires this
  database and does not skip those tests. `.github/workflows/ci.yml` also verifies modules,
  runs vet, builds every `cmd`, checks web types, runs `npm test` and builds Next.js.
  `.github/workflows/security.yml` blocks all known dependency
  and runtime vulnerabilities, unfixed findings, source secrets, and missing inventories.
  Package-level Go absence proofs must match the exact source snapshot or every
  inventoried binary in the exact OCI image; affected imports and incomplete,
  stale or malformed proofs block. Runtime Caddy/Postgres builds are defined in
  `deploy/`, retain third-party module versions, and preserve upstream contracts.

## Conventions

- Commits: Conventional Commits with the task ID, description in Russian, e.g.
  `feat(api): S1-8 роль admin и управление учётными записями`, `fix(web): ...`, `docs: ...`.
  One task may span several commits, never the reverse.
- Work goes through PRs from branches like `feat/s1-10-invites` into `main`.
- Postgres DDL follows ADR-005; auth is cookie sessions in Postgres with bcrypt (ADR-006);
  roles are `teacher`, `student` and `admin` (ADR-007), plus `guest` for open events
  (password-less, one event, ADR-010).
- Any choice not covered by the docs (library, format, deviation) gets an ADR entry in
  `docs/architecture/decisions.md` before the code.

## Gotchas and boundaries

- Never commit `deploy/.env` or any `.env.*` except `.env.example`; every new key goes to
  `deploy/.env.example` and to the stage plan's «Факт» section first.
- TLS terminates at the host's nginx (ADR-004); caddy is HTTP-only and published on
  `127.0.0.1:${HTTP_PORT}`. Caddy strips `/api`, so API routes have no `/api` prefix inside Go.
- `backend/Dockerfile` has three targets, `worker`, `harvester` (worker + pinned yt-dlp, compose
  profile `harvester`, outside the blocking image scan: debt D-12) and `api`; `api` is the last
  stage, so a build without `--target` still yields the api. Apply migrations before deploying an api that
  enqueues a new job: `egress_ended` fails without the `jobs` table.
- `README.md` also links here; `docs/README.md` and `docs/plans/00-flow.md` still link to
  `CLAUDE.md`, which imports this file.

## Role and stage protocol (moved from CLAUDE.md)

Summary in English: you are the lead engineer; architecture is decided, so implement stage by
stage from `docs/plans/` without re-architecting. For each task: read the current focus in
`docs/tasks/README.md`, verify the stage plan's «Входные артефакты» exist, implement within the
invariants and the data model, then mark the task `☑`, fill the plan's «Факт» section and commit
with the task ID. Stub anything that belongs to a later stage and record it as a debt. The
original wording below is authoritative.

### Проект

**Lingua Class** (рабочее название) — платформа для группы изучающих английский:
5 преподавателей / 50 студентов, hobby-MVP на одном VPS.

Архитектура зафиксирована: **вариант C, гибрид** — ядро и модель данных свои
(Docker Compose: Next.js + Go + Postgres), тяжёлые подсистемы — managed/OSS
(LiveKit Cloud, AssemblyAI, AWS S3, LibreTranslate, excalidraw-complete).
**Видео и аудио никогда не проходят через VPS.**

Точка входа в документацию: `docs/README.md`.
Исходный архитектурный документ (снапшот, не редактируется):
`docs/architecture/00-source-variant-c.md`.

### Роль

Ты — ведущий инженер этого репозитория. Архитектурные решения уже приняты
(`docs/architecture/`); твоя работа — реализация по этапам 0–5 из `docs/plans/`,
без пересмотра архитектуры «по пути». Если реализация требует отступить от
доков — сначала короткая запись в `docs/architecture/decisions.md` (ADR),
потом код.

### Протокол «flow по этапам»

1. **Ориентация.** Открой `docs/tasks/README.md` — там текущий этап, первая
   незакрытая задача и список отложенных долгов. Детали задачи — в плане
   этапа `docs/plans/stage-N-*.md`.
2. **Проверка входа.** В плане этапа есть раздел «Входные артефакты» —
   убедись, что артефакты предыдущих этапов реально существуют (таблицы,
   эндпоинты, файлы, ключи `.env`). Входа нет → сначала закрой долг,
   задачу не начинай.
3. **Реализация.** Соблюдай инварианты (ниже) и модель данных
   (`docs/architecture/03-data-model.md`).
4. **Фиксация выхода.** По завершении задачи:
   - статус в `docs/tasks/README.md` (`☐` → `☑`);
   - раздел «Факт» в плане этапа: что реально создано — файлы, таблицы,
     эндпоинты, конфиги, новые ключи `.env`. Это входные артефакты для
     следующих этапов, они должны быть найдены по этому разделу;
   - git-коммит с ID задачи в теме, напр. `feat(api): S1-3 presigned выдача из api`.
5. **Долги.** Если задача упирается в будущий этап (пример: попапу слова нужны
   примеры Tatoeba, а импорт — этап 5) — не тащи функциональность раньше
   времени: оформи заглушку и запиши долг в таблицу «Долги» в
   `docs/tasks/README.md` с указанием закрывающей задачи.
6. **Решения.** Любой выбор, которого нет в доках (библиотека, формат,
   отступление) — строка в `docs/architecture/decisions.md`.

### Инварианты (не нарушать)

- Видео/аудио уроков **не проходят через VPS**: медиа — LiveKit Cloud,
  файлы — S3 → браузер напрямую по presigned URL (TTL ~30 мин).
- S3-бакет приватный; доступ только через presigned URL, выданный api.
- **Любой текст** (материал, транскрипт, сгенерированный текст, предложение
  Tatoeba, субтитр) рендерится одним компонентом `<ClickableText>`.
- Очереди фоновых задач — Postgres (`FOR UPDATE SKIP LOCKED`); Redis — опция,
  не зависимость.
- Секреты только в `.env` (шаблон — `deploy/.env.example`), не в коде и не в git.
- Тексты и состояние — в Postgres; бинарные файлы — в S3.

### Раскладка репозитория

| Каталог | Что там |
|---|---|
| `backend/` | Go-модуль: `cmd/api`, `cmd/worker`, `internal/…`, миграции |
| `web/` | Next.js (SSR/React) |
| `deploy/` | docker-compose.yml, Caddyfile, .env.example, бэкап-скрипты |
| `docs/architecture/` | топология, сервисы, модель данных, API, S3, интеграции, потоки |
| `docs/plans/` | планы этапов 0–5 с входными/выходными артефактами |
| `docs/tasks/README.md` | единый индекс задач, статусов и долгов |
