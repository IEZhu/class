#!/usr/bin/env bash
# Ночной бэкап Postgres в объектное хранилище (S1-7, ADR-014).
#
#   pg_dump (custom-формат) | zstd → s3://<бакет>/backups/pg/<дата UTC>.dump.zst
#
# Перед выгрузкой дамп проверяется (zstd -t, оглавление pg_restore --list),
# после — скачивается обратно и сверяется по sha256; затем чистка по
# retention 7/30. Любой сбой — ненулевой код выхода: systemd-юнит
# lingua-backup-pg.service уходит в failed. Повторный запуск в тот же день
# перезаписывает копию этого дня.
#
#   deploy/backup-pg.sh                        бэкап + чистка (так ходит таймер)
#   deploy/backup-pg.sh --prune-only [--dry-run]   только чистка / показать её
#
# Окружение — deploy/.env (BACKUP_S3_* или S3_*), подробности —
# deploy/backup-lib.sh и deploy/README.md. Нужны docker и zstd на хосте.

SCRIPT_NAME=backup-pg
# shellcheck source=SCRIPTDIR/backup-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/backup-lib.sh"

# Retention 7/30 (ADR-014): все копии последних 7 суток; старше — по одной
# на ISO-неделю (самая свежая в неделе, то есть обычно воскресная), пока ей
# не больше 30 суток; остальное удаляется.
DAILY_DAYS=7
KEEP_DAYS=30

PRUNE_ONLY=0
DRY_RUN=0
while (($#)); do
    case $1 in
        --prune-only) PRUNE_ONLY=1 ;;
        --dry-run) DRY_RUN=1 ;;
        -h | --help) usage ;;
        *)
            log "неизвестный аргумент: $1"
            usage 2
            ;;
    esac
    shift
done
if ((DRY_RUN && !PRUNE_ONLY)); then
    die "--dry-run работает только вместе с --prune-only"
fi

backup() {
    local key=$1 dump="$WORK/dump.zst" check="$WORK/check.zst" entries sha size remote_sha meta_sha

    pg_setup
    log "дамп базы $PG_DB из контейнера $PG_NAME"
    docker exec "$PG" pg_dump --no-password -U "$PG_USER" -d "$PG_DB" \
        --format=custom --compress=0 | zstd -q -T0 -c >"$dump" ||
        die "дамп не снят: коды pg_dump | zstd — ${PIPESTATUS[*]}"
    [[ -s $dump ]] || die "пустой дамп"
    zstd -q -t -- "$dump"
    # Оглавление читает сам pg_restore; хвост stdin дочитывается, чтобы
    # zstd не получил обрыв канала
    zstd -q -dc -- "$dump" |
        docker exec -i "$PG" sh -c 'pg_restore --list && cat >/dev/null' >"$WORK/toc" ||
        die "pg_restore не читает оглавление дампа"
    entries="$(grep -c '^[0-9]' "$WORK/toc" || true)"
    ((entries > 0)) || die "в оглавлении дампа нет объектов"

    sha="$(sha256_of "$dump")"
    size="$(stat -c %s -- "$dump")"
    log "выгрузка s3://$BUCKET/$key: $size байт, объектов в оглавлении $entries, sha256 $sha"
    aws_cli_stdin s3 cp - "s3://$BUCKET/$key" --only-show-errors \
        --expected-size "$size" --content-type application/zstd \
        --metadata "sha256=$sha" <"$dump" || die "выгрузка в s3://$BUCKET/$key не удалась"

    # Проверка выгруженного: объект скачивается обратно и совпадает байт
    # в байт; sha256 в метаданных нужен restore-pg.sh
    aws_cli s3 cp "s3://$BUCKET/$key" - --only-show-errors >"$check" ||
        die "выгруженный объект не скачивается обратно"
    remote_sha="$(sha256_of "$check")"
    [[ $remote_sha == "$sha" ]] || die "объект в хранилище не совпадает с дампом (sha256 $remote_sha)"
    meta_sha="$(aws_cli s3api head-object --bucket "$BUCKET" --key "$key" \
        --query 'Metadata.sha256' --output text)"
    [[ $meta_sha == "$sha" ]] || die "в метаданных объекта sha256 '$meta_sha', ожидался $sha"
    log "бэкап проверен: s3://$BUCKET/$key"
}

prune() {
    local today=$1 today_s raw keys=() key d ts age week fresh=0 delete=()
    local -A week_seen=()
    today_s="$(date -u -d "$today" +%s)"

    # Список — отдельным присваиванием: сбой хранилища роняет скрипт,
    # а в подстановке процесса он прошёл бы незамеченным
    raw="$(list_keys)"
    # Свежие — первыми: первая встреченная копия недели и есть самая свежая
    mapfile -t keys < <(grep -E "$NIGHTLY_KEY_RE" <<<"$raw" | sort -r || true)

    for key in "${keys[@]}"; do
        [[ $key =~ $NIGHTLY_KEY_RE ]]
        d=${BASH_REMATCH[1]}
        if ! ts="$(date -u -d "$d" +%s 2>/dev/null)"; then
            log "пропуск $key: в имени не дата"
            continue
        fi
        age=$(((today_s - ts) / 86400))
        week="$(date -u -d "$d" +%G-W%V)"
        if ((age < DAILY_DAYS)); then
            log "оставить $key: дневная, $age сут."
            ((age < 0)) || fresh=1
        elif ((age <= KEEP_DAYS)) && [[ -z ${week_seen[$week]:-} ]]; then
            log "оставить $key: недельная ($week), $age сут."
        else
            log "удалить  $key: $age сут."
            delete+=("$key")
        fi
        week_seen[$week]=1
    done

    # Без свежей копии чистка могла бы стереть последние рабочие —
    # бэкапы, видимо, не делаются; громко падаем и ничего не трогаем
    ((fresh)) || die "нет копии моложе $DAILY_DAYS сут. — чистку не делаю"

    if ((DRY_RUN)); then
        log "dry-run: удалилось бы ${#delete[@]} из ${#keys[@]}"
        return 0
    fi
    for key in "${delete[@]}"; do
        aws_cli s3api delete-object --bucket "$BUCKET" --key "$key" >/dev/null
    done
    log "чистка: удалено ${#delete[@]}, осталось $((${#keys[@]} - ${#delete[@]}))"
}

require_cmds docker zstd sha256sum
s3_setup
work_setup
TODAY="$(date -u +%F)"

if ((!PRUNE_ONLY)); then
    backup "${BACKUP_PREFIX}${TODAY}.dump.zst"
fi
prune "$TODAY"
