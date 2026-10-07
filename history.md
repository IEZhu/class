---
repo: Class
created: 2026-07-31T12:05:36+00:00
format_version: 1
---

## 2026-07-31T12:05:36+00:00 | 211aa28daba0
**Intent:** Скаффолдинг документации и планов реализации проекта Lingua Class
**Action:** Agent: tech_writer — создано 33 файла, initial commit 8ce0d6b на master
**Outcome:** Создан каркас документации в /opt/Class: docs/architecture (снапшот исходника + топология, сервисы, модель данных с привязкой таблиц к этапам, API, S3, смета, риски, ADR-журнал с 3 решениями, 4 потока, 5 интеграций), docs/plans (00-flow конвейер артефактов + 6 планов этапов со входными/выходными артефактами, DoD и разделом «Факт»), docs/tasks/INDEX.md (39 задач S0-1..S5-8 с зависимостями + 4 долга D-1..D-4), CLAUDE.md (роль ведущего инженера + протокол flow + инварианты), каркасные README для backend/web/deploy, README и .gitignore. Initial commit 8ce0d6b (33 файла, 1845 строк). Следующий шаг: этап 0, задача S0-1.
**Files:** CLAUDE.md, README.md, docs/00-overview.md, docs/architecture/00-source-variant-c.md, docs/plans/00-flow.md, docs/tasks/INDEX.md, backend/README.md, web/README.md, deploy/README.md
**Tags:** #docs #scaffolding #planning #lingua-class


## 2026-07-31T12:10:33+00:00 | 00c6c39cc024
**Intent:** Публикация репозитория документации на GitHub
**Action:** Agent: tech_writer — git remote add origin, переименование master→main, push -u origin main (коммит 8ce0d6b), верификация через gh
**Outcome:** Remote origin=https://github.com/IEZhu/class.git подключён; локальная ветка переименована master→main и запушена (8ce0d6b, 33 файла). GitHub подтверждает: default branch main, репозиторий PUBLIC. README и вся документация доступны по https://github.com/IEZhu/class. Пуш выполнен под аккаунтом WonderMr (авторизация gh). history.md остался нетрекнутым.
**Tags:** #git #github #push #lingua-class


## 2026-07-31T12:16:23+00:00 | 2c2a01e0f83e
**Intent:** Навигационные индексы папок документации для просмотра на GitHub
**Action:** Agent: tech_writer — README-индексы уровней, переименования INDEX.md→README.md и 00-overview.md→README.md, sed по ссылкам, commit 2c21091 + push
**Outcome:** Каждая папка доков на GitHub теперь открывается индексом со ссылками на карточки уровня в начале (использован README.md — GitHub автооткрывает его, index.md не рендерится в браузере репозитория). Созданы: docs/README.md (бывший 00-overview), architecture/README.md, flows/README.md, integrations/README.md, plans/README.md; tasks/INDEX.md переименован в tasks/README.md; 18 ссылок в 10 файлах обновлены. Коммит 2c21091 запушен в main IEZhu/class. history.md попал в коммит (git add -A) — решение оставить/исключить за пользователем.
**Files:** docs/README.md, docs/architecture/README.md, docs/architecture/flows/README.md, docs/architecture/integrations/README.md, docs/plans/README.md, docs/tasks/README.md, CLAUDE.md, README.md
**Tags:** #docs #navigation #github #lingua-class


## 2026-07-31T12:36:57+00:00 | 020847d78961
**Intent:** Реализация этапа 0 (S0-1 compose-скелет + S0-6 деплой) проекта Lingua Class
**Action:** Agent: devops_engineer — создан деплой-скелет, стенд поднят и опубликован на lang.wondermr.com
**Outcome:** Закрыты S0-1 и S0-6 этапа 0: compose-скелет (caddy 2.10, Next.js 15 standalone, Go api, postgres 16) поднят на VPS, https://lang.wondermr.com работает извне (web 200, /api/healthz ok), перезапуск не теряет данные. ADR-004: TLS терминирует host-nginx (80/443 заняты соседями), caddy стека — HTTP-only на 127.0.0.1:8090. Коммиты f02fc75, 910e509. Следующая задача — S0-2 (БД v1 + миграции).
**Files:** deploy/docker-compose.yml, deploy/Caddyfile, deploy/.env.example, Makefile, backend/cmd/api/main.go, backend/Dockerfile, web/Dockerfile, docs/architecture/decisions.md, docs/plans/stage-0-skeleton.md, docs/tasks/README.md
**Tags:** #stage-0 #S0-1 #S0-6 #deploy #docker-compose #caddy #nginx #certbot


## 2026-07-31T12:49:02+00:00 | 9a81f84a9813
**Intent:** Публикация правок этапа 0 через PR и ожидание ревью
**Action:** Agent: devops_engineer — push ветки, gh pr create, запрос Copilot-ревью, монитор PR
**Outcome:** Создан PR #1 https://github.com/IEZhu/class/pull/1 (ветка feat/s0-1-compose-skeleton, коммиты f02fc75+910e509 — S0-1/S0-6). Ревьюеры: CodeRabbit уже подхватил PR (авто-summary в описании), Copilot review запрошен через REST API. Поставлен persistent-монитор на новые ревью/комментарии PR — отчитаюсь по мере их появления.
**Tags:** #stage-0 #pr #github #review #copilot #coderabbit


## 2026-07-31T12:58:18+00:00 | 697649d52124
**Intent:** Отработка замечаний ревьюеров PR #1
**Action:** Agent: devops_engineer — фиксы по ревью, push f269a5c, ответы в 6 тредов
**Outcome:** Все 6 замечаний обработаны, фиксы в f269a5c запушены в PR-ветку, в каждый тред дан ответ. Исправлено: check-env guard в Makefile; migrate/seed через if/else (ошибки не глотаются); .env* в .dockerignore обоих сервисов; удалён нерабочий lint-скрипт; из публичных доков убраны hostname/IP origin. Рекомендация CodeRabbit об ограничении ingress CF-диапазонами передана пользователю (общий nginx хоста). Ждём инкрементального ревью CodeRabbit.
**Files:** Makefile, web/.dockerignore, backend/.dockerignore, web/package.json, docs/plans/stage-0-skeleton.md
**Tags:** #stage-0 #pr-1 #review-fixes #coderabbit #copilot


## 2026-07-31T13:07:17+00:00 | 75ef81daac49
**Intent:** Отработка инкрементального ревью PR #1
**Action:** Agent: devops_engineer — фиксы Makefile, push fc42ef1, ответы в 3 треда
**Outcome:** Все 3 замечания валидны и починены в fc42ef1: (1) check-env — DOMAIN в REQUIRED_ENV, отклоняются whitespace/кавычки-пустышки; (2) migrate: check-env — прямой make migrate не инициализирует pg_data с плохим .env; (3) проба миграций по *.up.sql — seed.sql не триггерит migrate up. Негативные сценарии прогнаны, ответы в треды отправлены.
**Files:** Makefile
**Tags:** #stage-0 #pr-1 #review-fixes #coderabbit


