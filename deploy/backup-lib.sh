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
#   BACKUP_MANIFEST        манифест проверенных выгрузок, по умолчанию
#                          /var/lib/lingua-backup/manifest (см. manifest_add)

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
# Выгрузка идёт сюда и переносится в ночной ключ только после проверки;
# под ANY_KEY_RE этот подкаталог не подходит — ни чистка, ни restore его
# не берут
INCOMING_PREFIX="${BACKUP_PREFIX}incoming/"

# Retention 7/30 (ADR-014): все копии последних 7 суток; старше — по одной
# на ISO-неделю (самая свежая в неделе, то есть обычно воскресная), пока ей
# не больше 30 суток; остальное удаляется.
DAILY_DAYS=7
KEEP_DAYS=30

MANIFEST="${BACKUP_MANIFEST:-/var/lib/lingua-backup/manifest}"

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
# Временный объект выгрузки: при любом выходе до переноса — удаляется
S3_TMP_KEY=""
on_exit() {
    local rc=$?
    if [[ -n $PG_TMP ]]; then
        docker exec "$PG" rm -f -- "$PG_TMP" >/dev/null 2>&1 || true
    fi
    if [[ -n $S3_TMP_KEY ]]; then
        aws_cli s3api delete-object --bucket "$BUCKET" --key "$S3_TMP_KEY" >/dev/null 2>&1 ||
            log "ВНИМАНИЕ: не удалён временный объект s3://$BUCKET/$S3_TMP_KEY — удали вручную"
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

# Хранилище бэкапов. Все BACKUP_S3_* пусты — общий режим: бакет записей
# и токен S3_* с предупреждением (долг D-8). Задан хоть один — нужен свой
# BACKUP_S3_BUCKET: иначе дампы молча ушли бы в бакет записей, который
# читает токен LiveKit. Эндпоинт и регион, если пусты, берутся из S3_*;
# пара ключей доступа — только целиком, пустая — из S3_*: смешать ID одного
# токена с секретом другого нельзя. Ключи уходят в окружение для aws cli,
# на экран — никогда.
s3_setup() {
    local key_id secret name override=0 s3_endpoint
    for name in BUCKET REGION ENDPOINT ACCESS_KEY_ID SECRET_ACCESS_KEY; do
        [[ -z "$(env_get "BACKUP_S3_$name")" ]] || override=1
    done
    BUCKET="$(env_get BACKUP_S3_BUCKET)"
    if ((override)); then
        [[ -n $BUCKET ]] ||
            die "задан BACKUP_S3_*, но не BACKUP_S3_BUCKET: дампы ушли бы в бакет записей" \
                "S3_BUCKET, который читает токен LiveKit; укажи отдельный бакет (ADR-014)"
    else
        BUCKET="$(env_get S3_BUCKET)"
    fi
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

    # Общее место — тот же бакет на том же эндпоинте, какими бы ключами
    # туда ни ходили: токен S3_* у LiveKit видит всё в бакете
    s3_endpoint="$(env_get S3_ENDPOINT)"
    if [[ $BUCKET == "$(env_get S3_BUCKET)" && ${ENDPOINT%/} == "${s3_endpoint%/}" ]]; then
        ((!override)) ||
            die "BACKUP_S3_BUCKET — это бакет записей S3_BUCKET на том же эндпоинте:" \
                "токен S3_* у LiveKit читает и удаляет дампы; нужен отдельный бакет" \
                "или пустые BACKUP_S3_* (общий режим, долг D-8)"
        log "ВНИМАНИЕ: BACKUP_S3_* пусты — бэкап в бакете записей под общим токеном S3_*;" \
            "его же получает LiveKit Egress, и он читает и удаляет дампы; нужны свои" \
            "бакет и токен BACKUP_S3_* (ADR-014, долг D-8)"
    elif [[ $key_id == "$(env_get S3_ACCESS_KEY_ID)" ]]; then
        log "ВНИМАНИЕ: бэкап под общим токеном S3_* — его же получает LiveKit Egress," \
            "и он читает и удаляет дампы; нужны свои BACKUP_S3_ACCESS_KEY_ID/SECRET (ADR-014)"
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

# Решение retention без хранилища и часов — его проверяет
# scripts/test_backup_retention.py. Аргумент — «сегодня» (ГГГГ-ММ-ДД, UTC),
# на stdin — ключи по одному на строку; не ночные (ручные копии, incoming/,
# чужие префиксы) пропускаются. На stdout — по строке на ночную копию:
#   keep <ключ> | delete <ключ> | future <ключ>
# future — дата позже «сегодня»: такую копию писал не этот скрипт (или сбиты
# часы); её не удаляют, но и в retention не считают. Пояснения — в журнал.
# Нет копии моложе DAILY_DAYS суток — код 3 и пустой stdout: бэкапы, видимо,
# не делаются, и чистка стёрла бы последние рабочие.
retention_plan() {
    local today=$1 today_s raw keys=() key d ts age week fresh=0 plan=()
    local -A week_seen=()
    [[ $today =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || return 2
    today_s="$(date -u -d "$today" +%s)" || return 2
    raw="$(cat)"
    # Свежие — первыми: первая встреченная копия недели и есть самая свежая
    mapfile -t keys < <(grep -E "$NIGHTLY_KEY_RE" <<<"$raw" | sort -r || true)

    for key in "${keys[@]}"; do
        [[ $key =~ $NIGHTLY_KEY_RE ]]
        d=${BASH_REMATCH[1]}
        if ! ts="$(date -u -d "$d" +%s 2>/dev/null)"; then
            log "пропуск $key: в имени не дата"
            continue
        fi
        if [[ $d > $today ]]; then
            log "ВНИМАНИЕ: $key — дата позже сегодняшней ($today)"
            plan+=("future $key")
            continue
        fi
        age=$(((today_s - ts) / 86400))
        week="$(date -u -d "$d" +%G-W%V)"
        if ((age < DAILY_DAYS)); then
            log "оставить $key: дневная, $age сут."
            plan+=("keep $key")
            fresh=1
        elif ((age <= KEEP_DAYS)) && [[ -z ${week_seen[$week]:-} ]]; then
            log "оставить $key: недельная ($week), $age сут."
            plan+=("keep $key")
        else
            log "удалить  $key: $age сут."
            plan+=("delete $key")
        fi
        week_seen[$week]=1
    done

    if ((!fresh)); then
        log "нет копии моложе $DAILY_DAYS сут."
        return 3
    fi
    if ((${#plan[@]})); then
        printf '%s\n' "${plan[@]}"
    fi
}

# Манифест проверенных выгрузок — строка «<ключ> <sha256> <размер>» на каждую
# копию, которую backup-pg.sh выгрузил и сверил. Лежит на VPS вне бакета
# и доступен только root: держатель токена хранилища может подменить объект
# вместе с его метаданными, но не манифест. restore-pg.sh сверяет копию
# с последней строкой её ключа. Растёт на строку в сутки.
manifest_dir() { dirname -- "$MANIFEST"; }

# Манифесту, который может править кто-то кроме нас, верить нельзя:
# владелец файла и каталога — пользователь скрипта, запись — только ему
manifest_check() {
    local path owner mode
    for path in "$(manifest_dir)" "$MANIFEST"; do
        [[ -e $path ]] || continue
        read -r owner mode <<<"$(stat -c '%u %a' -- "$path")"
        if [[ $owner != "$EUID" ]] || (((8#$mode & 8#022) != 0)); then
            die "$path: владелец uid $owner, права $mode — манифесту нужен владелец" \
                "uid $EUID и запись только ему (chown root, chmod go-w)"
        fi
    done
}

manifest_add() {
    local dir
    dir="$(manifest_dir)"
    [[ -d $dir ]] || mkdir -p -m 0700 -- "$dir"
    manifest_check
    (
        umask 077
        printf '%s %s %s\n' "$1" "$2" "$3" >>"$MANIFEST"
    )
}
