# shellcheck shell=bash
# Общая часть deploy/backup-pg.sh и deploy/restore-pg.sh (S1-7, ADR-014):
# окружение, aws cli, контейнер Postgres, временный каталог. Подключается
# через source, отдельно не запускается.
#
# Переменные процесса (в deploy/.env их не пишут — это для эксплуатации
# и проверки на одноразовом стенде):
#   BACKUP_ENV_FILE        файл окружения, по умолчанию deploy/.env
#   BACKUP_PG_CONTAINER    контейнер Postgres; по умолчанию — сервис postgres
#                          compose-проекта (docker compose ps -q postgres)
#   BACKUP_AWS_IMAGE       образ aws cli (по умолчанию закреплён digest'ом)
#   BACKUP_DOCKER_NETWORK  docker-сеть для контейнера aws cli — нужна, только
#                          если S3-совместимое хранилище само живёт в docker

set -Eeuo pipefail
# Без этого ошибка внутри $(…) не роняет скрипт
shopt -s inherit_errexit

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${BACKUP_ENV_FILE:-$DEPLOY_DIR/.env}"

# Раскладка 05-storage-s3.md: backups/pg/{date}.dump.zst, дата — UTC
BACKUP_PREFIX="backups/pg/"
NIGHTLY_KEY_RE='^backups/pg/([0-9]{4}-[0-9]{2}-[0-9]{2})\.dump\.zst$'
# Ручная копия (напр. перед миграцией) — любое имя под тем же префиксом;
# чистка такие не трогает, восстановить из них можно
ANY_KEY_RE='^backups/pg/[A-Za-z0-9._-]+\.dump\.zst$'

# aws cli на хосте не ставим: одноразовый контейнер из образа, которым уже
# работали с R2 стенда (S1-3). Тег для читателя, digest — для закрепления.
AWS_IMAGE="${BACKUP_AWS_IMAGE:-amazon/aws-cli:2.32.7@sha256:2fcb4059b4f87a42cfe28a674bee119c0c484f7bdc67f1643f9dda0200134d1e}"

log() { printf '%s %s: %s\n' "$(date -u +%FT%TZ)" "$SCRIPT_NAME" "$*" >&2; }
die() {
    log "ОШИБКА: $*"
    exit 1
}

# Последняя упавшая команда — текст без подстановки переменных, то есть
# без значений секретов; печатается при выходе с ошибкой
FAILED_AT=""
trap 'FAILED_AT="${BASH_SOURCE[0]##*/}:$LINENO: $BASH_COMMAND"' ERR

