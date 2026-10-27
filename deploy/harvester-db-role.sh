#!/usr/bin/env bash
# Своя роль Postgres для сборщика субтитров (S5-2, ADR-022). yt-dlp
# разбирает ответы YouTube в том же контейнере, что и worker, и прочтёт
# его окружение, поэтому DSN сборщика даёт доступ только к его таблицам
# (subtitle_sources, videos, subtitle_segments) и к очереди (jobs,
# job_schedules); users, sessions, гостевые ссылки и остальное роли
# не видны.
#
# Имя роли и пароль берутся из HARVESTER_DATABASE_URL в deploy/.env — тот
# же DSN compose отдаёт сервису harvester, а с другой машины его передают
# в DATABASE_URL. Пароль не попадает ни в репозиторий, ни в аргументы
# процессов, ни на экран.
#
#   1) в deploy/.env (пароль — openssl rand -hex 32):
#      HARVESTER_DATABASE_URL=postgres://lingua_harvester:<пароль>@postgres:5432/<POSTGRES_DB>?sslmode=disable
#   2) make migrate (таблицы сборщика — миграция 0011), затем
#      deploy/harvester-db-role.sh
#   3) docker compose -f deploy/docker-compose.yml --profile harvester up -d harvester
#
# Скрипт идемпотентен: повторный запуск сводит права роли к этому списку
# и обновляет пароль. Новые таблицы сборщика (будущие миграции) — сюда же.
# Подключение — по сокету внутри контейнера postgres, как у make psql;
# окружение и контейнер — как у бэкапов (deploy/backup-lib.sh).

SCRIPT_NAME=harvester-db-role
# shellcheck source=SCRIPTDIR/backup-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/backup-lib.sh"

case ${1:-} in
    -h | --help) usage ;;
    "") ;;
    *) usage 2 ;;
esac
require_cmds docker

DSN="$(env_get HARVESTER_DATABASE_URL)"
[[ -n $DSN ]] || die "не задан HARVESTER_DATABASE_URL ($ENV_FILE)"
# Пароль — только символы, которые в URL не кодируются: его не нужно
# раскодировать, и в psql он попадает без экранирования
DSN_RE='^postgres(ql)?://([a-z_][a-z0-9_]{0,62}):([A-Za-z0-9._~-]{32,})@[^/@]+/([A-Za-z0-9_]+)([?].*)?$'
[[ $DSN =~ $DSN_RE ]] ||
    die "HARVESTER_DATABASE_URL: нужен postgres://<роль>:<пароль>@<хост>:<порт>/<база>, роль из [a-z0-9_]," \
        "пароль не короче 32 символов из [A-Za-z0-9._~-] (openssl rand -hex 32)"
ROLE=${BASH_REMATCH[2]}
PASSWORD=${BASH_REMATCH[3]}
DSN_DB=${BASH_REMATCH[4]}

pg_setup
[[ $ROLE != "$PG_USER" ]] || die "HARVESTER_DATABASE_URL указывает на владельца базы $PG_USER: нужна своя роль"
[[ $DSN_DB == "$PG_DB" ]] || die "HARVESTER_DATABASE_URL ведёт в базу $DSN_DB, а база стенда — $PG_DB"

log "роль $ROLE в базе $PG_DB (контейнер $PG_NAME)"
# Значения — psql-переменными на stdin: в аргументах docker exec пароль
# увидел бы любой процесс хоста
{
    printf '%s\n' "\\set role $ROLE" "\\set password '$PASSWORD'"
    cat <<'SQL'
SELECT format('CREATE ROLE %I LOGIN', :'role')
 WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'role') \gexec
ALTER ROLE :"role" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT
    NOREPLICATION NOBYPASSRLS PASSWORD :'password';
-- Права сводятся к списку ниже: выданное раньше сверх него снимается
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM :"role";
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM :"role";
REVOKE ALL ON SCHEMA public FROM :"role";
GRANT CONNECT ON DATABASE :"DBNAME" TO :"role";
GRANT USAGE ON SCHEMA public TO :"role";
GRANT SELECT, INSERT, UPDATE, DELETE
    ON subtitle_sources, videos, subtitle_segments, jobs, job_schedules TO :"role";
SQL
} | docker exec -i "$PG" psql -X -q -v ON_ERROR_STOP=1 -U "$PG_USER" -d "$PG_DB"
log "готово: пересоздай сервис harvester, чтобы он вошёл под $ROLE"
