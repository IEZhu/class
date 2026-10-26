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
                       1: GOOGLE_OAUTH_*, TOKEN_ENC_KEY, LIVEKIT_*, S3_*
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
