#!/usr/bin/env bash
# Настройка бакета для записей уроков (S1-3) и файлов материалов (D-4):
# CORS и lifecycle.
# Провайдер — Cloudflare R2 (ADR-009), API S3-совместимый, поэтому работаем
# обычным aws cli с --endpoint-url. Раскладка и политика хранения —
# docs/architecture/05-storage-s3.md.
#
# Токены R2 создаются в кабинете Cloudflare, S3 API их не выдаёт. Нужны два:
#   1) временный Admin Read & Write — под ним запускается этот скрипт
#      (бакетная конфигурация рабочему ключу недоступна, и это правильно);
#   2) постоянный Object Read & Write, ограниченный одним бакетом — он идёт
#      в deploy/.env и в конфиг LiveKit Egress.
#
#   ACCOUNT_ID=<id> BUCKET=lingua-class \
#   AWS_ACCESS_KEY_ID=<admin-key> AWS_SECRET_ACCESS_KEY=<admin-secret> \
#     ./deploy/s3-bootstrap.sh
#
# Скрипт идемпотентен: повторный запуск перезаписывает конфигурацию тем же.

set -euo pipefail

BUCKET="${BUCKET:?нужно имя бакета: BUCKET=lingua-class ...}"
ACCOUNT_ID="${ACCOUNT_ID:?нужен Account ID из кабинета Cloudflare (R2 → Overview)}"
ORIGIN="${ORIGIN:-https://lang.wondermr.com}"
# У R2 регион всегда auto; пустое значение и us-east-1 алиасятся в него
REGION="${REGION:-auto}"
ENDPOINT="${ENDPOINT:-https://${ACCOUNT_ID}.r2.cloudflarestorage.com}"
# Через сколько дней записи уходят в дешёвый класс
IA_AFTER_DAYS="${IA_AFTER_DAYS:-30}"

command -v aws >/dev/null || { echo "нет aws cli: https://aws.amazon.com/cli/"; exit 1; }
: "${AWS_ACCESS_KEY_ID:?нужен ключ токена Admin Read & Write}"
: "${AWS_SECRET_ACCESS_KEY:?нужен секрет токена Admin Read & Write}"

r2() { aws s3api --endpoint-url "$ENDPOINT" --region "$REGION" "$@"; }
say() { printf '\n=== %s\n' "$1"; }

echo "endpoint: $ENDPOINT"
echo "бакет:    $BUCKET"

say "1/4 бакет"
if r2 head-bucket --bucket "$BUCKET" 2>/dev/null; then
    echo "уже существует — пропускаю"
else
    r2 create-bucket --bucket "$BUCKET"
fi

say "2/4 CORS: GET/HEAD и PUT, только с домена платформы"
# GET/HEAD нужен плееру: запись отдаётся браузеру по presigned GET напрямую
# из R2, минуя VPS. ExposeHeaders — чтобы работала перемотка по Range.
# PUT — загрузка файлов материалов браузером по presigned PUT (ADR-024):
# ровно те два заголовка, что api вносит в подпись; длину браузер ставит
# сам. Правило PUT появилось с долгом D-4 — бакет, настроенный раньше,
# надо перенастроить повторным запуском скрипта (долг D-14).
r2 put-bucket-cors --bucket "$BUCKET" --cors-configuration "{
  \"CORSRules\": [{
    \"AllowedOrigins\": [\"$ORIGIN\"],
    \"AllowedMethods\": [\"GET\", \"HEAD\"],
    \"AllowedHeaders\": [\"*\"],
    \"ExposeHeaders\": [\"Content-Length\", \"Content-Range\", \"Accept-Ranges\", \"ETag\"],
    \"MaxAgeSeconds\": 3000
  }, {
    \"AllowedOrigins\": [\"$ORIGIN\"],
    \"AllowedMethods\": [\"PUT\"],
    \"AllowedHeaders\": [\"Content-Type\", \"Content-Disposition\"],
    \"MaxAgeSeconds\": 3000
  }]
}"

say "3/4 lifecycle: записи дешевеют, транскрипты не трогаем"
# У R2 только Standard и Infrequent Access, архивного класса нет (ADR-009).
# Удаление старого видео с сохранением аудио и VTT — отдельное решение,
# когда накопится статистика просмотров; сейчас только переход в IA.
# Бэкапы Postgres (backups/pg/, S1-7) правилом не покрыты намеренно:
# «одну копию в неделю после 7 суток» lifecycle не выразить, а истечение
# по возрасту стёрло бы последние копии, если бэкапы перестанут делаться.
# Чистит сам deploy/backup-pg.sh (ADR-014); в IA они тоже не уходят.
r2 put-bucket-lifecycle-configuration --bucket "$BUCKET" --lifecycle-configuration "{
  \"Rules\": [
    {
      \"ID\": \"recordings-to-ia\",
      \"Status\": \"Enabled\",
      \"Filter\": {\"Prefix\": \"recordings/\"},
      \"Transitions\": [{\"Days\": $IA_AFTER_DAYS, \"StorageClass\": \"STANDARD_IA\"}]
    },
    {
      \"ID\": \"abort-incomplete-uploads\",
      \"Status\": \"Enabled\",
      \"Filter\": {\"Prefix\": \"\"},
      \"AbortIncompleteMultipartUpload\": {\"DaysAfterInitiation\": 7}
    }
  ]
}"

say "4/4 проверка"
r2 get-bucket-cors --bucket "$BUCKET" >/dev/null && echo "CORS на месте"
r2 get-bucket-lifecycle-configuration --bucket "$BUCKET" >/dev/null && echo "lifecycle на месте"

cat <<EOF

Бакет настроен. Дальше в кабинете Cloudflare создайте второй токен —
Object Read & Write, scope: только бакет $BUCKET — и впишите в deploy/.env:

S3_BUCKET=$BUCKET
S3_REGION=auto
S3_ENDPOINT=$ENDPOINT
S3_ACCESS_KEY_ID=<Access Key ID того токена>
S3_SECRET_ACCESS_KEY=<Secret Access Key, показывается один раз>

Проверить рабочий токен:
  AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… \\
    aws s3 ls "s3://$BUCKET" --endpoint-url $ENDPOINT --region auto

Бэкапам Postgres нужен отдельный бакет и свой токен (ADR-014,
BACKUP_S3_* в deploy/.env) — см. deploy/README.md.

Временный Admin-токен после этого удалите — он больше не нужен.
EOF
