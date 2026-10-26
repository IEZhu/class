#!/usr/bin/env bash
# Восстановление Postgres из бэкапа в хранилище (S1-7, ADR-014).
#
#   deploy/restore-pg.sh --list                       какие копии есть
#   deploy/restore-pg.sh --db БАЗА [--date ГГГГ-ММ-ДД | --key КЛЮЧ] [--force]
#                        [--insecure-no-verify]
#
# Без --date/--key берётся самая свежая ночная копия с датой не позже
# сегодняшней (UTC); в рабочую базу (POSTGRES_DB контейнера) — только
# с явной --date или --key. Копия скачивается и сверяется по sha256
# и размеру с манифестом проверенных выгрузок (BACKUP_MANIFEST,
# deploy/backup-lib.sh); манифеста на хосте нет — с sha256 из метаданных
# объекта, а это защита от порчи, не от подмены. Ключа нет в манифесте или
# sha256 нет вовсе — отказ; развернуть такую копию без сверки —
# --insecure-no-verify. Расхождение sha256 не обходится ничем.
# Копия разворачивается pg_restore одной транзакцией в базу БАЗА внутри
# контейнера Postgres; базы нет — создаётся. Непустая база перезаписывается
# только с --force (pg_restore --clean): прежде остановите api, web
# и worker. Владелец объектов — учётка контейнера (POSTGRES_USER), гранты
# из дампа не переносятся.
#
# Проверка копии: --db lingua_restore_check, после — DROP DATABASE.
# Окружение то же, что у backup-pg.sh (deploy/backup-lib.sh).

SCRIPT_NAME=restore-pg
# shellcheck source=SCRIPTDIR/backup-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/backup-lib.sh"

LIST=0
FORCE=0
INSECURE=0
DB=""
KEY=""
DATE=""
while (($#)); do
    case $1 in
        --list) LIST=1 ;;
        --force) FORCE=1 ;;
        --insecure-no-verify) INSECURE=1 ;;
        --db | --key | --date)
            (($# >= 2)) || die "$1: нужно значение"
            case $1 in
                --db) DB=$2 ;;
                --key) KEY=$2 ;;
                --date) DATE=$2 ;;
            esac
            shift
            ;;
        -h | --help) usage ;;
        *)
            log "неизвестный аргумент: $1"
            usage 2
            ;;
    esac
    shift
done

list_backups() {
    local out
    out="$(aws_cli s3api list-objects-v2 --bucket "$BUCKET" --prefix "$BACKUP_PREFIX" \
        --query 'Contents[].[Key, Size, LastModified]' --output text)"
    [[ $out == None ]] || printf '%s\n' "$out" | sort
}

