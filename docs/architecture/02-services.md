# Сервисы docker-compose

> Живёт в `deploy/docker-compose.yml`. Состав растёт по этапам:
> этап 0 — caddy, web, api, postgres; этап 1 — + worker;
> этап 3 — + libretranslate, + excalidraw; redis — опция при необходимости.

## Состав

| Сервис | Образ / стек | RAM (оценка) | Этап | Роль |
|---|---|---|---|---|
| caddy | caddy:2 | ~30 MB | 0 | TLS (Let's Encrypt), reverse proxy, единая точка входа |
| web | Next.js | 300–500 MB | 0 | UI: страницы уроков, плеер, словарь, практика, доска-обёртка |
| api | Go | 50–150 MB | 0 | REST/JSON API, auth (sessions/JWT), FSRS (go-fsrs), выдача presigned URL, LiveKit tokens |
| postgres | postgres:16 | 300–500 MB | 0 | всё состояние + full-text search субтитров/текстов |
| worker | Go | 100–300 MB | 1 | очереди: календарь, ASR-джобы, сборщик субтитров, импорт Tatoeba, LLM-генерация, VTT-рендер |
| libretranslate | libretranslate:v1.9.6 (digest) | 1–1.5 GB (замер ~380 MB) | 3 | перевод en↔ru; `LT_LOAD_ONLY=en,ru`, `LT_THREADS=1`; профиль `translate`, сеть `translate` (internal) — ADR-018 |
| excalidraw | PatWie/excalidraw-complete | 50–100 MB | 3 | доска: фронт + storage + realtime в одном Go-бинаре; сцены привязаны к lesson_id |
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
- **excalidraw-complete** держит realtime-сессии сам; наша интеграция —
  URL `?room=lesson-{id}` + бэкап JSON-сцены в S3 (этап 3).
- Тома: данные postgres; конфиг caddy. Healthcheck'и — минимум на postgres
  и api.

Связано: [01-topology.md](01-topology.md) · план [stage-0](../plans/stage-0-skeleton.md)
