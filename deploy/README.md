# deploy — Docker Compose, Caddy, окружение

Появляется на этапе 0 (S0-1). Единственная точка входа в эксплуатацию
стенда. Обзор сервисов: `../docs/architecture/02-services.md`.

## Планируемое содержимое

```text
docker-compose.yml   сервисы по этапам:
                       этап 0: caddy, web, api, postgres
                       этап 1: + worker
                       этап 3: + libretranslate (--load-only en,ru), + excalidraw
                       опция:  redis (только при реальной нужде — ADR-003)
Caddyfile            TLS (Let's Encrypt), '/' → web, '/api' → api, доска
.env.example         шаблон секретов; растёт по этапам:
                       0: POSTGRES_*, DOMAIN, HTTP_PORT (ADR-004),
                          SEED_PASSWORD (ADR-006; SESSION_SECRET удалён)
                       1: GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET,
                          GOOGLE_TOKEN_KEY (S1-1), LIVEKIT_*, S3_*
                       2: ASSEMBLYAI_*
                       5: LLM_PROVIDER, LLM_API_KEY
backup-pg.sh         ночной pg_dump | zstd → S3, retention 7/30 (S1-7)
restore-pg.sh        восстановление из копии в S3 (S1-7)
backup-lib.sh        общая часть двух скриптов выше
systemd/             юнит и таймер ночного бэкапа (S1-7)
```

## Бэкапы Postgres (S1-7)

Решение и трактовка retention — ADR-014 в
[decisions.md](../docs/architecture/decisions.md). Коротко: каждую ночь
`pg_dump` (custom-формат) сжимается zstd и уходит в
`s3://<бакет>/backups/pg/<дата UTC>.dump.zst`; скрипт сверяет выгруженный
объект по sha256, затем чистит: все копии за 7 суток, дальше по одной на
ISO-неделю до 30 суток (~11 копий). Объекты с другими именами под
`backups/pg/` (ручные копии) чистка не трогает. Любой сбой — ненулевой
код выхода, юнит уходит в `failed`.

Что нужно на хосте: docker с запущенным стеком и `zstd`
(`apt install zstd`). aws cli ставить не надо — он запускается из образа
`amazon/aws-cli`, закреплённого digest'ом. Пароль БД скриптам не нужен:
pg_dump и pg_restore работают внутри контейнера postgres по локальному
сокету, как `make psql`.

### Хранилище и токен

Дамп содержит хэши паролей и email всех учёток, а токен `S3_*` api
передаёт LiveKit в каждом egress-запросе. Поэтому бэкапам нужен свой
бакет и свой токен:

1. Cloudflare → R2 → Create bucket, напр. `lingua-class-backups`
   (CORS не нужен, бакет приватный).
2. R2 → Manage API tokens → Object Read & Write, scope — только этот бакет.
3. В `deploy/.env`:
   `BACKUP_S3_BUCKET=lingua-class-backups`,
   `BACKUP_S3_ACCESS_KEY_ID=…`, `BACKUP_S3_SECRET_ACCESS_KEY=…`
   (эндпоинт и регион возьмутся из `S3_*`).

Пока `BACKUP_S3_*` пусты, бэкап пишется в бакет записей под общим токеном
и предупреждает об этом в журнале (долг D-8).

### Установка таймера

Юниты рассчитаны на клон в `/opt/Class`; в другом месте — поправить
`WorkingDirectory` и `ExecStart` в `systemd/lingua-backup-pg.service`.

```bash
sudo deploy/backup-pg.sh          # первый прогон руками: копия + чистка
sudo install -m 0644 deploy/systemd/lingua-backup-pg.service \
    deploy/systemd/lingua-backup-pg.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now lingua-backup-pg.timer
systemctl list-timers lingua-backup-pg.timer   # следующий прогон 03:30 UTC
journalctl -u lingua-backup-pg -n 50           # журнал прогона
systemctl --failed                             # упавший прогон виден здесь
```

Показать, что удалила бы чистка, ничего не трогая:
`sudo deploy/backup-pg.sh --prune-only --dry-run`.

### Восстановление

```bash
sudo deploy/restore-pg.sh --list                     # какие копии есть
# проверка копии: разворот в отдельную базу рядом с рабочей
sudo deploy/restore-pg.sh --db lingua_restore_check  # самая свежая
sudo deploy/restore-pg.sh --db lingua_restore_check --date 2026-10-25 --force
make psql   # сверить данные, затем: DROP DATABASE lingua_restore_check;
```

Восстановление стенда (RTO — час): новая VPS, клон репозитория,
`deploy/.env` из менеджера паролей, затем

```bash
docker compose -f deploy/docker-compose.yml up -d postgres
sudo deploy/restore-pg.sh --db lingua    # база POSTGRES_DB пуста — --force не нужен
make migrate                             # миграции новее копии, если есть
make up
```

Непустую базу скрипт перезаписывает только с `--force`
(`pg_restore --clean` одной транзакцией); перед этим остановите api
и web: `docker compose -f deploy/docker-compose.yml stop caddy web api`.
Восстановление идёт одной транзакцией: при ошибке база остаётся какой была.
Объекты, которых в копии нет, `--clean` не удаляет — для разворота начисто
базу удаляют и создают заново (`make psql`).

