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
  materials/{lesson_id}/…
  backups/pg/{date}.dump.zst
```

## Доступ

- Bucket приватный; CORS — только GET для домена платформы.
- Presigned GET (TTL ~30 мин) выдаёт api после проверки прав
  (`GET /media/{lesson_id}/url`).
- S3 отдаёт HTTP Range → перемотка в плеере Plyr работает из коробки.
- Egress LiveKit пишет в бакет сам (S3 credentials в конфиге egress-запроса);
  AssemblyAI читает запись по presigned GET.

## Объёмы и стоимость

- Запись 720p ≈ 0.7–1.1 GB/час → 80 уроков/мес ≈ 60–90 GB прироста.
  Audio-only на порядок меньше.
- Egress у R2 бесплатен независимо от объёма (ADR-009) — порог «поставить
  CDN перед бакетом» из [06-budget.md](06-budget.md) снят. Платим только
  за хранение и операции по действующему прайсу Cloudflare.

## Lifecycle policy (обязательно, ставится в S1-3)

- `recordings/` → Infrequent Access через 30 дней.
- Холодной ступени нет: **у R2 всего два класса — Standard и Infrequent
  Access**, аналога Glacier не существует (ADR-009). Роль архива играет
  удаление видео старше N месяцев с сохранением аудио и VTT — транскрипт
  и словарь ценнее сырого видео.
- Аудио + VTT храним вечно.
- Незавершённые multipart-загрузки убираются через 7 дней.

## Бэкапы (S1-7)

- Nightly `pg_dump | zstd` → `backups/pg/{date}.dump.zst`,
  retention 7 дневных / 30 дней.
- Сцены досок (whiteboards) и VTT уже лежат в S3 — отдельно не бэкапим.
- RTO для hobby: «пересоздать compose за час» из бэкапа.

Связано: [01-topology.md](01-topology.md) ·
[integrations/livekit.md](integrations/livekit.md) ·
план [stage-1](../plans/stage-1-calendar-livekit.md)
