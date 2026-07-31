# Топология

> Этапы: закладывается на 0, дополняется сервисами до 5.
> Исходник: [00-source-variant-c.md](00-source-variant-c.md), раздел 1.

## Схема

```text
                        ┌──────────────────────────────────────────┐
  Google Calendar ◄─────┤              VPS (Docker Compose)        │
  (OAuth2, refresh      │                                          │
   token преподавателя) │  caddy ── TLS, reverse proxy             │
                        │   ├─ web        Next.js (SSR/React)      │
 Teacher/Student ──────►│   ├─ api        Go (бизнес-логика, FSRS) │
   browser              │   ├─ worker     Go (фоновые задачи)      │
      │                 │   ├─ postgres   основная БД + FTS        │
      │                 │   ├─ redis      (опц.: очереди/кэш)      │
      │  WebRTC         │   ├─ libretranslate  (--load-only en,ru) │
      │                 │   └─ excalidraw-complete (доска, Go)     │
      │                 └───────────────┬──────────────────────────┘
      ▼                                 │ presigned URLs, webhooks
 LiveKit Cloud ───── Egress ─────► AWS S3 (записи, VTT, бэкапы)
 (комнаты уроков)                       │      ▲
                                        ▼      │ presigned GET
 AssemblyAI ◄─── presigned S3 URL ── browser (плеер Plyr)
 (ASR + diarization, webhook)

 LLM API (генерация текстов)     YouTube RSS + yt-dlp / TED-дампы
                                 (фоновый сборщик субтитров → PG)
```

## Что НЕ живёт на VPS

| Подсистема | Где живёт | Как связана с VPS |
|---|---|---|
| Медиапотоки уроков (WebRTC) | LiveKit Cloud | api выдаёт access token; браузер соединяется с LiveKit напрямую |
| Файлы записей, VTT, материалы, бэкапы | AWS S3 | api выдаёт presigned URL; браузер качает/грузит напрямую |
| Транскрибация (ASR + diarization) | AssemblyAI | worker передаёт presigned GET-ссылку; результат приходит webhook'ом |
| Раздача видео записей | S3 → браузер | presigned GET + HTTP Range (перемотка в Plyr) |

## Правила потоков данных

- **Входящий трафик** — только через caddy (TLS, reverse proxy): `/` → web,
  `/api` → api, путь доски → excalidraw.
- **Webhooks** (LiveKit `egress_ended`, AssemblyAI `completed`) приходят в api
  и только меняют состояние + ставят джобы в очередь; тяжёлая работа — в worker.
- **Медиа-инвариант**: ни один видео/аудио байт урока не проксируется через
  VPS — ни при записи (Egress пишет в S3 сам), ни при просмотре (presigned GET),
  ни при транскрибации (AssemblyAI забирает из S3 по presigned URL).
- **Сборщик субтитров** — offline-процесс worker'а (RSS + yt-dlp), пишет только
  текст в Postgres; видео не скачивается (`--skip-download`).

Связано: [02-services.md](02-services.md) · [05-storage-s3.md](05-storage-s3.md) ·
[flows/lesson-lifecycle.md](flows/lesson-lifecycle.md)
