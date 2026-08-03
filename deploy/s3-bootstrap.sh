#!/usr/bin/env bash
# Заведение S3 для записей уроков (S1-3): приватный бакет, шифрование, CORS,
# lifecycle и IAM-пользователь с доступом только к этому бакету.
#
# Раскладка бакета и политика хранения — docs/architecture/05-storage-s3.md.
# Запускать один раз, с правами администратора AWS. Скрипт идемпотентен:
# уже созданное пропускается, повторный запуск ничего не ломает.
#
#   BUCKET=lingua-class REGION=eu-central-1 ./deploy/s3-bootstrap.sh
#
# Ключ выдаётся в конце — он же уходит в LiveKit Egress, поэтому политика
# ограничена одним бакетом: скомпрометированный ключ не даёт ничего больше.

set -euo pipefail

BUCKET="${BUCKET:?нужно имя бакета: BUCKET=lingua-class ./deploy/s3-bootstrap.sh}"
REGION="${REGION:-eu-central-1}"
IAM_USER="${IAM_USER:-lingua-class-s3}"
POLICY_NAME="${POLICY_NAME:-lingua-class-s3}"
# Домен платформы: под него открывается CORS на чтение
ORIGIN="${ORIGIN:-https://lang.wondermr.com}"

command -v aws >/dev/null || { echo "нет aws cli: https://aws.amazon.com/cli/"; exit 1; }
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
echo "аккаунт AWS: $ACCOUNT, регион: $REGION, бакет: $BUCKET"

say() { printf '\n=== %s\n' "$1"; }

say "1/6 бакет"
if aws s3api head-bucket --bucket "$BUCKET" 2>/dev/null; then
    echo "уже существует — пропускаю"
elif [ "$REGION" = "us-east-1" ]; then
    # us-east-1 — единственный регион, который не принимает LocationConstraint
    aws s3api create-bucket --bucket "$BUCKET" --region us-east-1
else
    aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
        --create-bucket-configuration "LocationConstraint=$REGION"
fi

say "2/6 публичный доступ закрыт"
aws s3api put-public-access-block --bucket "$BUCKET" \
    --public-access-block-configuration \
    "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"

say "3/6 шифрование по умолчанию (SSE-S3)"
aws s3api put-bucket-encryption --bucket "$BUCKET" --server-side-encryption-configuration '{
  "Rules": [{
    "ApplyServerSideEncryptionByDefault": {"SSEAlgorithm": "AES256"},
    "BucketKeyEnabled": true
  }]
}'

say "4/6 CORS: только GET и только с домена платформы"
# Нужен плееру: запись отдаётся браузеру по presigned GET напрямую из S3,
# минуя VPS. ExposeHeaders — чтобы работала перемотка по Range.
aws s3api put-bucket-cors --bucket "$BUCKET" --cors-configuration "{
  \"CORSRules\": [{
    \"AllowedOrigins\": [\"$ORIGIN\"],
    \"AllowedMethods\": [\"GET\", \"HEAD\"],
    \"AllowedHeaders\": [\"*\"],
    \"ExposeHeaders\": [\"Content-Length\", \"Content-Range\", \"Accept-Ranges\", \"ETag\"],
    \"MaxAgeSeconds\": 3000
  }]
}"

say "5/6 lifecycle: видео дешевеет со временем, аудио и VTT живут вечно"
# По 05-storage-s3.md: recordings → Standard-IA через 30 дней → Glacier IR
# через 90. transcripts/ и whiteboards/ не трогаем — они мелкие и ценные.
aws s3api put-bucket-lifecycle-configuration --bucket "$BUCKET" --lifecycle-configuration '{
  "Rules": [
    {
      "ID": "recordings-cooldown",
      "Status": "Enabled",
      "Filter": {"Prefix": "recordings/"},
      "Transitions": [
        {"Days": 30, "StorageClass": "STANDARD_IA"},
        {"Days": 90, "StorageClass": "GLACIER_IR"}
      ]
    },
    {
      "ID": "abort-incomplete-uploads",
      "Status": "Enabled",
      "Filter": {"Prefix": ""},
      "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 7}
    }
  ]
}'

say "6/6 IAM: пользователь и политика только на этот бакет"
POLICY_ARN="arn:aws:iam::${ACCOUNT}:policy/${POLICY_NAME}"
if ! aws iam get-policy --policy-arn "$POLICY_ARN" >/dev/null 2>&1; then
    aws iam create-policy --policy-name "$POLICY_NAME" --policy-document "{
      \"Version\": \"2012-10-17\",
      \"Statement\": [
        {
          \"Sid\": \"ObjectsInThisBucketOnly\",
          \"Effect\": \"Allow\",
          \"Action\": [\"s3:PutObject\", \"s3:GetObject\", \"s3:DeleteObject\"],
          \"Resource\": \"arn:aws:s3:::${BUCKET}/*\"
        },
        {
          \"Sid\": \"ListThisBucketOnly\",
          \"Effect\": \"Allow\",
          \"Action\": [\"s3:ListBucket\", \"s3:GetBucketLocation\"],
          \"Resource\": \"arn:aws:s3:::${BUCKET}\"
        }
      ]
    }" >/dev/null
    echo "политика $POLICY_NAME создана"
else
    echo "политика $POLICY_NAME уже есть — пропускаю"
fi

aws iam get-user --user-name "$IAM_USER" >/dev/null 2>&1 || aws iam create-user --user-name "$IAM_USER" >/dev/null
aws iam attach-user-policy --user-name "$IAM_USER" --policy-arn "$POLICY_ARN"

KEY_JSON=$(aws iam create-access-key --user-name "$IAM_USER")
ACCESS_KEY=$(echo "$KEY_JSON" | grep -o '"AccessKeyId": *"[^"]*"' | cut -d'"' -f4)
SECRET_KEY=$(echo "$KEY_JSON" | grep -o '"SecretAccessKey": *"[^"]*"' | cut -d'"' -f4)

cat <<EOF

Готово. Допишите в deploy/.env (секрет показывается один раз):

S3_BUCKET=$BUCKET
S3_REGION=$REGION
S3_ENDPOINT=
S3_ACCESS_KEY_ID=$ACCESS_KEY
S3_SECRET_ACCESS_KEY=$SECRET_KEY

Проверка доступа:
  AWS_ACCESS_KEY_ID=$ACCESS_KEY AWS_SECRET_ACCESS_KEY=$SECRET_KEY \\
    aws s3 ls "s3://$BUCKET" --region $REGION
EOF