## 2026-07-31T13:08:31+00:00 | d3146e521fae
**Intent:** Завершение цикла ревью PR #1
**Action:** Agent: devops_engineer — подтверждения ревьюеров получены, треды закрыты, PR готов к мержу
**Outcome:** Ревью PR #1 полностью отработано: 9 замечаний CodeRabbit + 1 Copilot, все исправлены (f269a5c, a172030, fc42ef1) и подтверждены ревьюером как addressed; последний Copilot-тред закрыт вручную. PR MERGEABLE/CLEAN, 5 коммитов. Мерж — за пользователем.
**Tags:** #stage-0 #pr-1 #review-complete


## 2026-07-31T13:24:47+00:00 | 7308a436517c
**Intent:** Продолжение этапа 0: задача S0-2 (БД v1 + миграции)
**Action:** Agent: database_admin — миграции ядра, seed, проверки на стенде, PR #2
**Outcome:** main синхронизирован (89efc71). S0-2 выполнена: ADR-005 (DDL-конвенции), миграция 0001_core (6 таблиц ядра с индексами), идемпотентный seed.sql; на стенде проверены идемпотентность migrate/seed и цикл down/up. Статус S0-2 ☑ (3/6), фокус → S0-3. PR #2 открыт (https://github.com/IEZhu/class/pull/2), Copilot запрошен, монитор ревью стоит.
**Files:** backend/migrations/0001_core.up.sql, backend/migrations/0001_core.down.sql, backend/migrations/seed.sql, docs/architecture/decisions.md, docs/architecture/03-data-model.md, docs/plans/stage-0-skeleton.md, docs/tasks/README.md
**Tags:** #stage-0 #S0-2 #db #migrations #seed #pr-2


## 2026-07-31T13:30:21+00:00 | 1082090696c5
**Intent:** Отработка ревью PR #2
**Action:** Agent: database_admin — фиксы по ревью, подтверждения, треды закрыты
**Outcome:** Все 6 замечаний PR #2 исправлены в 7f74e33 (CHECK на groups.level A1..C2 + ADR-005 дополнен; seed заскоуплен на Test Group, явные email). Стенд перекатан down/up, CHECK и идемпотентность проверены. CodeRabbit подтвердил все 3 своих треда, 3 Copilot-треда закрыты вручную. PR #2 MERGEABLE, ждёт финализации check'а CodeRabbit и мержа пользователем.
**Files:** backend/migrations/0001_core.up.sql, backend/migrations/seed.sql, docs/architecture/decisions.md
**Tags:** #stage-0 #pr-2 #review-complete


## 2026-07-31T13:55:14+00:00 | 06854da53c12
**Intent:** Вопрос о состоянии стенда
**Action:** Agent: devops_engineer — подтверждение ожидаемого поведения заглушки
**Outcome:** Подтвердил: это намеренная заглушка главной страницы из S0-1, реальный UI появится после S0-3..S0-5. Статус: S0-1/S0-6 готовы, S0-2 в PR #2 (OPEN, ждёт мержа пользователя), дальше S0-3.
**Tags:** #stage-0 #status


## 2026-07-31T14:09:59+00:00 | 6fc40a9702f0
**Intent:** Реализация S0-3 (auth и роли)
**Action:** Agent: software_engineer — auth-слой Go, миграция, seed, DoD-прогон, PR #3
**Outcome:** S0-3 выполнена: ADR-006 (cookie-сессии в PG, bcrypt), миграция 0002_auth (password_hash, sessions с sha256-дайджестом), эндпоинты /auth/login|logout|me, middleware requireUser/requireRole, stub POST /lessons (студент 403 — DoD, teacher 501). Deps pgx v5.10.0, x/crypto v0.54.0, go 1.25. Seed-пароли из SEED_PASSWORD. 10 curl-сценариев прошли, внешний login 200. Статус ☑ (4/6), PR #3 открыт, Copilot запрошен, монитор стоит.
**Files:** backend/internal/httpapi/, backend/internal/store/store.go, backend/migrations/0002_auth.up.sql, backend/migrations/seed.sql, docs/architecture/decisions.md, docs/architecture/04-api.md
**Tags:** #stage-0 #S0-3 #auth #pr-3


## 2026-07-31T14:23:50+00:00 | 2c7d40bae5d7
**Intent:** Завершение цикла ревью PR #3
**Action:** Agent: software_engineer — фиксы подтверждены, треды закрыты, PR готов к мержу
**Outcome:** Все замечания PR #3 закрыты (a68a66c): тайминг-канал логина (dummy bcrypt, 253vs254мс), seed-allowlist, пустой дефолт SEED_PASSWORD, таймауты server+statement_timeout, сброс битого cookie, удаление SESSION_SECRET, seed:migrate, MaxBytesReader, пин go 1.25.12. Отклонена глобальная чистка сессий (by design до worker S1-2). 0 unresolved-тредов, PR MERGEABLE (UNSTABLE — только pending-check CodeRabbit, самозакроется). Мерж за пользователем.
**Tags:** #stage-0 #pr-3 #review-complete


## 2026-07-31T14:40:54+00:00 | f08a0581da5a
**Intent:** Реализация S0-4 (CRUD ядра)
**Action:** Agent: software_engineer — CRUD-слой store+httpapi, DoD-прогон, PR #4
**Outcome:** PR #3 смёржен, main синхронизирован. S0-4 выполнена: CRUD групп/уроков/материалов с ролями, снапшот участников при создании урока (транзакция), перенос/отмена только для scheduled (409), маппинг SQLSTATE→HTTP. Сквозной DoD прогнан: урок с домашкой виден студенту, все отказы честные. Статус ☑ (5/6), фокус S0-5. PR #4 открыт, Copilot запрошен, монитор стоит.
**Files:** backend/internal/store/groups.go, backend/internal/store/lessons.go, backend/internal/store/materials.go, backend/internal/httpapi/groups.go, backend/internal/httpapi/lessons.go, backend/internal/httpapi/materials.go, docs/architecture/04-api.md
**Tags:** #stage-0 #S0-4 #crud #pr-4


## 2026-07-31T19:20:37+00:00 | 288019eb2693
**Intent:** Завершение цикла ревью PR #4
**Action:** Agent: software_engineer — фиксы подтверждены, треды закрыты
**Outcome:** Все 6 замечаний PR #4 исправлены в f00d992 и подтверждены: доступ к уроку по контракту (без послабления любому teacher), LessonTeacherID для прав на материалы, decodeBody строго одно JSON-значение, 413 для oversized-тел, префикс /api задокументирован. 0 unresolved, MERGEABLE. Мерж за пользователем, дальше S0-5.
**Tags:** #stage-0 #pr-4 #review-complete


