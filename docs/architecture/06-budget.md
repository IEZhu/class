# Смета и пороги апгрейда

## Месяц, полная загрузка

| Статья | Оценка |
|---|---|
| VPS | уже есть → $0 |
| LiveKit Cloud | $0 (пилот) → $50 (Ship) или $0 self-hosted |
| AssemblyAI (~60 ч) | ~$10 |
| S3 (хранение + запросы) | $3–8 (растёт со временем, гасится lifecycle) |
| S3 egress | $0 (в пределах 100 GB free) |
| LLM API | $2–5 |
| DeepL Free / LibreTranslate | $0 |
| **Итого** | **~$15–25/мес** (пилот) / **~$65–75** при LiveKit Ship |

## Пороги апгрейда

| Сигнал | Действие |
|---|---|
| Уроков > ~20/мес (LiveKit free ≈ 5 000 участнико-минут) | Ship $50/мес **или** self-hosted `livekit-server` на VPS ([integrations/livekit.md](integrations/livekit.md)) |
| Счёт за S3 egress стабильно > $15–20/мес | Поставить CloudFront перед бакетом |
| S3 storage растёт к TB | Проверить lifecycle: IA → Glacier, удаление видео старше N мес. ([05-storage-s3.md](05-storage-s3.md)) |
| LibreTranslate не хватает качества | DeepL Free (500k знаков/мес) как «второе мнение» ([integrations/translation.md](integrations/translation.md)) |
