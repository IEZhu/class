# deploy — Docker Compose, Caddy, окружение

Появляется на этапе 0 (S0-1). Единственная точка входа в эксплуатацию
стенда. Обзор сервисов: `../docs/architecture/02-services.md`.

## Планируемое содержимое

```text
docker-compose.yml   сервисы по этапам:
                       этап 0: caddy, web, api, postgres
                       этап 1: + worker
                       этап 3: + libretranslate (--load-only en,ru),
                               + whiteboard (ретранслятор доски, профиль, ADR-020)
                       этап 5: + harvester (профиль harvester, ADR-022)
                       опция:  redis (только при реальной нужде — ADR-003)
Caddyfile            '/' → web, '/api' → api, '/whiteboard/lesson-{id}/socket.io/'
                     → ретранслятор доски после forward_auth в api
.env.example         шаблон секретов; растёт по этапам:
                       0: POSTGRES_*, DOMAIN, HTTP_PORT (ADR-004),
                          SEED_PASSWORD (ADR-006; SESSION_SECRET удалён)
                       1: GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET,
                          GOOGLE_TOKEN_KEY (S1-1), LIVEKIT_*, S3_*,
                          BACKUP_S3_* (ADR-014)
                       2: ASSEMBLYAI_*
                       3: COMPOSE_PROFILES, LIBRETRANSLATE_URL (ADR-018),
                          WHITEBOARD_ENABLED (ADR-020)
                       5: HARVESTER_COOKIES_FILE, HARVESTER_DATABASE_URL (ADR-022),
                          LLM_PROVIDER, LLM_API_KEY
backup-pg.sh         ночной pg_dump | zstd → S3, retention 7/30 (S1-7)
restore-pg.sh        восстановление из копии в S3 (S1-7)
backup-lib.sh        общая часть двух скриптов выше
harvester-db-role.sh своя роль базы сборщика субтитров (S5-2, ADR-022)
systemd/             юнит и таймер ночного бэкапа (S1-7)
```

## Бэкапы Postgres (S1-7)

Решение и трактовка retention — ADR-014 в
[decisions.md](../docs/architecture/decisions.md). Коротко: каждую ночь
`pg_dump` (custom-формат) сжимается zstd и уходит в
`s3://<бакет>/backups/pg/<дата UTC>.dump.zst`. Выгрузка идёт во временный
`backups/pg/incoming/<дата>.<pid>.dump.zst`, скачивается обратно и
сверяется по sha256; только проверенная копия переносится (копированием
внутри хранилища) в ночной ключ, а её ключ, sha256 и размер дописываются
в манифест `/var/lib/lingua-backup/manifest` (root, `0600`). Сбой
проверки ночной ключ не трогает, временный объект удаляется. Затем
чистка: все копии за 7 суток, дальше по одной на ISO-неделю до 30 суток
(~11 копий). Объекты с другими именами под `backups/pg/` (ручные копии)
чистка не трогает. Ночную копию с датой позже сегодняшней (её писал не
этот скрипт или сбиты часы) чистка не удаляет, но называет в журнале
и завершается с ошибкой — разобраться и удалить вручную. Любой сбой —
ненулевой код выхода, юнит уходит в `failed`.

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

