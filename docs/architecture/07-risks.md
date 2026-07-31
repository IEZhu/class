# Риски и митигации

| Риск | Митигация | Где закрывается |
|---|---|---|
| Refresh token умирает через 7 дней (GCP Testing mode) | Перевести GCP-app в Production сразу | S1-1 |
| LiveKit free tier мал (≈20 уроков/мес) | Порог: Ship $50 или self-hosted livekit-server на VPS (audio-only egress на слабом железе) | [integrations/livekit.md](integrations/livekit.md), [06-budget.md](06-budget.md) |
| YouTube блокирует IP VPS | Низкий рейт + cookies + backoff; fallback — сборщик с домашней машины в ту же PG (VPN/Tailscale) | S5-2, [flows/subtitle-harvester.md](flows/subtitle-harvester.md) |
| RU/EN code-switching деградирует ASR | `language_code: en`, принять деградацию русских вставок; ручная правка utterances в UI | S2-4, [integrations/assemblyai.md](integrations/assemblyai.md) |
| LibreTranslate слаб на редких словах | Кэш + словарный датасет для дефиниций + DeepL Free (500k знаков/мес) как «второе мнение» | S3-3/S3-4, [integrations/translation.md](integrations/translation.md) |
| Рост S3 до TB за год | Lifecycle: IA → Glacier / удаление видео, аудио+VTT хранить вечно | S1-3, [05-storage-s3.md](05-storage-s3.md) |
| Один VPS = единая точка отказа | Nightly-бэкапы в S3; RTO «пересоздать compose за час» для hobby приемлем | S1-7 |
| Перекрывающаяся речь на уроке | Свойство задачи, не сервиса: принять; влияет только на качество транскрипта | — |
