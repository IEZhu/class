# Lingua Class

[![CI](https://github.com/IEZhu/class/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/IEZhu/class/actions/workflows/ci.yml?query=branch%3Amain)
[![Security](https://github.com/IEZhu/class/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/IEZhu/class/actions/workflows/security.yml?query=branch%3Amain)

## Automated checks

Pull requests and pushes to `main` run the complete Go suite with the race detector against a disposable Postgres database, Go module verification and vet, TypeScript checking, and the production Next.js build. The database tests exercise the actual HTTP router and SQL store: role escalation, private lesson and LiveKit room access, session expiry/logout/password revocation, and concurrent single-use invitation acceptance. They fail in CI when the database is unavailable. Locally, set `TEST_DATABASE_URL` to a disposable database before running `cd backend && go test -race -count=1 ./...`; each test creates and removes its own schema.

The `security` workflow checks both dependency manifests and source secrets, then scans the actual API, web, Caddy, and Postgres runtime images. It runs on pull requests, `main`, daily, and manually. Every applicable known vulnerability severity, including unfixed findings, blocks the job. Scan failures and missing runtime/package inventories also block; there is no ignore file or report-only image. The migration utility under the optional Compose `tools` profile is outside the runtime image matrix. These checks cover known dependency advisories and tested access boundaries; they do not establish that the application has no unknown vulnerabilities. No check deploys the stack or contacts a production database or LiveKit service.

Go module advisories can describe packages that an application does not import. The gate accepts only a fresh `govulncheck` package-level proof that the affected package is absent, with a mandatory text-mode gate and matching JSON/OpenVEX evidence. Source proofs bind the complete Linux package and test graph to exact source files, dependency versions and the original scan report. Runtime proofs inspect every inventoried Go binary, retain package symbols and module versions, and bind binary hashes and dependency PURLs to the exact image ID and OCI digest from a disposable loopback registry. An imported affected package, weaker proof, changed artifact, missing inventory or scanner error fails. Actual OpenPGP source and binary controls must fail to establish that affected code is detected. Complete inventories, raw findings and proofs are retained as CI artifacts for seven days; detected secret values are redacted while their findings continue to block.

Source and runtime secret scans retain all built-in rules and disable Trivy's Markdown allow rule. A clean Markdown control must pass, and a generated, unissued token in a README must fail with the expected token rule before scanning the project.

The web image runs the Next.js standalone server directly with Node 22. Build stages use Node 22 on Alpine 3.22; the production stage installs Alpine's `nodejs~22` package so the actual Node runtime and its dependencies have auditable OS package metadata. npm and Yarn remain in build stages because the running server does not use installation tools.

Caddy and Postgres are built from the Dockerfiles referenced by Compose and checked with disposable runtime smoke tests. Caddy retains the complete standard distribution and the existing mounted configuration. Postgres remains version 16 with its official entrypoint and privilege drop; its bundled gosu is rebuilt with patched Go dependencies. The API remains user 10001, and the existing service health checks remain in Compose.

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
backend/   Go: cmd/api, cmd/worker, internal/, миграции
web/       Next.js
deploy/    docker-compose, Caddyfile, .env.example, бэкапы Postgres (S1-7)
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
