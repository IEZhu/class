#!/usr/bin/env bash
# Ночной бэкап Postgres в объектное хранилище (S1-7, ADR-014).
#
#   pg_dump (custom-формат) | zstd → s3://<бакет>/backups/pg/<дата UTC>.dump.zst
#
# Перед выгрузкой дамп проверяется (zstd -t, оглавление pg_restore --list).
# Выгружается он во временный backups/pg/incoming/<дата>.<pid>.dump.zst,
# скачивается обратно и сверяется по sha256; только проверенный объект
# копируется на стороне хранилища в ночной ключ, ключ с sha256 и размером
# дописывается в манифест на VPS (BACKUP_MANIFEST, deploy/backup-lib.sh),
# временный удаляется. Затем чистка по retention 7/30. Любой сбой —
# ненулевой код выхода: systemd-юнит lingua-backup-pg.service уходит
# в failed, а непроверенная выгрузка ночной ключ не трогает. Повторный
# запуск в тот же день заменяет копию дня проверенной. Ночные ключи с датой
# позже сегодняшней чистка не трогает, но называет и завершается с ошибкой.
#
#   deploy/backup-pg.sh                        бэкап + чистка (так ходит таймер)
#   deploy/backup-pg.sh --prune-only [--dry-run]   только чистка / показать её
#
# Окружение — deploy/.env (BACKUP_S3_* или S3_*), подробности —
# deploy/backup-lib.sh и deploy/README.md. Нужны docker и zstd на хосте.

SCRIPT_NAME=backup-pg
# shellcheck source=SCRIPTDIR/backup-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/backup-lib.sh"

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
    local day=$1 key staging dump="$WORK/dump.zst" check="$WORK/check.zst"
    local entries sha size remote_sha meta_sha final
    key="${BACKUP_PREFIX}${day}.dump.zst"
    staging="${INCOMING_PREFIX}${day}.$$.dump.zst"

    # Манифест с чужими правами — отказ до дампа, а не после выгрузки
    manifest_check
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
    log "выгрузка s3://$BUCKET/$staging: $size байт, объектов в оглавлении $entries, sha256 $sha"
    # С этого момента временный объект удаляет on_exit при любом сбое
    S3_TMP_KEY=$staging
    aws_cli_stdin s3 cp - "s3://$BUCKET/$staging" --only-show-errors \
        --expected-size "$size" --content-type application/zstd \
        --metadata "sha256=$sha" <"$dump" || die "выгрузка в s3://$BUCKET/$staging не удалась"

    # Проверка выгруженного: объект скачивается обратно и совпадает байт
    # в байт; sha256 в метаданных — для восстановления без манифеста
    aws_cli s3 cp "s3://$BUCKET/$staging" - --only-show-errors >"$check" ||
        die "выгруженный объект не скачивается обратно"
    remote_sha="$(sha256_of "$check")"
    [[ $remote_sha == "$sha" ]] || die "объект в хранилище не совпадает с дампом (sha256 $remote_sha)"
    meta_sha="$(aws_cli s3api head-object --bucket "$BUCKET" --key "$staging" \
        --query 'Metadata.sha256' --output text)"
    [[ $meta_sha == "$sha" ]] || die "в метаданных объекта sha256 '$meta_sha', ожидался $sha"

    # Перенос в ночной ключ — копированием внутри хранилища (метаданные
    # и тип едут с объектом); копия дня меняется только на проверенную
    aws_cli s3api copy-object --bucket "$BUCKET" --copy-source "$BUCKET/$staging" \
        --key "$key" >/dev/null || die "проверенная копия не перенесена в s3://$BUCKET/$key"
    final="$(aws_cli s3api head-object --bucket "$BUCKET" --key "$key" \
        --query '[ContentLength, Metadata.sha256]' --output text)"
    [[ $final == "$size"$'\t'"$sha" ]] ||
        die "s3://$BUCKET/$key после переноса: размер и sha256 '$final', ожидались $size $sha"
    manifest_add "$key" "$sha" "$size"
    log "бэкап проверен: s3://$BUCKET/$key, записан в манифест $MANIFEST"

    if aws_cli s3api delete-object --bucket "$BUCKET" --key "$staging" >/dev/null; then
        S3_TMP_KEY=""
    fi
}

prune() {
    local today=$1 raw plan verdict key kept=0 delete=() future=()

    # Список — отдельным присваиванием: сбой хранилища роняет скрипт,
    # а в подстановке процесса он прошёл бы незамеченным
    raw="$(list_keys)"
    # Без свежей копии чистка могла бы стереть последние рабочие —
    # громко падаем и ничего не трогаем
    plan="$(retention_plan "$today" <<<"$raw")" ||
        die "нет копии моложе $DAILY_DAYS сут. — чистку не делаю"
    while read -r verdict key; do
        case $verdict in
            keep) kept=$((kept + 1)) ;;
            delete) delete+=("$key") ;;
            future) future+=("$key") ;;
        esac
    done <<<"$plan"

    if ((DRY_RUN)); then
        log "dry-run: удалилось бы ${#delete[@]}, осталось бы $kept"
    else
        for key in "${delete[@]}"; do
            aws_cli s3api delete-object --bucket "$BUCKET" --key "$key" >/dev/null
        done
        log "чистка: удалено ${#delete[@]}, осталось $kept"
    fi

    # Копия «из будущего» вечно считалась бы свежей и подставлялась бы
    # при восстановлении: её не трогаем, но прогон помечаем упавшим
    if ((${#future[@]})); then
        die "ночные копии с датой позже $today: ${future[*]} — их писал не этот" \
            "скрипт или сбиты часы; restore их не выбирает, разберись и удали вручную"
    fi
}

require_cmds docker zstd sha256sum
s3_setup
work_setup
TODAY="$(date -u +%F)"

if ((!PRUNE_ONLY)); then
    backup "$TODAY"
fi
prune "$TODAY"
