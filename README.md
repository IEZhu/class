# Lingua Class

[![CI](https://github.com/IEZhu/class/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/IEZhu/class/actions/workflows/ci.yml?query=branch%3Amain)
[![Security](https://github.com/IEZhu/class/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/IEZhu/class/actions/workflows/security.yml?query=branch%3Amain)

## Automated checks

Pull requests and pushes to `main` run the complete Go suite with the race detector against a disposable Postgres database, Go module verification and vet, TypeScript checking, and the production Next.js build. The database tests exercise the actual HTTP router and SQL store: role escalation, private lesson and LiveKit room access, session expiry/logout/password revocation, and concurrent single-use invitation acceptance. They fail in CI when the database is unavailable. Locally, set `TEST_DATABASE_URL` to a disposable database before running `cd backend && go test -race -count=1 ./...`; each test creates and removes its own schema.

The `security` workflow checks both dependency manifests and source secrets, then scans the actual API, web, Caddy, and Postgres runtime images. It runs on pull requests, `main`, daily, and manually. Every known vulnerability severity, including unfixed findings, blocks the job. Scan failures and missing runtime/package inventories also block; there is no ignore file or report-only image. The migration utility under the optional Compose `tools` profile is outside the runtime image matrix. These checks cover known dependency advisories and tested access boundaries; they do not establish that the application has no unknown vulnerabilities. No check deploys the stack or contacts a production database or LiveKit service.

**English summary.** Lingua Class is a self-hosted web platform for a small
English-language class (5 teachers, 50 students) built with Next.js, Go and
Postgres on Docker Compose for a single VPS. Working today: accounts and roles,
invitation links, groups, lessons and homework, and a LiveKit video lesson room.
Planned: lesson recordings in S3, speaker-labelled transcripts (AssemblyAI),
lesson and personal vocabulary, FSRS spaced repetition, flashcards, cloze tests
and subtitle-based video practice. Early MVP; the documentation is in Russian.

Платформа для группы изучающих английский (5 преподавателей / 50 студентов,
hobby-MVP): календарь → видеоуроки (LiveKit) → записи (S3) → транскрипты по
спикерам (AssemblyAI) → словарь урока и личный словарь → SRS-повторения (FSRS)
→ практика (карточки / видео по субтитрам / тексты / cloze-тесты).

Ядро — монолит на одном VPS (Docker Compose: Next.js + Go + Postgres),
тяжёлые подсистемы — managed/OSS. Медиа через VPS не ходит.
Смета: ~$15–25/мес (пилот).

## Навигация

- **Обзор**: [docs/README.md](docs/README.md)
- **Архитектура**: [docs/architecture/](docs/architecture/) — топология,
  сервисы, модель данных, API, S3, интеграции, потоки
- **Планы этапов 0–5**: [docs/plans/](docs/plans/) — каждый план со входными
  и выходными артефактами
- **Индекс задач и статусы**: [docs/tasks/README.md](docs/tasks/README.md)
- **Роль и протокол работы**: [AGENTS.md](AGENTS.md)

## Структура

```text
backend/   Go: cmd/api, internal/, миграции     (cmd/worker появится в S1-2)
web/       Next.js
deploy/    docker-compose, Caddyfile, .env.example
docs/      архитектура, планы, индекс задач
```

## Запуск стенда

```bash
cp deploy/.env.example deploy/.env   # заполнить реальными значениями
make up        # docker compose up -d --build
make migrate   # миграции golang-migrate
make seed      # тестовые данные (SEED_PASSWORD опционален)
```

Остальные цели: `make ps`, `make logs`, `make psql`, `make down`, `make build`.
Подробнее о сервисах — [deploy/README.md](deploy/README.md) и
[docs/architecture/02-services.md](docs/architecture/02-services.md).

## Статус

Этап 0 закрыт, идёт этап 1: учётные записи, приглашения и видеокомната урока
работают.
Текущий этап и задача — в [docs/tasks/README.md](docs/tasks/README.md).