WORK=""
PG=""
PG_TMP=""
on_exit() {
    local rc=$?
    if [[ -n $PG_TMP ]]; then
        docker exec "$PG" rm -f -- "$PG_TMP" >/dev/null 2>&1 || true
    fi
    if [[ -n $WORK ]]; then
        rm -rf -- "$WORK"
    fi
    if ((rc != 0)); then
        log "ОШИБКА: завершение с кодом $rc${FAILED_AT:+ ($FAILED_AT)}"
    fi
    exit "$rc"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Справка — шапка-комментарий запущенного скрипта
usage() {
    awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
    exit "${1:-0}"
}

require_cmds() {
    local c
    for c in "$@"; do
        command -v "$c" >/dev/null || die "нет команды $c на хосте"
    done
}

# Дамп содержит хэши паролей и email: всё временное — только владельцу
work_setup() {
    umask 077
    WORK="$(mktemp -d "${TMPDIR:-/tmp}/lingua-pg-backup.XXXXXX")"
}

# Значение ключа: окружение процесса важнее файла — как у docker compose.
# .env не исполняется (source выполнил бы любую строку), а читается как
# KEY=VALUE; парные кавычки вокруг значения снимаются, последняя строка
# с ключом побеждает.
env_get() {
    local key=$1 line val=""
    if [[ -n ${!key:-} ]]; then
        printf '%s' "${!key}"
        return 0
    fi
    [[ -r $ENV_FILE ]] || return 0
    while IFS= read -r line || [[ -n $line ]]; do
        line=${line%$'\r'}
        if [[ $line == "$key="* ]]; then
            val=${line#*=}
        fi
    done <"$ENV_FILE"
    if ((${#val} >= 2)) && [[ ($val == \"*\") || ($val == \'*\') ]]; then
        val=${val:1:${#val}-2}
    fi
    printf '%s' "$val"
}

# Хранилище бэкапов: BACKUP_S3_* перекрывают S3_* по одному ключу, а пара
# ключей доступа берётся только целиком — смешать ID одного токена с секретом
# другого нельзя. Ключи уходят в окружение для aws cli, на экран — никогда.
s3_setup() {
    local key_id secret
    BUCKET="$(env_get BACKUP_S3_BUCKET)"
    [[ -n $BUCKET ]] || BUCKET="$(env_get S3_BUCKET)"
    ENDPOINT="$(env_get BACKUP_S3_ENDPOINT)"
    [[ -n $ENDPOINT ]] || ENDPOINT="$(env_get S3_ENDPOINT)"
    REGION="$(env_get BACKUP_S3_REGION)"
    [[ -n $REGION ]] || REGION="$(env_get S3_REGION)"
    REGION="${REGION:-auto}"

    key_id="$(env_get BACKUP_S3_ACCESS_KEY_ID)"
    secret="$(env_get BACKUP_S3_SECRET_ACCESS_KEY)"
    if [[ -n $key_id || -n $secret ]]; then
        [[ -n $key_id && -n $secret ]] ||
            die "BACKUP_S3_ACCESS_KEY_ID и BACKUP_S3_SECRET_ACCESS_KEY задаются только парой"
    else
        key_id="$(env_get S3_ACCESS_KEY_ID)"
        secret="$(env_get S3_SECRET_ACCESS_KEY)"
    fi

    [[ -n $BUCKET ]] || die "не задан бакет: BACKUP_S3_BUCKET или S3_BUCKET ($ENV_FILE)"
    [[ -n $key_id && -n $secret ]] ||
        die "не заданы ключи доступа: BACKUP_S3_* или S3_* ($ENV_FILE)"
    if [[ "$BUCKET$ENDPOINT$REGION$key_id$secret" == *change-me* ]]; then
        die "в окружении шаблонные значения change-me — заполни $ENV_FILE"
    fi
    [[ $BUCKET =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || die "недопустимое имя бакета: $BUCKET"
    [[ -z $ENDPOINT || $ENDPOINT =~ ^https?://[^[:space:]]+$ ]] || die "S3-эндпоинт должен быть URL http(s)://"

    if [[ $key_id == "$(env_get S3_ACCESS_KEY_ID)" ]]; then
        log "ВНИМАНИЕ: бэкап под общим токеном S3_* — его же получает LiveKit Egress," \
            "и он читает и удаляет дампы; нужен отдельный BACKUP_S3_* (ADR-014)"
    fi

    export AWS_ACCESS_KEY_ID="$key_id" AWS_SECRET_ACCESS_KEY="$secret" AWS_DEFAULT_REGION="$REGION"
    AWS_RUN=(run --rm
        -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION
        -e AWS_EC2_METADATA_DISABLED=true)
    if [[ -n ${BACKUP_DOCKER_NETWORK:-} ]]; then
        AWS_RUN+=(--network "$BACKUP_DOCKER_NETWORK")
    fi
    AWS_ENDPOINT_ARGS=()
    if [[ -n $ENDPOINT ]]; then
        AWS_ENDPOINT_ARGS=(--endpoint-url "$ENDPOINT")
    fi
    log "хранилище: s3://$BUCKET/$BACKUP_PREFIX${ENDPOINT:+ ($ENDPOINT)}"
}

# aws cli в одноразовом контейнере. Ключи передаются как `-e ИМЯ` без
# значения — docker берёт их из окружения, поэтому в командной строке
# (ps, журнал) секретов нет. Файлов хоста контейнер не видит: данные
# идут через stdin/stdout. stdin закрыт, чтобы контейнер не съел ввод
# циклов `while read`; для выгрузки — aws_cli_stdin.
aws_cli() { docker "${AWS_RUN[@]}" "$AWS_IMAGE" "${AWS_ENDPOINT_ARGS[@]}" "$@" </dev/null; }
aws_cli_stdin() { docker "${AWS_RUN[@]}" -i "$AWS_IMAGE" "${AWS_ENDPOINT_ARGS[@]}" "$@"; }

# Ключи объектов под префиксом бэкапов, по одному на строку (без фильтра)
list_keys() {
    local out
    out="$(aws_cli s3api list-objects-v2 --bucket "$BUCKET" --prefix "$BACKUP_PREFIX" \
        --query 'Contents[].Key' --output text)"
    # Пустой префикс aws cli печатает как None
    [[ $out == None ]] || printf '%s\n' "$out" | tr '\t' '\n'
}

# Контейнер Postgres и его учётка. Подключение — по локальному сокету
# внутри контейнера (как у make psql): пароль БД скриптам не нужен.
pg_setup() {
    if [[ -n ${BACKUP_PG_CONTAINER:-} ]]; then
        PG="$BACKUP_PG_CONTAINER"
    else
        PG="$(docker compose -f "$DEPLOY_DIR/docker-compose.yml" ps -q postgres)"
    fi
    [[ -n $PG ]] || die "контейнер postgres не запущен (docker compose ps -q postgres пуст)"
    [[ "$(docker inspect -f '{{.State.Running}}' "$PG")" == true ]] || die "контейнер $PG не запущен"
    PG_NAME="$(docker inspect -f '{{.Name}}' "$PG")"
    PG_NAME=${PG_NAME#/}
    PG_USER="$(docker exec "$PG" printenv POSTGRES_USER)"
    PG_DB="$(docker exec "$PG" printenv POSTGRES_DB)"
    [[ -n $PG_USER && -n $PG_DB ]] || die "в контейнере $PG нет POSTGRES_USER/POSTGRES_DB"
}

sha256_of() { sha256sum -- "$1" | cut -d' ' -f1; }
