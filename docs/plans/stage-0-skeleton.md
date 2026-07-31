# Этап 0 — Скелет (оценка: 1–2 нед)

**Цель:** работающий стенд на VPS: compose + TLS, auth с ролями, CRUD уроков
и материалов, страница `/lesson/{id}` со state machine. После этапа платформа
уже полезна как «расписание и материалы», без видео.

Доки: [сервисы](../architecture/02-services.md) ·
[модель данных](../architecture/03-data-model.md) ·
[API](../architecture/04-api.md) ·
[жизненный цикл урока](../architecture/flows/lesson-lifecycle.md)

## Входные артефакты

| Артефакт | Откуда |
|---|---|
| VPS с Docker + домен | вне репозитория (есть) |
| Архитектура и модель данных | `docs/architecture/` |

## Задачи

### S0-1 Compose-скелет
`deploy/docker-compose.yml`: caddy, web, api, postgres (тома, healthcheck'и);
`deploy/Caddyfile` (TLS Let's Encrypt; `/` → web, `/api` → api);
`deploy/.env.example` со всеми ключами этапа; корневой `Makefile`
(up / down / logs / migrate / psql / seed).
**DoD:** `https://<домен>` отдаёт web через caddy.

### S0-2 БД v1 + миграции
golang-migrate в `backend/migrations/`; таблицы: `users`, `groups`,
`group_members`, `lessons`, `lesson_participants`, `materials`
(по [03-data-model.md](../architecture/03-data-model.md)).
Seed: преподаватель + тестовая группа + 2–3 студента.
**DoD:** `make migrate` идемпотентен; `make seed` наполняет стенд.

### S0-3 Auth и роли
Механизм — cookie-сессии или JWT: выбрать, зафиксировать ADR.
`POST /auth/login`, logout, middleware ролей teacher|student.
**DoD:** студент получает 403 на создание урока.

### S0-4 CRUD ядра
Группы и участники (teacher); уроки: `POST /lessons`, `GET /lessons/{id}`,
перенос/отмена; материалы/домашка: `POST /lessons/{id}/materials|homework`
(kind, title, body_md; **файлы в S3 — этап 1**, пока только markdown — долг D-4).
**DoD:** сценарий «создать урок, приложить домашку» проходит через API.

### S0-5 Web-каркас и state machine `/lesson/{id}`
Next.js: логин, список уроков по роли, страница урока. Состояния по
времени + статусу: `scheduled` (материалы, домашка) → `live` (заглушка
«Войти в класс» — оживёт в S1-4) → `processing` → `done` (заглушки
плеера/транскрипта — оживут в S1-6/S2-4).
**DoD:** одна ссылка ведёт себя по-разному до/во время/после урока.

### S0-6 Деплой на VPS
compose up на VPS, домен, том PG, ротация docker-логов.
**DoD:** стенд доступен извне по HTTPS; перезапуск не теряет данные.

## Выходные артефакты

| Артефакт | Потребитель |
|---|---|
| `deploy/docker-compose.yml`, `Caddyfile`, `.env.example`, `Makefile` | все этапы (сервисы и ключи добавляются сюда) |
| Таблицы ядра + механизм миграций и seed | все этапы |
| Auth-middleware и роли | все API следующих этапов |
| `/lesson/{id}` state machine с заглушками | S1-4 (кнопка «Войти»), S1-6 (плеер), S2-4 (транскрипт), S3-8 (словарь урока) |
| `users.email` участников групп | S1-2 (attendees календаря) |

## Definition of Done этапа

- [ ] S0-1…S0-6 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: логин преподавателя → урок → домашка → студент видит страницу
- [ ] Раздел «Факт» заполнен

## Факт (заполняется по ходу реализации)

### S0-1 Compose-скелет (2026-07-31)

- `deploy/docker-compose.yml` — проект `lingua`: caddy (`caddy:2.10`),
  web (Next.js standalone), api (Go), postgres (`postgres:16-alpine`);
  healthcheck'и на всех, ротация json-логов (10m×3, anchor `x-logging`),
  тома `pg_data`, `caddy_data`, `caddy_config`; профиль `tools`: сервис
  `migrate` (`migrate/migrate:v4.17.1`) — заработает с файлами S0-2.
- `deploy/Caddyfile` — HTTP-only `:80` (ADR-004): `handle_path /api/*` →
  `api:8080` (префикс стрипается — пути внутри api без `/api`, как в
  [04-api.md](../architecture/04-api.md)), остальное → `web:3000`.
- `deploy/.env.example` — ключи этапа: `DOMAIN`, `HTTP_PORT`, `POSTGRES_DB`,
  `POSTGRES_USER`, `POSTGRES_PASSWORD`, `SESSION_SECRET`. Реальный
  `deploy/.env` создан на VPS (chmod 600, вне git).
- Корневой `Makefile`: `up / down / build / logs / ps / migrate / psql /
  seed` (`migrate` и `seed` — честные заглушки до S0-2).
- `backend/`: `go.mod` (`github.com/IEZhu/class/backend`, go 1.24),
  `cmd/api/main.go` (stdlib, `GET /healthz`), `Dockerfile` (multi-stage,
  target `api`; target `worker` появится в S1-2), `migrations/.gitkeep`.
- `web/`: Next.js 15.5.22 / React 19.2.8 / TypeScript 5 (`package-lock.json`
  закоммичен), `output: "standalone"`, заглушки `app/layout.tsx` +
  `app/page.tsx`, `Dockerfile` (node:22-alpine, non-root).
- Проверено: `https://lang.wondermr.com/` → 200 (web через caddy);
  `https://lang.wondermr.com/api/healthz` → `{"status":"ok"}`.
- Известное: `npm audit` — postcss ≤8.5.17 (high, build-time) как
  транзитивная зависимость всех актуальных next; фикса апстрим пока нет.

### S0-2 БД v1 + миграции (2026-07-31)

- `backend/migrations/0001_core.up.sql` / `.down.sql` — таблицы `users`,
  `groups`, `group_members`, `lessons`, `lesson_participants`, `materials`
  по скетчу [03-data-model.md](../architecture/03-data-model.md);
  DDL-конвенции — ADR-005 (identity PK, timestamptz, text+CHECK, created_at).
  Поля будущих этапов в `lessons` (`gcal_event_id`, `livekit_room`,
  `recording_s3_key`, `transcript_id`) и `materials.s3_key` — nullable,
  FK транскрипта добавит S2-2.
- Индексы: `lessons(group_id, starts_at)`, `lessons(teacher_id, starts_at)`,
  `group_members(user_id)`, `lesson_participants(user_id)`,
  `materials(lesson_id)`; `users.email` — UNIQUE.
- `backend/migrations/seed.sql` — идемпотентный: Test Teacher + Test Group
  (A2) + 3 студента + урок через 2 дня (`scheduled`) + домашка (body_md).
- Проверено на стенде: `make migrate` ×2 (второй — `no change`),
  `make seed` ×2 (нулевые вставки), цикл `migrate down 1` → `up` → `seed`
  восстанавливает стенд. Механизм — сервис `migrate`
  (`migrate/migrate:v4.17.1`) из compose (S0-1).

### S0-3 Auth и роли (2026-07-31)

- Механизм — серверные cookie-сессии в Postgres, ADR-006 (JWT отклонён):
  cookie `sid` HttpOnly/Secure/Lax, TTL 30 дней; в БД — sha256-дайджест
  токена. Пароли — bcrypt (`golang.org/x/crypto/bcrypt` ↔ `pgcrypto`).
- `backend/migrations/0002_auth.up.sql`/`.down.sql`: `users.password_hash`,
  таблица `sessions(user_id, token_hash UNIQUE, expires_at)` + pgcrypto.
- Эндпоинты (пути внутри api, снаружи с префиксом `/api`):
  `POST /auth/login` (200 + Set-Cookie / 401), `POST /auth/logout` (204),
  `GET /auth/me` (200/401); stub `POST /lessons` под `requireRole("teacher")`
  — студент 403, teacher 501 до S0-4.
- Go-код: `internal/store` (pgx: UserByEmail, сессии CRUD, ленивая уборка
  протухших) и `internal/httpapi` (роуты Go 1.22+, middleware `requireUser`/
  `requireRole`, ошибки JSON `{error, code}`). Каталог назван `httpapi`,
  а не `http` из README backend — чтобы не коллизировать с `net/http`.
- Зависимости: `jackc/pgx/v5 v5.10.0`, `golang.org/x/crypto v0.54.0`;
  go 1.25 (требование x/crypto), образ сборки `golang:1.25-alpine`.
- Seed задаёт bcrypt-пароль всем `*@lingua.local` из ключа `.env`
  `SEED_PASSWORD` (новый, опциональный; прокинут в контейнер postgres).
- Проверено на стенде (10 сценариев curl): login teacher/student 200,
  `me` 200, teacher `POST /lessons` 501, **студент 403 (DoD)**, неверный
  пароль 401, гость 401, logout 204 → `me` 401; внешний контур через
  Cloudflare — login 200.

### S0-6 Деплой на VPS (2026-07-31)

- VPS: реквизиты хоста — в операционном контуре вне git (репозиторий
  публичный); стек из `/opt/Class` (`make up`). Домен `lang.wondermr.com` —
  за Cloudflare-прокси (SSL Full).
- TLS-край — host-nginx (ADR-004): vhost
  `/etc/nginx/sites-available/lang.wondermr.com` (enabled) → проксирует на
  `127.0.0.1:8090` (caddy стека, `HTTP_PORT`); сертификат Let's Encrypt
  (certbot, webroot `/var/www/html`), авто-renewal настроен; HSTS на nginx.
- Ротация docker-логов — в compose (`x-logging`); том PG — `pg_data`.
- Проверено: `docker compose down && up` сохраняет данные Postgres;
  внешний HTTPS после перезапуска — 200.

## Риски этапа

Переусложнение auth (достаточно cookie-сессий); преждевременный Redis
(очереди будут в PG — ADR-003); затягивание UI-полировки — каркас важнее вида.