# Ключ копии: явный, по дате или самый свежий ночной не позже сегодня.
# Копия «из будущего» (её писал не backup-pg.sh этого хоста) иначе
# побеждала бы при сортировке вечно
resolve_key() {
    local raw today key latest="" future=()
    if [[ -n $KEY && -n $DATE ]]; then
        die "--key и --date вместе не задаются"
    elif [[ -n $DATE ]]; then
        [[ $DATE =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "--date: нужен формат ГГГГ-ММ-ДД"
        KEY="${BACKUP_PREFIX}${DATE}.dump.zst"
    elif [[ -z $KEY ]]; then
        today="$(date -u +%F)"
        raw="$(list_keys)"
        while read -r key; do
            [[ $key =~ $NIGHTLY_KEY_RE ]] || continue
            if [[ ${BASH_REMATCH[1]} > $today ]]; then
                future+=("$key")
            else
                latest=$key
            fi
        done < <(sort <<<"$raw")
        if ((${#future[@]})); then
            log "ВНИМАНИЕ: пропускаю копии с датой позже $today: ${future[*]}"
        fi
        [[ -n $latest ]] || die "в s3://$BUCKET/$BACKUP_PREFIX нет ночных копий не позже $today"
        KEY=$latest
    fi
    [[ $KEY =~ $ANY_KEY_RE ]] || die "ключ должен быть вида ${BACKUP_PREFIX}<имя>.dump.zst: $KEY"
}

# Последняя запись манифеста для ключа: «<sha256> <размер>» или пусто
manifest_entry() {
    awk -v k="$1" '$1 == k { e = $2 " " $3 } END { print e }' "$MANIFEST"
}

# Без сверки — только когда сверять не с чем и оператор сам так решил
no_sha() {
    ((INSECURE)) || die "$1; развернуть без сверки — --insecure-no-verify"
    log "ВНИМАНИЕ: $1 — разворачиваю без сверки (--insecure-no-verify)"
}

download() {
    local dump=$1 sha size entry meta_sha dir
    log "скачиваю s3://$BUCKET/$KEY"
    aws_cli s3 cp "s3://$BUCKET/$KEY" - --only-show-errors >"$dump" || die "не скачивается s3://$BUCKET/$KEY"
    [[ -s $dump ]] || die "пустой объект $KEY"
    sha="$(sha256_of "$dump")"
    size="$(stat -c %s -- "$dump")"

    # Манифест — источник правды о том, что выгрузил этот хост; метаданные
    # объекта пишет тот же, кто пишет объект
    dir="$(manifest_dir)"
    [[ ! -d $dir || -x $dir ]] || die "нет доступа к $dir (манифест) — запускай от root"
    if [[ -e $MANIFEST ]]; then
        manifest_check
        [[ -r $MANIFEST ]] || die "манифест $MANIFEST не читается — запускай от root"
        entry="$(manifest_entry "$KEY")"
        if [[ -z $entry ]]; then
            no_sha "копии $KEY нет в манифесте $MANIFEST: её выгружал не этот хост или она подменена"
        elif [[ $entry != "$sha $size" ]]; then
            die "копия $KEY не совпадает с манифестом: sha256 и размер $sha $size, в манифесте $entry"
        else
            log "копия совпадает с манифестом $MANIFEST"
        fi
    else
        meta_sha="$(aws_cli s3api head-object --bucket "$BUCKET" --key "$KEY" \
            --query 'Metadata.sha256' --output text)"
        if [[ $meta_sha == None ]]; then
            no_sha "манифеста $MANIFEST нет, а у объекта нет sha256 в метаданных"
        elif [[ $meta_sha != "$sha" ]]; then
            die "sha256 скачанного $sha не совпадает с метаданными $meta_sha"
        else
            log "ВНИМАНИЕ: манифеста $MANIFEST нет — сверено с sha256 из метаданных объекта;" \
                "их пишет тот же, кто пишет объект: это защита от порчи, не от подмены"
        fi
    fi
    zstd -q -t -- "$dump"
    log "копия цела: sha256 $sha"
}

# psql в контейнере: SQL — со stdin, имя базы — переменной psql (:'db', :"db")
pg_sql() {
    docker exec -i "$PG" psql --no-password -X -q -At -v ON_ERROR_STOP=1 \
        -U "$PG_USER" -d "$1" -v db="$DB"
}

prepare_target() {
    local exists objects
    exists="$(pg_sql postgres <<<"SELECT count(*) FROM pg_database WHERE datname = :'db';")"
    if [[ $exists == 0 ]]; then
        log "базы $DB нет — создаю"
        pg_sql postgres <<<'CREATE DATABASE :"db";'
        return 0
    fi
    objects="$(pg_sql "$DB" <<<"SELECT count(*) FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
          AND n.nspname !~ '^pg_(toast|temp)';")"
    if [[ $objects != 0 ]]; then
        ((FORCE)) || die "база $DB не пуста (объектов: $objects); перезапись — только с --force"
        log "база $DB не пуста (объектов: $objects) — перезаписываю (--force)"
    fi
}

restore() {
    local dump=$1 plain="$WORK/restore.dump" clean=()
    if ((FORCE)); then
        clean=(--clean --if-exists)
    fi
    zstd -q -dc -- "$dump" >"$plain"
    # pg_restore читает файл, а не канал: дамп снят в поток, без смещений
    # блоков, и найти блок вне порядка оглавления pg_restore может только
    # в файле, по которому умеет перемещаться
    PG_TMP="/tmp/lingua-restore-$$.dump"
    docker cp -q "$plain" "$PG:$PG_TMP"
    log "pg_restore в $DB (контейнер $PG_NAME)"
    docker exec "$PG" pg_restore --no-password -U "$PG_USER" -d "$DB" \
        --single-transaction --exit-on-error --no-owner --no-privileges \
        "${clean[@]}" "$PG_TMP"
    log "восстановлено из $KEY в базу $DB; строк по таблицам:"
    pg_sql "$DB" <<<"SELECT format('%s.%s', table_schema, table_name),
               (xpath('/row/c/text()', query_to_xml(
                   format('SELECT count(*) AS c FROM %I.%I', table_schema, table_name),
                   false, true, '')))[1]::text::bigint
        FROM information_schema.tables
        WHERE table_type = 'BASE TABLE'
          AND table_schema NOT IN ('pg_catalog', 'information_schema')
        ORDER BY 1;"
}

require_cmds docker zstd sha256sum
s3_setup

if ((LIST)); then
    list_backups
    exit 0
fi

[[ -n $DB ]] || die "нужно --db БАЗА (куда восстанавливать)"
[[ $DB =~ ^[a-z_][a-z0-9_]{0,62}$ ]] || die "--db: только [a-z0-9_], с буквы или _"
case $DB in
    postgres | template0 | template1) die "--db $DB: служебная база" ;;
esac

work_setup
pg_setup
# Рабочая база — только с копией, которую оператор назвал сам
if [[ $DB == "$PG_DB" && -z $KEY && -z $DATE ]]; then
    die "--db $DB — рабочая база (POSTGRES_DB): копию укажи явно, --date или --key" \
        "(список — --list)"
fi
resolve_key
download "$WORK/dump.zst"
prepare_target
restore "$WORK/dump.zst"
