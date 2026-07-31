# S3: layout, доступ, стоимость

> Появляется на этапе 1 (S1-3). Бакет приватный, весь доступ — presigned URL
> от api. Медиа-инвариант: файлы ходят S3 ↔ браузер / S3 ↔ внешние сервисы,
> никогда через VPS.

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

- Запись 720p ≈ 0.7–1.1 GB/час → 80 уроков/мес ≈ 60–90 GB прироста
  (~$2/мес хранение). Audio-only на порядок меньше.
- Egress S3→Internet: первые 100 GB/мес бесплатно — при 50 студентах почти
  наверняка $0. Порог для CloudFront: счёт за egress стабильно > $15–20/мес.

## Lifecycle policy (обязательно, ставится в S1-3)

- `recordings/` → Standard-IA через 30 дней → Glacier Instant Retrieval
  через 90 (или удалять видео через N месяцев, оставляя аудио + VTT —
  транскрипт и словарь ценнее сырого видео).
- Аудио + VTT храним вечно.

## Бэкапы (S1-7)

- Nightly `pg_dump | zstd` → `backups/pg/{date}.dump.zst`,
  retention 7 дневных / 30 дней.
- Сцены досок (whiteboards) и VTT уже лежат в S3 — отдельно не бэкапим.
- RTO для hobby: «пересоздать compose за час» из бэкапа.

Связано: [01-topology.md](01-topology.md) ·
[integrations/livekit.md](integrations/livekit.md) ·
план [stage-1](../plans/stage-1-calendar-livekit.md)
