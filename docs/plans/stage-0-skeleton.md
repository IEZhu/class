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

- [ ] S0-1…S0-6 закрыты в [INDEX](../tasks/INDEX.md)
- [ ] Сквозной сценарий: логин преподавателя → урок → домашка → студент видит страницу
- [ ] Раздел «Факт» заполнен

## Факт (заполняется по ходу реализации)

_пока пусто_

## Риски этапа

Переусложнение auth (достаточно cookie-сессий); преждевременный Redis
(очереди будут в PG — ADR-003); затягивание UI-полировки — каркас важнее вида.
