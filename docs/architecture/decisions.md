# Журнал решений (ADR-lite)

> Формат: `ADR-NNN · дата · статус (accepted/superseded)` → Контекст →
> Решение → Последствия. Новая запись — при любом выборе, которого нет
> в доках, и при любом отступлении от них.

## ADR-001 · 2026-07-31 · accepted — Вариант C (гибрид)

**Контекст.** Hobby-MVP на 5 преподавателей / 50 студентов, один VPS,
бюджет единицы $/мес.
**Решение.** Ядро и модель данных — свои (Compose: Next.js + Go + Postgres);
тяжёлые подсистемы — managed/OSS: LiveKit Cloud, AssemblyAI, S3,
LibreTranslate, excalidraw-complete. Медиа никогда не проходит через VPS.
**Последствия.** Низкая стоимость и простота эксплуатации; зависимость от
внешних сервисов гасится порогами апгрейда ([06-budget.md](06-budget.md)).
Снапшот исходного документа: [00-source-variant-c.md](00-source-variant-c.md).

## ADR-002 · 2026-07-31 · accepted — api и worker: один Go-модуль, два бинаря

**Контекст.** В compose api и worker — отдельные сервисы; код у них общий
(модель, store, интеграции).
**Решение.** Один Go-модуль `backend/` с `cmd/api` и `cmd/worker`, общий
`internal/`, один Dockerfile с двумя build-target'ами.
**Последствия.** Нет дублирования домена и миграций; деплой двух контейнеров
из одного образа/сборки.

## ADR-003 · 2026-07-31 · accepted — Очереди в Postgres, Redis опционален

**Контекст.** Нужны фоновые джобы (календарь, ASR, сборщик, LLM); масштаб —
единицы задач в минуту.
**Решение.** Таблица `jobs` + `SELECT … FOR UPDATE SKIP LOCKED` в worker.
Redis добавляем только если появится реальная нужда (кэш/rate-limit).
**Последствия.** Минус один stateful-сервис; транзакционность джоб вместе
с данными.

## ADR-004 · 2026-07-31 · accepted — TLS-край: host-nginx; caddy стека — HTTP-only на loopback

**Контекст.** Доки закладывали caddy единой точкой входа с TLS Let's Encrypt
([02-services.md](02-services.md), S0-1). Фактический VPS уже обслуживает
соседние проекты: 80/443 заняты системным nginx, который терминирует TLS
(certbot) и проксирует каждый стек на его loopback-порт (образец —
`barenta.wondermr.com` → caddy стека на `127.0.0.1:8080`). Домен
`lang.wondermr.com` — за Cloudflare-прокси.
**Решение.** Вписаться в паттерн хоста: caddy стека — HTTP-only (`:80`,
`auto_https off`), опубликован только на `127.0.0.1:${HTTP_PORT}` (8090);
TLS и HSTS для `lang.wondermr.com` — на host-nginx (certbot, webroot
`/var/www/html`); vhost — `/etc/nginx/sites-available/lang.wondermr.com`.
Внутренняя маршрутизация без изменений: `/api/*` → api (префикс стрипается,
пути внутри api — как в [04-api.md](04-api.md)), остальное → web.
**Последствия.** Реальный IP клиента — в `X-Real-IP` от nginx (за Cloudflare —
адрес CF, см. комментарий в vhost barenta). Новый ключ `.env`: `HTTP_PORT`.
При переезде на чистый VPS достаточно опубликовать 80/443 и вернуть
`{$DOMAIN}` в Caddyfile.

## ADR-005 · 2026-07-31 · accepted — DDL-конвенции схемы Postgres

**Контекст.** Скетч в [03-data-model.md](03-data-model.md) задаёт таблицы и
колонки, но не фиксирует тип PK, тип времени и способ ограничения
перечислений. Масштаб — hobby (≈50 пользователей), Postgres 16.
**Решение.** Для всех таблиц: PK — `bigint GENERATED ALWAYS AS IDENTITY`
(кроме чистых связок с составным PK); время — `timestamptz`; перечисления
(`users.role`, `lessons.status`, `materials.kind`, `groups.level` —
CEFR `A1, A2, B1, B2, C1, C2`) — `text` + `CHECK`
(дешевле эволюция, чем `ENUM`); служебный `created_at timestamptz NOT NULL
DEFAULT now()` у сущностей. Колонки будущих этапов из скетча
(`lessons.transcript_id`, `recording_s3_key`, `materials.s3_key`) создаются
сразу nullable, но FK на ещё не существующие таблицы не ставится —
его добавит миграция соответствующего этапа.
**Последствия.** Единообразный DDL без сюрпризов при `ALTER`; identity
не переживёт merge двух баз (не наш случай); `CHECK`-перечисления меняются
одной миграцией без пересоздания типа.

## ADR-006 · 2026-07-31 · accepted — Auth: cookie-сессии в Postgres, bcrypt

**Контекст.** S0-3 требует выбрать механизм auth: cookie-сессии или JWT.
Масштаб — 55 пользователей, один VPS, один api-процесс; план этапа прямо
предостерегает от переусложнения auth.
**Решение.** Серверные сессии в Postgres: таблица `sessions` (в БД — только
`sha256`-дайджест токена, TTL 30 дней), cookie `sid` — HttpOnly, Secure,
SameSite=Lax, Path=/. Пароли — bcrypt: в Go `golang.org/x/crypto/bcrypt`,
в seed — `pgcrypto` `crypt(..., gen_salt('bf', 12))` (форматы совместимы);
пароль seed-пользователей задаётся ключом `.env` `SEED_PASSWORD` и в git
не попадает. JWT отклонён: ревокация и logout при сессиях тривиальны,
stateless-выгоды на одном процессе отсутствуют. Новые эндпоинты этапа 0:
`POST /auth/logout`, `GET /auth/me` (нужен web-каркасу S0-5) — добавлены
в [04-api.md](04-api.md).
**Последствия.** Каждый авторизованный запрос — один SELECT сессии
(на нашем масштабе незаметно); протухшие сессии чистятся лениво при
логине владельца, глобальная уборка — с появлением worker (S1-2).
Ключ `.env` `SESSION_SECRET`, заложенный планом на S0-1, удалён: при
серверных сессиях подпись не нужна. Первые Go-зависимости:
`jackc/pgx/v5`, `golang.org/x/crypto`.

<!-- Следующие ADR добавлять ниже по мере реализации. -->