## 2026-08-02T10:32:26+00:00 | 2bd7767dee60
**Intent:** Реализация S0-5 и закрытие этапа 0
**Action:** Agent: software_engineer — web-каркас Next.js, прогон фаз, PR #5
**Outcome:** S0-5 выполнена, этап 0 закрыт целиком (6/6, DoD этапа прогнан). Web-каркас: /login, /lessons (SSR по роли), /lesson/[id] со state machine scheduled/live/ended/processing/done (статус+время), заглушки LiveKit/плеера/транскрипта, apiFetch с cookie-forwarding через INTERNAL_API_URL. Все фазы проверены SSR-прогоном, студент видит домашку, внешний контур 200. PR #5 открыт, Copilot запрошен, монитор стоит. Фокус индекса → S1-1.
**Files:** web/lib/api.ts, web/lib/lesson-phase.ts, web/app/login/page.tsx, web/app/lessons/page.tsx, web/app/lesson/[id]/page.tsx, web/app/user-bar.tsx, deploy/docker-compose.yml, docs/plans/stage-0-skeleton.md, docs/tasks/README.md
**Tags:** #stage-0 #S0-5 #web #pr-5 #stage-complete


## 2026-08-02T10:43:49+00:00 | a389b1aaf49a
**Intent:** Завершение цикла ревью PR #5 (финал этапа 0)
**Action:** Agent: software_engineer — фиксы подтверждены, треды закрыты, PR готов
**Outcome:** PR #5 закрыт по ревью: 9 пунктов исправлены в de8306a и подтверждены (таймаут apiFetch, sid-only cookie, group_name required, logout finally, тип роли, nitpick'и), 1 отклонён обоснованно (params: Promise — корректно для Next 15, билд с типчеком зелёный). 0 unresolved, MERGEABLE. Мерж этапа 0 за пользователем.
**Tags:** #stage-0 #pr-5 #review-complete


## 2026-08-02T13:30:12+00:00 | 740850d14794
**Intent:** Закрытие замечаний ревью PR #5 (S0-5 web-каркас) и мерж этапа 0
**Action:** Agent: software_engineer — аудит 6 тредов ревью PR #5, доработка невыполненного nitpick (6034c0e), мерж PR, прогон стенда
**Outcome:** Все 9 замечаний PR #5 (Copilot ×3, CodeRabbit ×3 actionable + ×4 nitpick) проверены: 8 закрыты в de8306a, 1 отклонён обоснованно (params: Promise — норма Next 15.5.22, подтверждено зелёным next build). Единственный невыполненный nitpick доделан в 6034c0e: web получил depends_on api/service_healthy, *.tsbuildinfo в .gitignore, раздел «Факт» S0-5 дополнен (таймаут 5s, только sid-cookie, depends_on). Проверки: tsc --noEmit чисто, next build 15.5.22 зелёный (5 роутов), docker compose config валиден. CodeRabbit перепрогнал ревью на 6034c0e — новых замечаний нет, все 6 тредов resolved. PR #5 смержен merge-коммитом 00a30bf (ветка сохранена по конвенции этапа 0), local main обновлён. Стенд: make up применил новый порядок старта (postgres → api healthy → web → caddy), внешний контур зелёный — /login 200, /api/healthz 200, /lessons → 307 /login, / → 307 /lessons; в работающем контейнере подтверждены AbortController и fallback «Группа #». Этап 0 закрыт 6/6, следующий фокус — S1-1 (Google OAuth2). Открыто advisory CodeRabbit «Docstring Coverage 28.57% < 80%» — не блокирует, JSDoc ради метрики не добавлялся.</outcome> <files>["deploy/docker-compose.yml", ".gitignore", "docs/plans/stage-0-skeleton.md"]</files> <tags>["stage-0", "S0-5", "pr-5", "review-fixes", "coderabbit", "copilot", "merge", "nextjs", "docker-compose"]</tags> </invoke>


## 2026-08-02T13:46:46+00:00 | 040bfb51632a
**Intent:** Вход на стенд Lingua Class под seed-учёткой
**Action:** Agent: software_engineer — проверка seed-пользователей в БД, наличия SEED_PASSWORD, сквозной curl-логин
**Outcome:** Учётки стенда: teacher@lingua.local (преподаватель), student1..3@lingua.local (студенты) — у всех четырёх password_hash заполнен. Пароль общий — значение SEED_PASSWORD из /opt/Class/deploy/.env (задан, 12 символов; в чат не выводился — репозиторий публичный). Сквозная проверка: POST /api/auth/login → 200 + cookie sid, GET /lessons с cookie → 200, HTML содержит «Test Teacher · преподаватель», «Test Group». Хэши совместимы: pgcrypto gen_salt('bf',12) в seed.sql и bcryptCost=12 в backend/internal/httpapi/auth.go. Текущее состояние данных: урок #1 Test Group 13:00–14:00 UTC при status=scheduled → фаза live («Войти в класс» disabled до S1-4), после 14:00 UTC станет ended; урок #3 B1 Evening на 4 августа → scheduled. Сброс пароля (make seed не перезапишет — UPDATE идёт с AND password_hash IS NULL): через make psql UPDATE users SET password_hash = crypt('новый', gen_salt('bf',12)).</outcome> <files>["backend/migrations/seed.sql", "backend/internal/httpapi/auth.go", "deploy/.env"]</files> <tags>["stand", "auth", "seed", "login", "lingua-class", "S0-3"]</tags> </invoke>


## 2026-08-02T14:52:54+00:00 | ac5b3ae2cbbd
**Intent:** Получение пароля seed-учёток для входа на стенд
**Action:** Agent: software_engineer — подтверждение пары логин/пароль, заметка о ротации
**Outcome:** Пользователь сам вывел SEED_PASSWORD из deploy/.env (значение намеренно не фиксируется в history.md — репозиторий публичный). Вход: https://lang.wondermr.com/login, teacher@lingua.local либо student1..3@lingua.local, пароль общий. Работоспособность пары подтверждена ранее в этой же сессии curl'ом (200 + cookie sid, /lessons рендерит кабинет). Отмечено: пароль теперь присутствует в транскрипте сессии; ротация — UPDATE users SET password_hash = crypt('новый', gen_salt('bf',12)) через make psql плюс правка ключа в deploy/.env.</outcome> <files>["deploy/.env"]</files> <tags>["stand", "auth", "seed", "login", "secrets"]</tags> </invoke>