Пока все `BACKUP_S3_*` пусты, бэкап пишется в бакет записей под общим
токеном и предупреждает об этом в журнале (долг D-8). Задан хоть один
`BACKUP_S3_*`, а `BACKUP_S3_BUCKET` пуст или совпадает с `S3_BUCKET`
на том же эндпоинте — оба скрипта падают: без этого ключи без бакета
молча писали бы дампы в бакет записей.

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
sudo deploy/restore-pg.sh --db lingua_restore_check  # самая свежая, не позже сегодня (UTC)
sudo deploy/restore-pg.sh --db lingua_restore_check --date 2026-10-25 --force
make psql   # сверить данные, затем: DROP DATABASE lingua_restore_check;
```

Копия сверяется по sha256 и размеру с манифестом: его пишет только
`backup-pg.sh` этого хоста, а метаданные объекта может подделать любой,
у кого есть токен хранилища. Копии нет в манифесте (ручная, с другого
хоста) — отказ; развернуть её без сверки — `--insecure-no-verify`, когда
уверены в происхождении. Расхождение с манифестом не обходится ничем.
Манифест с чужим владельцем или правом записи для группы и прочих
не принимается.

Восстановление стенда (RTO — час): новая VPS, клон репозитория,
`deploy/.env` из менеджера паролей, затем

```bash
docker compose -f deploy/docker-compose.yml up -d postgres
sudo deploy/restore-pg.sh --list
sudo deploy/restore-pg.sh --db lingua --date 2026-10-25   # копию в рабочую базу — только явно
make migrate                             # миграции новее копии, если есть
make up
```

В рабочую базу (`POSTGRES_DB`) скрипт восстанавливает только копию,
названную явно (`--date` или `--key`). На новой VPS манифеста нет,
и копия сверяется с sha256 из её же метаданных — это защита от порчи,
не от подмены. Чтобы сверка при переезде была настоящей, держите копию
манифеста рядом с `deploy/.env` в менеджере паролей и положите её в
`/var/lib/lingua-backup/manifest` (`chown root`, `chmod 600`) до
восстановления.

Непустую базу скрипт перезаписывает только с `--force`
(`pg_restore --clean` одной транзакцией); перед этим остановите api,
web и worker: `docker compose -f deploy/docker-compose.yml stop caddy web api worker`.
Восстановление идёт одной транзакцией: при ошибке база остаётся какой была.
Объекты, которых в копии нет, `--clean` не удаляет — для разворота начисто
базу удаляют и создают заново (`make psql`).

Переменные для эксплуатации и проверки на одноразовом стенде
(`BACKUP_ENV_FILE`, `BACKUP_PG_CONTAINER`, `BACKUP_AWS_IMAGE`,
`BACKUP_DOCKER_NETWORK`, `BACKUP_MANIFEST`) описаны в шапке `backup-lib.sh`.

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

## Бакет: загрузка файлов материалов (D-4)

Файлы материалов браузер грузит в R2 сам, по presigned PUT от api
([ADR-024](../docs/architecture/decisions.md)). Для этого CORS бакета
должен пускать `PUT` с домена платформы, а загрузки под
`materials-pending/` — стираться через сутки: CORS-правило и
lifecycle-правило есть в `s3-bootstrap.sh`. Бакет, настроенный до D-4,
разрешает только GET/HEAD: пока его не перенастроить, кнопка «Прикрепить
файл» падает на шаге загрузки (долг D-14). Владельцу стенда:

1. В кабинете Cloudflare — временный токен Admin Read & Write.
2. `ACCOUNT_ID=… BUCKET=… AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=…
   ./deploy/s3-bootstrap.sh` — скрипт идемпотентен, lifecycle
   перезапишется тем же.
3. Проверить: на странице урока прикрепить файл и скачать его;
   временный токен удалить.

## Сборщик субтитров (S5-1, S5-2)

Решение — ADR-021/ADR-022, поток —
[subtitle-harvester.md](../docs/architecture/flows/subtitle-harvester.md).
Сервис `harvester` — тот же worker с ролью `harvester` и yt-dlp — стоит
под профилем compose: он ходит в YouTube с IP стенда, поэтому `make up`
его не поднимает.

Сборщик запускает yt-dlp, который разбирает ответы YouTube в том же
контейнере и видит его окружение, поэтому DSN ему — своей роли только
с таблицами сборщика и очереди, а не владельца базы. Перед первым
запуском: в `deploy/.env` — `HARVESTER_DATABASE_URL` (шаблон и пароль
`openssl rand -hex 32` — в `.env.example`), затем скрипт роли; он
идемпотентен, повтор обновляет пароль и сводит права к списку в скрипте.
Пустой `HARVESTER_DATABASE_URL` — сборщик под владельцем, как до ревью.

```bash
make migrate    # миграция 0011_subtitles: таблицы и seed каналов
deploy/harvester-db-role.sh   # роль из HARVESTER_DATABASE_URL
docker compose -f deploy/docker-compose.yml --profile harvester up -d --build harvester
docker compose -f deploy/docker-compose.yml logs -f harvester
docker compose -f deploy/docker-compose.yml stop harvester    # выключить
```

Начнёт YouTube проверять «вы не бот» — выгрузить cookies (формат Netscape)
и указать путь к файлу на хосте в `HARVESTER_COOKIES_FILE`, затем
пересоздать сервис. Файл cookies должен читаться пользователем контейнера
uid 10001: `chown 10001 <файл> && chmod 600 <файл>`; иначе сборщик пишет
«yt-dlp не запускается: cookies: …» и ждёт, не тратя попытки видео.
Сервис работает с корнем только на чтение, без capabilities, с потолком
512 MB и 1 CPU до замера S5-2. Блокирует IP VPS — остановить сервис
и запустить тот же образ с другой машины (команды — в потоке). Состояние: `make psql`,
затем `SELECT title, polled_at, last_error FROM subtitle_sources;` и
`SELECT subtitles, count(*) FROM videos GROUP BY 1;`.

## Правила

- Реальный `.env` не коммитится (см. корневой `.gitignore`); каждый новый
  ключ сначала попадает в `.env.example` и в «Факт» плана этапа.
- Целевая VPS: 4 vCPU / 8 GB (минимум 2/4); суммарная оценка RAM сервисов
  ~2.5–3 GB.
- RTO: «пересоздать compose за час» из бэкапов S3.

## Перевод слов: LibreTranslate (S3-3)

Решение — ADR-018 в [decisions.md](../docs/architecture/decisions.md).
Сервис `libretranslate` (образ закреплён digest'ом) живёт в профиле
`translate` и по умолчанию не стартует: его образ не проходит блокирующий
скан ADR-011 (долг D-10), включение — решение владельца стенда. Сеть
`translate` — `internal`: у контейнера нет выхода в интернет и портов
наружу, к нему ходит только api. Без сервиса клик по слову отвечает
дефинициями и `translation: null`.

```bash
make translate-models   # один раз: модели en↔ru в том lt_models (~370 MB, нужен интернет)
# в deploy/.env:
#   COMPOSE_PROFILES=translate
#   LIBRETRANSLATE_URL=http://libretranslate:5000
make up                 # api перечитает LIBRETRANSLATE_URL, поднимется libretranslate
docker compose -f deploy/docker-compose.yml ps libretranslate   # healthy через 1–2 мин
```

Ресурсы: `LT_THREADS=1` (один процесс gunicorn с одной копией моделей),
`cpus: 1.5`, `mem_limit: 2g`; на этом VPS — ~380 MB RAM, перевод слова
0.1–0.6 с. Обновить модели — снова `make translate-models` и
`docker compose -f deploy/docker-compose.yml restart libretranslate`.

## Словарь: импорт дефиниций (S3-4)

Решение — ADR-019. Команда `dictimport` лежит в образе worker'а и пишет
в таблицу `definitions` (миграция 0009 должна быть применена: `make
migrate`). Источник — выгрузка Wiktextract английского Wiktionary
с kaikki.org (CC BY-SA / GFDL): по умолчанию полный
`raw-wiktextract-data.jsonl.gz` (≈2.9 GB gzip; читается потоком, на диск
не сохраняется), из него берутся только английские статьи слов,
известных лемматизатору, и служебные слова; на статью — до 3 значений
и 5 русских переводов. Ожидаемый объём — 80–150 тыс. статей, 60–120 MB
в PG; разбор — порядка 5–10 мин CPU плюс загрузка.

```bash
make up            # образ worker'а с dictimport
make migrate
make dict-import   # итог в последней строке: строк, статей, удалено устаревших
```

Оборвался (сеть, SIGTERM) — тот же `make dict-import` продолжит
с последней зафиксированной пачки, если версия источника (ETag или
Last-Modified) не сменилась; сменилась — команда откажется,
тогда `docker compose -f deploy/docker-compose.yml run --rm --entrypoint
dictimport worker -source <URL> -restart`. Повторный импорт свежего
дампа обновляет статьи и удаляет пропавшие — только после успешного
завершения. На медленном канале надёжнее скачать файл заранее
(`curl -C - -O <URL>` докачивает) и импортировать локально:

```bash
make dict-import DICT_DIR=/var/tmp/wiktextract DICT_SOURCE=/data/raw-wiktextract-data.jsonl.gz
```

Каталог монтируется в контейнер только на чтение; файл должен быть
читаем для пользователя контейнера (uid 10001): `chmod a+r`.

## Доска (S3-6)

Решение — ADR-020 в [decisions.md](../docs/architecture/decisions.md).
Ретранслятор доски `whiteboard` (excalidraw-room, образ закреплён
digest'ом) живёт в профиле `whiteboard` и по умолчанию не стартует: его
образ не проходит блокирующий скан ADR-011 (долг D-11), включение —
решение владельца стенда. Без него вкладка «Доска» отвечает 503,
остальной стенд работает; снимки сцен worker пишет в хранилище `S3_*`.

```bash
# в deploy/.env (профили — через запятую):
#   COMPOSE_PROFILES=whiteboard     # с переводом: translate,whiteboard
#   WHITEBOARD_ENABLED=1
make migrate    # миграция 0010_whiteboards, если ещё не применена
make up         # поднимется whiteboard, api перечитает WHITEBOARD_ENABLED
docker compose -f deploy/docker-compose.yml ps whiteboard   # healthy в пределах минуты
```

**WebSocket через host-nginx.** Снаружи ретранслятор доступен по
`/whiteboard/lesson-{id}/socket.io/` (caddy пускает после forward_auth
в api). caddy WebSocket проксирует сам, а host-nginx (ADR-004) без
заголовков `Upgrade` его не пробросит: socket.io тогда останется на
long-polling — доска работает, но с задержкой и лишними запросами.
В vhost `/etc/nginx/sites-available/lang.wondermr.com` владельцу стенда
нужен для `/whiteboard/` блок с теми же `proxy_pass` и заголовками, что
у основного `location /`, плюс три строки:

```nginx
location /whiteboard/ {
    # proxy_pass и proxy_set_header — как в location / этого vhost
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

затем `sudo nginx -t && sudo systemctl reload nginx`. Проверка:
в DevTools браузера на вкладке «Доска» запрос `socket.io/?EIO=4&transport=websocket`
отвечает `101 Switching Protocols`.

## Примеры Tatoeba: импорт корпуса (S5-4)

Решение — ADR-028, источник и лицензия —
[docs/architecture/integrations/tatoeba.md](../docs/architecture/integrations/tatoeba.md).
Команда `tatoebaimport` лежит в образе worker'а и пишет в
`tatoeba_sentences` и `tatoeba_links` (миграция 0016 должна быть
применена: `make migrate`). По умолчанию качает три официальные
поязыковые выгрузки tatoeba.org — пары `eng-rus_links.tsv.bz2` (≈5 MB)
и подробные предложения `eng_sentences_detailed.tsv.bz2` (≈35 MB),
`rus_sentences_detailed.tsv.bz2` (≈24 MB); читает потоком, на диск
не сохраняет. Остаются английские предложения с русским переводом,
оба до 150 символов, один перевод на предложение.

Ожидаемый объём (оценка на синтетической выгрузке полного размера,
ADR-028): ~544 тыс. английских и ~474 тыс. русских предложений,
~544 тыс. пар; ~250 MB в PG с индексами (сразу после импорта ~300 MB),
сжатый дамп корпуса — от 34 MB; сам импорт — пара минут плюс загрузка
64 MB. Итог прогона команда печатает последней строкой.

```bash
make up              # образ worker'а с tatoebaimport
make migrate
make tatoeba-import  # итог: пары, испорченные строки, записано/удалено, корпус
```

Оборвался (сеть, SIGTERM) — тот же `make tatoeba-import` продолжит
с последней зафиксированной пачки своей фазы, если не сменилась версия
ни одного из трёх файлов (ETag или Last-Modified); Tatoeba обновляет
выгрузки по субботам, и после обновления команда откажется — тогда
`docker compose -f deploy/docker-compose.yml run --rm --entrypoint
tatoebaimport worker -restart`. Повторный импорт свежей выгрузки
обновляет корпус и удаляет пропавшие предложения — только после
успешного завершения. Файлы можно скачать заранее и импортировать
локально:

```bash
make tatoeba-import TATOEBA_DIR=/var/tmp/tatoeba   # в каталоге — три файла с именами как на сайте
```

Каталог монтируется в контейнер только на чтение (`/data`); файлы
должны быть читаемы для uid 10001: `chmod a+r`. Проверка после
импорта — клик по частотному слову (run, bank) отдаёт в `examples`
1–2 примера с переводом и подписью (`attribution`, CC BY 2.0 FR).
