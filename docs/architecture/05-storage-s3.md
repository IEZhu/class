# S3: layout, доступ, стоимость

> Появляется на этапе 1 (S1-3). Бакет приватный, весь доступ — presigned URL
> от api. Медиа-инвариант: файлы ходят S3 ↔ браузер / S3 ↔ внешние сервисы,
> никогда через VPS.
> **Провайдер — Cloudflare R2** ([ADR-009](decisions.md)), API S3-совместимый:
> всё ниже читается как есть, отличия отмечены явно.

## Layout

```text
s3://lingua/
  recordings/{lesson_id}/room.mp4        (или audio.ogg при audio-only egress)
  transcripts/{lesson_id}.vtt
  whiteboards/{lesson_id}.json
  materials/{lesson_id}/{random}.{ext}  (ключ выбирает api, ADR-024)
  backups/pg/{date}.dump.zst
```

## Доступ

- Bucket приватный; CORS — только с домена платформы: GET/HEAD (плеер,
  скачивание) и PUT с заголовками `Content-Type`, `Content-Disposition`
  (загрузка файлов материалов).
- Presigned GET (TTL ~30 мин) выдаёт api после проверки прав
  (`GET /media/{lesson_id}/url`, `GET /materials/{id}/url`).
- Файлы материалов (ADR-024): преподаватель урока получает presigned PUT
  на 10 мин — тип, длина и `Content-Disposition` с именем файла входят
  в подпись; затем `POST /materials/{id}/confirm`, где api сверяет объект
  (HEAD) с заявленным. Белый список типов и потолок 50 MiB — в
  `backend/internal/httpapi/materials.go`.
- S3 отдаёт HTTP Range → перемотка в плеере Plyr работает из коробки.
- Egress LiveKit пишет в бакет сам (S3 credentials в конфиге egress-запроса);
  AssemblyAI читает запись по presigned GET.

## Объёмы и стоимость

- Запись 720p ≈ 0.7–1.1 GB/час → 80 уроков/мес ≈ 60–90 GB прироста.
  Audio-only на порядок меньше.
- Egress у R2 бесплатен независимо от объёма (ADR-009) — порог «поставить
  CDN перед бакетом» из [06-budget.md](06-budget.md) снят. Платим только
  за хранение и операции по действующему прайсу Cloudflare.
- Infrequent Access дешевле в хранении ($0.01 против $0.015 за GB-месяц),
  но чтение из него платное — $0.01/GB, минимальный срок хранения 30 дней
  (прайс R2 на 2026-10-26). Полный просмотр записи 0.7–1.1 GB стоит
  $0.007–0.011, а экономия на её хранении — $0.0035–0.0055 в месяц:
  переход в IA окупается, пока за месяц читается меньше половины объёма
  записей старше 30 дней. AssemblyAI читает запись сразу после урока,
  ещё из Standard. Станут старые записи пересматривать чаще — правило
  `recordings-to-ia` из `deploy/s3-bootstrap.sh` убрать.

## Lifecycle policy (обязательно, ставится в S1-3)

- `recordings/` → Infrequent Access через 30 дней (пока окупается —
  условие выше).
- Холодной ступени нет: **у R2 всего два класса — Standard и Infrequent
  Access**, аналога Glacier не существует (ADR-009). Роль архива играет
  удаление видео старше N месяцев с сохранением аудио и VTT — транскрипт
  и словарь ценнее сырого видео.
- Аудио + VTT храним вечно.
- Незавершённые multipart-загрузки убираются через 7 дней.

## Ротация токена R2 (долг D-6)

Рабочий токен `Object Read & Write` истекает **2027-08-03**; после этого
перестанут открываться записи и молча перестанут писаться новые.
Ротирует владелец стенда **до 2027-07-20**:

1. В кабинете Cloudflare — новый токен `Object Read & Write` на этот бакет,
   без ограничения по IP ([ADR-009](decisions.md)).
2. В `deploy/.env` — новые `S3_ACCESS_KEY_ID` и `S3_SECRET_ACCESS_KEY`,
   затем `make up`: api читает ключи на старте и передаёт их в каждый
   запрос Egress.
3. Проверка: ссылка `GET /media/{lesson_id}/url` на старую запись
   открывается, тестовый урок записывается.
4. Старый токен отозвать, когда нет идущих уроков (запущенный Egress
   выгружает файл с теми ключами, с которыми стартовал). Дату истечения
   нового — в D-6 и в напоминание за две недели.

## Бэкапы (S1-7)

- Nightly `pg_dump | zstd` → `backups/pg/{date}.dump.zst`,
  retention 7 дневных / 30 дней: все копии за 7 суток, дальше по одной
  на неделю до 30 суток ([ADR-014](decisions.md)). Чистит скрипт
  `deploy/backup-pg.sh`, lifecycle на `backups/pg/` не ставится.
- Бакет и токен для бэкапов — отдельные (`BACKUP_S3_*`): токен записей
  уходит в LiveKit Egress, а в дампе хэши паролей и email (ADR-014).
- Сцены досок (whiteboards) и VTT уже лежат в S3 — отдельно не бэкапим.
- RTO для hobby: «пересоздать compose за час» из бэкапа.

Связано: [01-topology.md](01-topology.md) ·
[integrations/livekit.md](integrations/livekit.md) ·
план [stage-1](../plans/stage-1-calendar-livekit.md)