## 2026-08-02T16:15:00+00:00 | 865f304e905e
**Intent:** Управление учётными записями и учениками в кабинете; сверка с планами этапов
**Action:** Agent: software_engineer — аудит планов, ADR-007, миграция роли admin, api /users, web-кабинеты, e2e на стенде, PR #6
**Outcome:** Обнаружен пробел проектирования: ни один план этапов 0–5 и 04-api.md не содержали эндпоинта заведения человека — POST /groups/{id}/members добавляет только существующего (404 иначе), пользователи появлялись лишь из seed.sql. По решению пользователя добавлена третья роль admin. ADR-007: admin — всё, teacher — только студенты своих групп и заведение только студентов, любой — своё имя и пароль; стартовый пароль вместо инвайт-ссылок (почтового сервиса в стеке нет), саморегистрация отклонена. Реализовано: миграция 0003_roles (CHECK role → admin|teacher|student, откат понижает админов), seed заводит admin@lingua.local; store/users.go и httpapi/users.go; requireRole → requireAnyRole; группы с teacher на staff (admin+teacher); эндпоинты GET/POST /users, PATCH /users/{id}, POST /users/{id}/password, PATCH /auth/me, POST /auth/password, DELETE /groups/{id}/members/{user_id}; смена/сброс пароля гасят сессии владельца одной транзакцией, при смене себе выдаётся свежий cookie. Web: /admin (позвать человека, люди с группами, сброс пароля, смена роли, группы и состав), /account (имя и пароль), общий web/app/forms.tsx, web/lib/roles.ts отдельно от lib/api.ts (тот тянет next/headers — падение поймано next build). Попутно исправлен контракт: omitempty выбрасывал members у пустой группы (SSR /admin падал в 500) и group_name на create/reschedule — оба поля теперь всегда в ответе. Проверено на стенде: миграция накатана, 24 e2e-проверки через lang.wondermr.com/api зелёные, тестовые данные удалены. Порядок этапа 1 переставлен под цель «провести живой урок»: S1-8/S1-9 → S1-4 → S1-3 → S1-5 → S1-2 → S2-1…S2-3, S1-1 (календарь) уезжает за MVP. Открыт долг D-5 (удаление/архивация учёток). PR #6: https://github.com/IEZhu/class/pull/6</outcome> <files>["docs/architecture/decisions.md", "docs/architecture/04-api.md", "docs/architecture/03-data-model.md", "docs/tasks/README.md", "docs/plans/stage-1-calendar-livekit.md", "backend/migrations/0003_roles.up.sql", "backend/internal/store/users.go", "backend/internal/httpapi/users.go", "backend/internal/httpapi/api.go", "web/app/admin/admin-panel.tsx", "web/app/account/account-forms.tsx", "web/lib/roles.ts"]</files> <tags>["stage-1", "S1-8", "S1-9", "ADR-007", "admin-role", "accounts", "nextjs", "go", "pr-6"]</tags> </invoke>


## 2026-08-02T16:32:27+00:00 | 417f4ec4b985
**Intent:** Отработка замечаний ревью PR #6 (учётки, роль admin)
**Action:** Agent: code_reviewer — разбор 7 замечаний Copilot и CodeRabbit, правки, регрессионные проверки на стенде, коммит 01aa9df, ответы в треды
**Outcome:** Все 7 замечаний признаны валидными и отработаны в 01aa9df. Copilot: ListUsersOfTeacherGroups и TeacherManagesUser теперь фильтруют по role=student (staff-учётка в group_members больше не открывает преподавателю чужой профиль и пароль), подзапрос «группы преподавателя» вынесен в константу teacherGroupIDs; aria-label вместо title на кнопке удаления участника; Group.members[].role типизирован как Role. CodeRabbit: критическое замечание про авторизацию состава групп закрыто иначе, чем предложено — проверка доступа к запрошенной группе не помогала, поскольку у groups нет владельца и преподаватель мог добавить себя в любую группу; правка состава (POST /groups, POST/DELETE members) переведена на роль admin, чтение групп осталось у staff; в web формы правки групп показываются только админу. Плюс формулировка auth в 04-api.md заменена на серверные cookie-сессии (ADR-006), добавлено window.confirm перед удалением участника. ADR-007 дополнен, 04-api.md и план этапа 1 обновлены. Проверки на стенде: 13 новых регрессионных проверок зелёные (teacher → 403 на создание группы, добавление и удаление участника; админ в группе преподавателя не виден в GET /users и защищён от PATCH и сброса пароля; свой студент по-прежнему правится; SSR /admin без форм групп у teacher, с формами и aria-label у admin) плюс прежние 24 проверки на чистых данных. go build/vet и next build зелёные, тестовые данные со стенда удалены.</outcome> <files>["backend/internal/store/users.go", "backend/internal/httpapi/api.go", "web/app/admin/admin-panel.tsx", "web/lib/api.ts", "docs/architecture/decisions.md", "docs/architecture/04-api.md", "docs/plans/stage-1-calendar-livekit.md"]</files> <tags>["pr-6", "review-fixes", "coderabbit", "copilot", "authorization", "least-privilege", "S1-8"]</tags> </invoke>


## 2026-08-02T17:05:02+00:00 | 3578d041b0af
**Intent:** Одноразовые ссылки-приглашения и подготовка к видео с транскрибацией
**Action:** Agent: software_engineer — ADR-008, миграция invites, api и web приглашений, e2e на стенде, PR #7
**Outcome:** S1-10 закрыт. ADR-008 заменяет пункт ADR-007 об отклонении инвайт-ссылок: канал передачи тот же, но ссылка одноразовая и не содержит пароля — его задаёт сам приглашённый. Таблица invites (миграция 0004_invites) хранит только sha256 токена, TTL 7 дней; одноразовость обеспечивает UPDATE ... WHERE accepted_at IS NULL первым шагом транзакции приёма, в ней же создание учётки и зачисление в группу. Публичные GET/POST /signup/{token}; выпуск/список/отзыв под staff с границами ADR-007 (teacher зовёт только студентов и без группы). httpapi.New принял Config{PublicBaseURL}; DOMAIN стал обязательным ключом api. Web: публичная страница /invite/{token} с предпросмотром и формой пароля, в /admin секция «Позвать по ссылке» с копированием ссылки и списком ожидающих, заведение с паролем убрано под details как запасной путь; в forms.tsx добавлен requestJson. Проверено на стенде: 20 сквозных проверок зелёные, включая 410 на повторный переход и зачисление в группу (подтверждено строкой в group_members). По ходу выяснилось, что рабочий каталог shell'а уехал в deploy/ и ломал пробники — проверка зачисления переписана на запрос к БД. PR #7: https://github.com/IEZhu/class/pull/7. Дальше S1-4: нужны LIVEKIT_URL/API_KEY/API_SECRET, для записи — S3 (bucket + IAM), для транскрипта — ASSEMBLYAI_API_KEY.</outcome> <files>["backend/migrations/0004_invites.up.sql", "backend/internal/store/invites.go", "backend/internal/httpapi/invites.go", "backend/cmd/api/main.go", "web/app/invite/[token]/page.tsx", "web/app/admin/admin-panel.tsx", "docs/architecture/decisions.md"]</files> <tags>["stage-1", "S1-10", "ADR-008", "invites", "onboarding", "pr-7", "livekit", "assemblyai"]</tags> </invoke>


