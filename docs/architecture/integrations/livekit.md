# Интеграция: LiveKit (WebRTC)

> Этап 1 (S1-4, S1-5). Комната на урок; медиапотоки идут браузер ↔ LiveKit
> Cloud, мимо VPS.

## Использование

- Комната `lesson-{id}`; api через серверный SDK выдаёт access token
  (`GET /lessons/{id}/room-token`, identity = user_id, права по роли).
- **Egress** (запись): room composite → `s3://…/recordings/{id}/room.mp4`;
  S3 credentials передаются в конфиге egress-запроса. Экономный вариант —
  **audio-only** (для транскрипта этого достаточно).
- Webhook `egress_ended` → `POST /webhooks/livekit` (проверка подписи по
  API key/secret) → `lessons.recording_s3_key`, `status=processing`.

## Ёмкость и пороги

- Free tier ≈ 5 000 участнико-минут/мес: урок 60 мин × 4 чел = 240 →
  **~20 уроков/мес**.
- Пилот — free. Полная загрузка → **Ship $50/мес** или **self-hosted
  `livekit-server`** на этой же VPS:
  - открыть UDP-диапазон + TURN/TLS;
  - для записи добавить egress-контейнер — он прожорлив: на слабой VPS
    писать audio-only.
- Токены и API совместимы — миграция cloud → self-hosted не трогает код api.

Связано: [../05-storage-s3.md](../05-storage-s3.md) ·
[../06-budget.md](../06-budget.md) ·
план [stage-1](../../plans/stage-1-calendar-livekit.md)
