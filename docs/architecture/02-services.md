# Сервисы docker-compose

> Живёт в `deploy/docker-compose.yml`. Состав растёт по этапам:
> этап 0 — caddy, web, api, postgres; этап 1 — + worker;
> этап 3 — + libretranslate, + whiteboard (профиль compose, ADR-020); redis —
> опция при необходимости.

## Состав

| Сервис | Образ / стек | RAM (оценка) | Этап | Роль |
|---|---|---|---|---|
| caddy | caddy:2 | ~30 MB | 0 | TLS (Let's Encrypt), reverse proxy, единая точка входа |
| web | Next.js | 300–500 MB | 0 | UI: страницы уроков, плеер, словарь, практика, доска-обёртка |
| api | Go | 50–150 MB | 0 | REST/JSON API, auth (sessions/JWT), FSRS (go-fsrs), выдача presigned URL, LiveKit tokens, LLM-генерация текстов, «объясни» и LLM-дистракторы cloze синхронно (ADR-029, ADR-039, ADR-037; дистракторы тратят лимит «объясни») |
| postgres | postgres:16 | 300–500 MB | 0 | всё состояние + full-text search субтитров/текстов |
| worker | Go | 100–300 MB | 1 | очереди: календарь, ASR-джобы, VTT-рендер, разбор доски `whiteboard_terms` (ADR-033), уборка сессий `session_cleanup` (ADR-035), `recording_sweep` с остановкой Egress через `LIVEKIT_*` (ADR-031); бэкфилл `srs_cards` на каждом старте (ADR-036); в образе — разовые импорты словаря и Tatoeba (`dictimport`, `tatoebaimport`, ADR-019, ADR-028) |
| harvester | Go + yt-dlp (target `harvester`) | 50–150 MB (оценка, не замерено; потолок 512 MB, 1 CPU до замера S5-2) | 5 | сборщик субтитров: тот же worker с `WORKER_ROLE=harvester`, профиль compose `harvester` — включается явно; своя роль базы только с его таблицами, корень только на чтение ([ADR-022](decisions.md)) |
| libretranslate | libretranslate:v1.9.6 (digest) | 1–1.5 GB (замер ~380 MB) | 3 | перевод en↔ru; `LT_LOAD_ONLY=en,ru`, `LT_THREADS=1`; профиль `translate`, сеть `translate` (internal) — ADR-018 |
| whiteboard | excalidraw/excalidraw-room (профиль `whiteboard`) | ~15 MB | 3 | ретранслятор доски: пересылает зашифрованные сообщения участников комнаты со случайным именем (128 бит, выдаёт api только участникам урока); путь сокета — `/whiteboard/lesson-{id}/socket.io/`, его проверяет forward_auth; редактор — в web, сцена — в api/Postgres, снимки — в S3 (ADR-020) |
| redis | redis:7 | 50 MB | опц. | кэш переводов, rate-limit; очереди держим в PG (SKIP LOCKED) — redis не зависимость |

## Ресурсы VPS

- Итого ~2.5–3 GB RAM; 2 vCPU достаточно (пики CPU — только парсинг
  субтитров в worker).
- Комфортная VPS: **4 vCPU / 8 GB**; минимальная: 2 / 4.

## Примечания по контейнерам

- **api и worker** — один Go-модуль (`backend/`), два бинаря `cmd/api` и
  `cmd/worker`, один Dockerfile с двумя target'ами (см. `backend/README.md`,
  решение — [decisions.md](decisions.md)).
- **libretranslate** без `--load-only en,ru` тянет все языковые модели и
  съедает RAM — флаг обязателен; ограничить threads, чтобы не душить api.
  В compose (S3-3): `LT_THREADS` — это число процессов gunicorn, каждый
  со своей копией моделей, поэтому 1; потолки `cpus: 1.5`, `mem_limit: 2g`.
  Образ вне блокирующего скана — сервис в профиле `translate`, модели
  ставит разовый `libretranslate-models` (ADR-018, долг D-10).
- **whiteboard** заменил excalidraw-complete — проект закрыт автором
  ([ADR-020](decisions.md)). Состояния не хранит; снаружи — только
  `/whiteboard/lesson-{id}/socket.io/` через caddy после forward_auth
  в api. Образ не проходит блокирующий скан ADR-011, поэтому сервис
  в профиле и включается явно (`COMPOSE_PROFILES=whiteboard`,
  `WHITEBOARD_ENABLED=1`), долг D-11.
- Тома: данные postgres; конфиг caddy. Healthcheck'и — минимум на postgres
  и api.

Связано: [01-topology.md](01-topology.md) · план [stage-0](../plans/stage-0-skeleton.md)