## 2026-08-02T17:21:48+00:00 | 3a6cc66a4153
**Intent:** Подключение LiveKit-комнаты урока (S1-4)
**Action:** Agent: software_engineer — ключи в .env, room-token эндпоинт, embed комнаты, Go-тесты, проверка на стенде, PR #8
**Outcome:** Экран LiveKit про Agents (голосовые боты) — не нужен, жать Skip for now; нужны только Websocket URL, API key и secret проекта. Ключи внесены в deploy/.env (значения в историю не пишутся), шаблон — в deploy/.env.example, проброс — в compose. S1-4 закрыт: GET /lessons/{id}/room-token выдаёт токен в комнату lesson-{id}, identity = user_id (для маппинга спикеров в S2-5), гранты только RoomJoin/CanPublish/CanSubscribe, TTL 3 часа; доступ через canSeeLesson, чужой 403, несуществующий 404, processing/done 409; без ключей эндпоинт отдаёт 503 и стенд остаётся рабочим. Web: lesson-room.tsx берёт токен по клику (не при загрузке — экономия участнико-минут free tier) и монтирует LiveKitRoom/VideoConference. Зависимости: github.com/livekit/protocol v1.50.4 требует Go >= 1.26, билд-образ поднят до golang:1.26.5-alpine; в web добавлены @livekit/components-react, @livekit/components-styles, livekit-client. Первые Go-тесты репозитория (rooms_test.go): гранты выпущенного токена проверяются тем же SDK, чужой секрет отвергается — оба зелёные. На стенде: 200 с корректными claims у преподавателя и двух студентов, 403 не-участнику, 404 на урок 999, 409 на завершённый; /lesson/1 в фазе live рендерит живую кнопку. Урок 1 на стенде переведён в live до 19:20 UTC для ручной проверки. PR #8 https://github.com/IEZhu/class/pull/8 — stacked на PR #7 (база feat/s1-10-invites, использует httpapi.Config оттуда). Дальше S1-3: нужны S3-бакет и IAM-ключи, затем S1-5 (Egress) и S2-1…S2-3 (ключ AssemblyAI).</outcome> <files>["backend/internal/httpapi/rooms.go", "backend/internal/httpapi/rooms_test.go", "backend/cmd/api/main.go", "backend/Dockerfile", "deploy/docker-compose.yml", "deploy/.env.example", "web/app/lesson/[id]/lesson-room.tsx", "docs/plans/stage-1-calendar-livekit.md"]</files> <tags>["stage-1", "S1-4", "livekit", "webrtc", "room-token", "go-tests", "pr-8"]</tags> </invoke>


## 2026-08-02T18:41:56+00:00 | ce560e5d0618
**Intent:** Отладка: в комнате урока не включаются камера и микрофон
**Action:** Agent: software_engineer — проверка заголовков, запрос ListRooms/ListParticipants к LiveKit Cloud, правка lesson-room.tsx, деплой
**Outcome:** Причина локализована фактами, а не гипотезами. Проверено: заголовков Permissions-Policy/Feature-Policy, блокирующих камеру, ни в ответе, ни в конфигах нет. Запрос к LiveKit Cloud (twirp RoomService) показал: комната lesson-1 создана, участник identity=1 Test Teacher в состоянии ACTIVE, can_publish=true, но tracks=[], is_publisher=false, num_publishers=0 — то есть WebSocket-сигналинг проходит, а треки не публикуются: отказывает getUserMedia на стороне браузера. Дефект в моём коде: LiveKitRoom был подключён без onError и onMediaDeviceFailure, поэтому отказ камеры/микрофона уходил в тишину и человек видел пустую комнату без объяснений. Исправлено в b696b71: обработчики onError, onMediaDeviceFailure и onConnected, баннер с конкретной причиной (PermissionDenied — разрешить в замке адресной строки; NotFound — нет устройства; DeviceInUse — занято другой программой) и указанием, камера это или микрофон. tsc и next build зелёные, раскатано на стенд, комната сейчас пуста и готова к повторной попытке. Само устранение отказа зависит от того, какую причину назовёт баннер — проверить это с сервера нельзя.</outcome> <files>["web/app/lesson/[id]/lesson-room.tsx"]</files> <tags>["S1-4", "livekit", "webrtc", "getusermedia", "debugging", "pr-8"]</tags> </invoke>


## 2026-08-02T20:35:46+00:00 | 39a2fa4534c9
**Intent:** Закрыть ревью на PR #7 и #8 и свести всё в main
**Action:** Agent: code_reviewer — правки по 15 замечаниям, ответы в треды, мерж #7 и #8, прогон всех e2e на main
**Outcome:** PR #7 (S1-10 приглашения): 14 замечаний отработаны в ed7fa8c. Бэкенд — граница срока приведена к SQL-условию expires_at > now() (предпросмотр и приём больше не расходятся); отозванная между предпросмотром и приёмом ссылка теперь даёт 404, а не 410 (транзакция проверяет EXISTS по token_hash); сессия создаётся в одной транзакции с учёткой, обещание «учётка и сессия» из 04-api.md стало неделимым; group_name возвращается уже в ответе на создание через CTE с LEFT JOIN groups. Web — сброс выбранной группы после выпуска, key={link} на IssuedLink (сбрасывает «Скопировано»), дата в Intl с timeZone UTC вместо toLocaleDateString (устраняет риск hydration mismatch), кнопка «Принять приглашение» вместо «Войти». Доки — реальные якоря вместо [ADR-007](#) и [ADR-006](#), уточнено что ссылка гасится на приёме а не на предпросмотре, снято обещание отсутствия 409 после проверки email, invites добавлены в таблицу появления схемы, блок S1-10 перемещён после S1-9. PR #8: единственное замечание (глобальный CSS LiveKit) отклонено с доказательством — app-build-manifest.json привязывает static/css/01d24f032f8db9b3.css (19939 байт, классы lk-*) только к маршруту /lesson/[id], у /lessons css нет; перенос в корневой layout добавил бы 20 КБ ко всем страницам. Регрессии проверены на стенде: group_name = «Test Group» в ответе на создание, приём после отзыва → 404 not_found. Все треды закрыты (Copilot свои не закрывает сам — закрыл вручную). Смержены merge-коммитами: #7 → 1c66c9d, затем база #8 переключена с feat/s1-10-invites на main и смержен → 91a58b5. Открытых PR нет. main раскатан на стенд, прогнано 58 сквозных проверок: 24 учётки и роли, 14 границы прав, 20 приглашения, плюс комната (200 с корректным room=lesson-1 identity=1, 403 не-участнику) и страницы /lesson/1 и /admin. Тестовые данные удалены, на стенде 5 пользователей, 2 группы, 0 приглашений. Урок 1 вышел из фазы live в 19:20 UTC — для следующей проверки комнаты время надо сдвинуть.</outcome> <files>["backend/internal/store/invites.go", "backend/internal/httpapi/invites.go", "web/app/admin/admin-panel.tsx", "web/app/invite/[token]/accept-form.tsx", "docs/architecture/decisions.md", "docs/architecture/03-data-model.md", "docs/plans/stage-1-calendar-livekit.md"]</files> <tags>["pr-7", "pr-8", "review-fixes", "merge", "S1-10", "S1-4", "invites", "livekit"]</tags> </invoke>


