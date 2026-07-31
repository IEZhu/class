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

<!-- Следующие ADR добавлять ниже по мере реализации. -->