Переменные для эксплуатации и проверки на одноразовом стенде
(`BACKUP_ENV_FILE`, `BACKUP_PG_CONTAINER`, `BACKUP_AWS_IMAGE`,
`BACKUP_DOCKER_NETWORK`) описаны в шапке `backup-lib.sh`.

## Google Calendar (S1-1)

Решение — ADR-023 в [decisions.md](../docs/architecture/decisions.md).
Преподаватель подключает свой Google Calendar на `/account`; дальше worker
ставит в него уроки событиями (`calendar_sync`), и участники получают
приглашения от Google. Пока ключей нет, раздел на `/account` пишет
«недоступно», а остальной стенд работает (после миграции `0012`: уроки
читают колонку токенов и без ключей, поэтому `make migrate` — до выката
api, иначе создание и перенос урока отвечают 500).

**Redirect URI стенда:** `https://lang.wondermr.com/api/auth/google/callback`
(в общем виде — `https://<DOMAIN>/api/auth/google/callback`; caddy стрипает
`/api`, внутри api это `GET /auth/google/callback`). Он должен совпасть
с записанным в клиенте Google до символа.

Создание OAuth-клиента в [Google Cloud console](https://console.cloud.google.com/)
(названия разделов — по документации Google на октябрь 2026, консоль
их иногда переименовывает):

1. Создать проект (например `lingua-class`) и выбрать его.
2. APIs & Services → Library → **Google Calendar API** → Enable.
3. Google Auth Platform → Branding: название приложения, email поддержки,
   в Authorized domains — `wondermr.com`.
4. Audience: User type **External**.
5. Data Access → Add or remove scopes → вписать
   `https://www.googleapis.com/auth/calendar.events` → Update → Save.
   Других scope приложению не нужно.
6. Clients (он же APIs & Services → Credentials) → Create client →
   Application type **Web application**; Authorized redirect URIs —
   redirect URI стенда выше. JavaScript origins не нужны.
   Скопировать Client ID и Client secret.
7. Audience → **Publish app** (статус In production). В статусе Testing
   refresh token живёт 7 дней — календарь отваливался бы раз в неделю.
   Без проверки Google экран согласия покажет предупреждение «приложение
   не проверено», а пользователей будет до ~100 — для пяти преподавателей
   это приемлемо ([google-calendar.md](../docs/architecture/integrations/google-calendar.md)).

Затем в `deploy/.env`:

```bash
GOOGLE_CLIENT_ID=…apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=…
GOOGLE_TOKEN_KEY=$(openssl rand -base64 32)   # вписать значение, не команду
```

и выкат: `make migrate` (миграция `0012` — колонка токенов), затем
`make up` (api и worker перечитают ключи). Проверка: в журнале api и
worker больше нет строки «Google Calendar выключен»; преподаватель на
`/account` → «Подключить Google Календарь» → экран Google → обратно
с «Календарь подключён»; новый урок через минуту в его календаре,
у студентов — приглашение.

`GOOGLE_TOKEN_KEY` шифрует refresh-токены в БД (и в бэкапах). Потерян или
заменён ключ — сохранённые токены не расшифровываются: календари
считаются отключёнными, преподаватели подключают их заново. Хранить
ключ вместе с остальным `deploy/.env` в менеджере паролей.

**Если `deploy/.env` утёк** (вместе с ним — ключ к токенам в любом дампе
или бэкапе БД), одной замены `GOOGLE_TOKEN_KEY` мало: старые refresh-токены,
зашифрованные утёкшим ключом, в Google остаются рабочими. Их гасит смена
client secret — без него refresh token на access token не обменять
([Manage OAuth Clients](https://support.google.com/cloud/answer/15549257),
у клиента может быть не больше двух секретов):

1. Google Auth Platform → Clients → клиент стенда → **Add secret**;
   скопировать новый секрет.
2. В `deploy/.env` — новый `GOOGLE_CLIENT_SECRET` и новый
   `GOOGLE_TOKEN_KEY=$(openssl rand -base64 32)`; `make up`.
3. Там же у старого секрета — **Disable** (и после проверки — Delete):
   выключенный секрет Google отклоняет, утёкшие токены бесполезны.
4. Преподаватели подключают календарь на `/account` заново: прежние токены
   не расшифровываются новым ключом.

Отключение на `/account` отзывает токен в Google, а с ним — весь доступ
приложения к аккаунту. Если Google не ответил или токен уже не
расшифровывается, страница просит снять доступ вручную: аккаунт Google →
Безопасность → сторонние приложения с доступом к аккаунту. Уже созданные
события отключение не удаляет (ADR-023).

## Правила

- Реальный `.env` не коммитится (см. корневой `.gitignore`); каждый новый
  ключ сначала попадает в `.env.example` и в «Факт» плана этапа.
- Целевая VPS: 4 vCPU / 8 GB (минимум 2/4); суммарная оценка RAM сервисов
  ~2.5–3 GB.
- RTO: «пересоздать compose за час» из бэкапов S3.