## 2026-08-03T09:56:31+00:00 | 9e61c9460d3b
**Intent:** Заведение S3-бакета и ключей для записи уроков
**Action:** Agent: devops_engineer — скрипт deploy/s3-bootstrap.sh, ключи S3 в .env.example, ветка feat/s1-3-s3
**Outcome:** Вместо списка кликов по консоли сделан идемпотентный скрипт deploy/s3-bootstrap.sh (коммит 6438629, ветка feat/s1-3-s3): создаёт бакет с учётом особенности us-east-1 (там LocationConstraint не передаётся), закрывает публичный доступ через put-public-access-block, включает SSE-S3 с BucketKeyEnabled, ставит CORS только на GET/HEAD с домена платформы и с ExposeHeaders для Range (нужно для перемотки в плеере), применяет lifecycle по 05-storage-s3.md (recordings/ → STANDARD_IA через 30 дней → GLACIER_IR через 90, плюс уборка незавершённых multipart через 7 дней), заводит IAM-политику строго на один бакет (PutObject/GetObject/DeleteObject на bucket/*, ListBucket/GetBucketLocation на сам бакет) и пользователя с ключом. Узкая политика принципиальна: этот же ключ уходит в LiveKit Egress в теле запроса. На выходе печатает готовые строки для deploy/.env и команду проверки доступа. Проверено: bash -n чист, все четыре встроенных JSON-документа парсятся, захардкоженных ключей нет. Запуск: BUCKET=lingua-class REGION=eu-central-1 ./deploy/s3-bootstrap.sh под админскими правами AWS. Скрипт против реального аккаунта не прогонялся — проверены только синтаксис и JSON. Ключи S3_BUCKET/S3_REGION/S3_ENDPOINT/S3_ACCESS_KEY_ID/S3_SECRET_ACCESS_KEY добавлены в deploy/.env.example.</outcome> <files>["deploy/s3-bootstrap.sh", "deploy/.env.example"]</files> <tags>["stage-1", "S1-3", "s3", "aws", "iam", "lifecycle", "cors", "devops"]</tags> </invoke>


## 2026-08-03T11:32:08+00:00 | 8b9f613d78de
**Intent:** Переход на Cloudflare R2 и заведение хранилища
**Action:** Agent: devops_engineer — сверка с документацией Cloudflare, ADR-009, правка 05-storage-s3.md, переписан deploy/s3-bootstrap.sh
**Outcome:** Решение зафиксировано в ADR-009 (коммит 9a42909, ветка feat/s1-3-s3): хранилище — Cloudflare R2 вместо AWS S3 из ADR-001, потому что записи читают браузеры студентов и AssemblyAI, то есть egress пропорционален просмотрам, а у R2 он бесплатен всегда. Проверено по документации Cloudflare, а не по памяти: эндпоинт https://<account_id>.r2.cloudflarestorage.com, регион в SDK всегда auto (пустое и us-east-1 алиасятся в него), presigned URL поддержаны для GET/HEAD/PUT/DELETE с максимумом 7 дней (наши 30 минут проходят), PutBucketCors и PutBucketLifecycleConfiguration доступны по S3 API. Ключевое ограничение: у R2 только два класса хранения — Standard и Infrequent Access, аналога Glacier нет, поэтому ступень «Glacier IR через 90 дней» из 05-storage-s3.md невыполнима и убрана; роль архива переходит к удалению старого видео с сохранением аудио и VTT. Порог «CloudFront перед бакетом» из 06-budget.md снят вместе с платным egress. Токены R2 создаются только в кабинете: нужны два — временный Admin Read & Write для настройки бакета и постоянный Object Read & Write со скоупом на один бакет для api и LiveKit Egress (бакетная конфигурация рабочему ключу недоступна). deploy/s3-bootstrap.sh переписан под aws cli с --endpoint-url: создание бакета, CORS (GET/HEAD с домена платформы, ExposeHeaders для Range), lifecycle (recordings/ → STANDARD_IA через 30 дней, уборка multipart через 7 дней), проверка и печать строк для .env. Проверено: bash -n чист, оба JSON парсятся. Против живого аккаунта не прогонялось. Возврат на AWS — очистить S3_ENDPOINT и указать реальный регион, код не меняется.</outcome> <files>["docs/architecture/decisions.md", "docs/architecture/05-storage-s3.md", "deploy/s3-bootstrap.sh", "deploy/.env.example"]</files> <tags>["stage-1", "S1-3", "ADR-009", "cloudflare-r2", "s3", "lifecycle", "cors", "presigned"]</tags> </invoke>


## 2026-08-03T11:41:03+00:00 | 1119fd3e4adf
**Intent:** Приём реквизитов R2 и проверка доступности эндпоинта
**Action:** Agent: devops_engineer — запись эндпоинта в .env, диагностика сетевой доступности R2 с VPS
**Outcome:** Из адреса извлечены account id c5768935c9474bc1c561a7a0868e97ec и бакет lingua-class; в deploy/.env записаны S3_BUCKET=lingua-class, S3_REGION=auto, S3_ENDPOINT=https://c5768935c9474bc1c561a7a0868e97ec.r2.cloudflarestorage.com. Ключей доступа пока нет — их нужно создать в кабинете Cloudflare (Object Read & Write со скоупом на бакет). Диагностика доступности: DNS резолвится (172.64.190.1 и IPv6), но TLS-рукопожатие падает с alert 40 handshake failure и по IPv4, и по IPv6, и из контейнеров, и с хоста; при этом developers.cloudflare.com отдаёт 200, а корневой r2.cloudflarestorage.com — 301. Контрольная проверка со случайным несуществующим account id падает идентично, значит дело не в реквизитах аккаунта, а в сетевом пути VPS до хостов *.r2.cloudflarestorage.com; без SNI рукопожатие проходит. Практическое следствие: bootstrap-скрипт с этого VPS запустить нельзя, его надо выполнять с машины пользователя. Для продакшена это скорее всего не критично — presigned URL считается локально без сети, Egress пишет из LiveKit Cloud, AssemblyAI и браузер читают напрямую, то есть VPS к R2 не обращается; но серверные операции с R2 из api работать не будут, пока путь заблокирован. aws cli на VPS не установлен.</outcome> <files>["deploy/.env"]</files> <tags>["stage-1", "S1-3", "cloudflare-r2", "network", "tls", "diagnostics"]</tags> </invoke>


## 2026-08-03T11:47:02+00:00 | 26e1de430958
**Intent:** Проверка доступности R2-эндпоинта с VPS
**Action:** Agent: sysadmin — повторный прогон curl, серия из 10 попыток, проверка из контейнера api
**Outcome:** Вывод пользовательской команды в переписку не попал, поэтому проверка выполнена заново. Результат противоположен прежнему: HEAD на https://c5768935c9474bc1c561a7a0868e97ec.r2.cloudflarestorage.com/lingua-class возвращает HTTP/1.1 400 Bad Request с Server: cloudflare, то есть TLS-рукопожатие проходит. Серия из 10 запросов — 10 успешных, ни одного отказа; из контейнера api тоже 400. Прежний диагноз (alert 40 handshake failure на всех *.r2.cloudflarestorage.com, включая контрольный несуществующий account id) был верен на момент замера, но оказался транзиентным сбоем сетевого пути, а не постоянной блокировкой — вывод отозван. 400 без подписи запроса для S3 API ожидаем: он означает, что до сервиса запрос дошёл. Практическое следствие: bootstrap-скрипт можно запускать и с VPS, ограничение снято. Отдельно отмечено: совет «проверьте с VPS через !» был бесполезен, так как ! исполняется на том же VPS — для сравнения маршрутов нужен запуск с машины пользователя. aws cli на VPS по-прежнему не установлен.</outcome> <files>[]</files> <tags>["stage-1", "S1-3", "cloudflare-r2", "network", "tls", "diagnostics", "transient"]</tags> </invoke>


## 2026-08-03T11:51:56+00:00 | a05c790458dd
**Intent:** Приём ключей R2 и проверка их пригодности для записи уроков
**Action:** Agent: devops_engineer — запись ключей в .env, сквозная проверка операций R2, анализ условия request_ip, правка ADR-009
**Outcome:** Ключи S3_ACCESS_KEY_ID и S3_SECRET_ACCESS_KEY записаны в deploy/.env (в репозиторий не попадают, .env в .gitignore). Проверено на стенде через контейнер amazon/aws-cli:2.32.7: list-objects-v2 — успех на пустом бакете, put-object — ETag получен, aws s3 presign на 900 секунд — ссылка выдана, скачивание с VPS — HTTP 200, delete-object — успех, бакет снова пуст. Ключевая находка: токен создан с условием request_ip in [84.201.5.63/32], и этот адрес — внешний IP самого VPS (проверено через api.ipify.org). Это конфликтует с архитектурой: записи в бакет делает LiveKit Egress со своих адресов, а читают браузеры студентов и AssemblyAI — перечислить эти адреса невозможно. Эмпирическая проверка дала неожиданный результат: presigned GET, подписанный этим токеном, успешно скачался с чужого адреса (через WebFetch, вне VPS), то есть на подписанные ссылки условие request_ip не распространяется. Поведение Egress-записи при таком условии проверить нельзя без реального запуска записи, а цена ошибки — молча потерянная запись урока, поэтому рекомендация — пересоздать токен без ограничения по IP. ADR-009 дополнен этим пунктом (коммит 08271f0). Отдельно отмечено: срок токена истекает 2027-08-03, после чего запись сломается молча; и в чат попал также bearer-токен cfat_, который проекту не нужен — достаточно пары S3-ключей.</outcome> <files>["deploy/.env", "docs/architecture/decisions.md"]</files> <tags>["stage-1", "S1-3", "cloudflare-r2", "ADR-009", "credentials", "presigned", "security"]</tags> </invoke>


## 2026-08-03T12:04:32+00:00 | 597042a07781
**Intent:** Реализация S1-3 (presigned из R2) и S1-5 (запись урока через Egress)
**Action:** Agent: software_engineer — storage-клиент, media-эндпоинт, Twirp-клиент Egress, вебхук LiveKit, тесты, проверки на стенде, PR #9
**Outcome:** S1-3 и S1-5 закрыты одним PR #9 (https://github.com/IEZhu/class/pull/9), ветка feat/s1-3-s3. S1-3: internal/storage/s3.go поверх aws-sdk-go-v2 — PresignGet с TTL 30 минут, Exists, RecordingKey по раскладке 05-storage-s3.md; для R2 включён UsePathStyle, иначе запрос уходит на bucket.<account>.r2 и объект не находится; без ключей New возвращает nil без ошибки и медиа-эндпоинты отвечают 503. GET /media/{lesson_id}/url отдаёт ссылку участнику урока с границей canSeeLesson. S1-5: internal/livekitapi/egress.go — свой тонкий Twirp-клиент (POST /twirp/livekit.Egress/StartRoomCompositeEgress, protojson, грант RoomRecord) вместо server-sdk-go, который тянет весь pion/webrtc ради одного вызова; POST /webhooks/livekit с проверкой подписи через webhook.ReceiveWebhookEvent; room_started запускает Egress, egress_ended пишет recording_s3_key и статус; идемпотентность обеспечена условием status='scheduled' в UPDATE. EGRESS_AUDIO_ONLY=1 переключает на OGG. Точка вставки ASR — onEgressEnded. Проверено против живых сервисов: Twirp-путь Egress подтверждён ответом LiveKit not_found о комнате (значит маршрут и токен приняты); на живом R2 объект положен, /media/1/url вернул 200, скачивание по ссылке дало совпадение содержимого побайтно, Range 0-5 вернул 206; вебхуки с настоящей подписью — egress_ended проставил ключ и done, чужая комната без эффекта, EGRESS_FAILED не затирает урок, room_started перевёл в live и запустил Egress, повторная доставка второй Egress не запустила; без подписи и с мусорной 401; границы медиа-эндпоинта 404/404/403/401. Go-тесты: раскладка ключей, форма egress-запроса для видео и audio-only включая ForcePathStyle, разбор lesson-{id} с round-trip, форма presigned-ссылки с проверкой что секрет не утёк в URL. Тестовые данные из R2 и БД удалены. Открыт долг D-6: токен R2 истекает 2027-08-03 и запись сломается молча. Не проверена única вещь — реальная запись Egress в R2 живого урока, для неё нужен урок в фазе live с настоящей комнатой.</outcome> <files>["backend/internal/storage/s3.go", "backend/internal/livekitapi/egress.go", "backend/internal/httpapi/webhooks.go", "backend/internal/httpapi/media.go", "backend/internal/store/recordings.go", "deploy/docker-compose.yml", "docs/plans/stage-1-calendar-livekit.md"]</files> <tags>["stage-1", "S1-3", "S1-5", "cloudflare-r2", "livekit", "egress", "presigned", "webhook", "pr-9"]</tags> </invoke>


## 2026-08-03T12:15:59+00:00 | c0cee44e6301
**Intent:** Плеер записи урока и подтверждение, что запись со звуком
**Action:** Agent: software_engineer — Plyr в фазе done, проверка записи со звуком на живом R2, PR #10
**Outcome:** S1-6 закрыт, PR #10 https://github.com/IEZhu/class/pull/10 (ветка feat/s1-6-player, stacked на #9 с базой feat/s1-3-s3, потому что плеер живёт на GET /media/{lesson_id}/url оттуда). Заглушка плеера в фазе done заменена рабочим Plyr поверх обычного video: ссылка из /media/{lesson_id}/url, файл идёт хранилище-браузер минуя VPS, перемотка на Range-запросах R2 без стриминговой обвязки. Plyr грузится динамическим import() — в типах он объявлен через export = и default-импорта не даёт, побочный выигрыш в весе маршрута /lesson/[id] со 204 kB обратно до 171 kB. Presigned живёт 30 минут и может истечь посреди длинной записи, поэтому по onError плеер берёт свежую ссылку и возвращается на ту же позицию. Скорость 0.75-2x, подписи по-русски. Про звук: запись RoomComposite без VideoOnly содержит обе дорожки — проверено конкретно, ffmpeg'ом сгенерирован MP4 с h264 и AAC 44100 Hz, положен в R2, скачан по presigned (80624 байта, Content-Type video/mp4) и ffprobe на скачанном файле показал обе дорожки. Перемотка: Range 40000-40999 вернул 206 с Content-Range bytes 40000-40999/80624 и Accept-Ranges bytes. DoD задачи: не-участник получает 403. tsc и next build зелёные, тестовые данные из R2 и БД откачены. EGRESS_AUDIO_ONLY=1 переключает на OGG только звук, если понадобится экономия.</outcome> <files>["web/app/lesson/[id]/lesson-player.tsx", "web/app/lesson/[id]/page.tsx", "web/package.json", "docs/plans/stage-1-calendar-livekit.md", "docs/tasks/README.md"]</files> <tags>["stage-1", "S1-6", "plyr", "player", "presigned", "cloudflare-r2", "pr-10"]</tags> </invoke>


## 2026-08-10T19:32:24+00:00 | 48641ba3e96a
**Intent:** Открытые события с гостевым доступом по ссылке и UI создания событий
**Action:** Agent: system_architect → software_engineer — ADR-010, миграция 0005, гостевые эндпоинты, web-формы, проверки на стенде, PR #11
**Outcome:** Обе части были вне планов 0-5: «событие» в доках всюду означало календарное событие урока, гостей не было вовсе, UI создания урока отсутствовал. Пользователь выбрал из предложенных развилок: событие = урок без группы, гость живёт только на время события. ADR-010 зафиксировал решение. Миграция 0005_open_events: lessons.group_id стал nullable, добавлены title и guest_token_hash (UNIQUE, sha256), CHECK на наличие названия, роль guest в users.role. Гость заводится строкой в users вместе с участием и сессией одной транзакцией — благодаря этому canSeeLesson, identity для LiveKit и будущий маппинг спикеров работают без правок; пароля нет, сессия живёт до конца события плюс 2 часа. Ссылка многоразовая, в отличие от одноразовых приглашений ADR-008; распоряжается ей организатор, перевыпуск гасит предыдущую. Гости скрыты из ListUsers, email синтетический guest+<хэш>@guests.local. Web: форма создания события на /lessons (без группы — открытое событие, datetime-local переводится в UTC), выдача и отзыв ссылки на странице события, публичная страница /join/{token} с предупреждением о записи до ввода имени, lib/lesson-title.ts для подписи урока в одном месте. Проверено на стенде: создание события 201 с group_id null, без группы и названия 400, студент выпускает ссылку 403, двое гостей зашли по одной ссылке и оба стали участниками, гость получил room-token с room=lesson-5 и своим identity, на чужой урок 403, отзыв гасит ссылку (404), перевыпуск гасит предыдущую, в GET /users гостей 0 при 2 в БД, страницы /join живой и отозванной ссылки рендерятся. PR #11 https://github.com/IEZhu/class/pull/11, stacked на #10 и #9, порядок мержа 9-10-11. Тестовые данные удалены.</outcome> <files>["backend/migrations/0005_open_events.up.sql", "backend/internal/store/guests.go", "backend/internal/httpapi/guests.go", "backend/internal/store/lessons.go", "web/app/join/[token]/page.tsx", "web/app/lessons/new-event.tsx", "web/app/lesson/[id]/guest-link.tsx", "docs/architecture/decisions.md"]</files> <tags>["stage-1", "S1-11", "S1-12", "ADR-010", "guest-access", "open-events", "livekit", "pr-11"]</tags> </invoke>


## 2026-08-10T19:53:02+00:00 | 7109a671caf8
**Intent:** Проверить состояние деплоя и подготовить сценарий ручного теста
**Action:** Agent: devops_engineer — проверка состояния стенда, создание живого события и гостевой ссылки
**Outcome:** Стенд lang.wondermr.com обновлён и содержит весь функционал: образы lingua-web и lingua-api собраны 2026-08-10T19:28 из ветки feat/s1-11-events (коммит afcec66), которая стоит поверх feat/s1-6-player и feat/s1-3-s3, миграции накатаны до версии 5 без dirty. Новые эндпоинты отвечают: POST /api/join/{token} 400 на пустое тело, /api/media/1/url 405 на POST, /api/webhooks/livekit 401 без подписи. Важная оговорка: стенд собран НЕ из main — main остался на 91a58b5 после PR #8, а PR #9, #10 и #11 открыты; если пересобрать из main, записи, плеера и событий на стенде не станет. Для ручной проверки создано открытое событие id=7 «Тестовое событие», живое с -5 минут до +3 часов (до 2026-08-10T22:52Z), выпущена гостевая ссылка https://lang.wondermr.com/join/cfe9e0e4f32fff419272f5a8876745e4724ca7fb8c0292525d2687125ef64b19 и страница события https://lang.wondermr.com/lesson/7. Обе страницы проверены: гостевая отдаёт 200 с названием, предупреждением о записи и полем имени; страница события под преподавателем отдаёт 200 с фазой «Идёт сейчас», кнопкой «Войти в класс» и блоком гостевой ссылки. Не проверено вживую: реальная запись Egress в R2 при настоящей комнате — это можно увидеть только на живом занятии.</outcome> <files>[]</files> <tags>["deploy", "stand", "testing", "S1-11", "guest-link", "livekit"]</tags> </invoke>

