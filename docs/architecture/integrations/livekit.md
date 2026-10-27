# Интеграция: LiveKit (WebRTC)

> Этап 1 (S1-4, S1-5). Комната на урок; медиапотоки идут браузер ↔ LiveKit
> Cloud, мимо VPS.

## Использование

- Комната `lesson-{id}`; api через серверный SDK выдаёт access token
  (`GET /lessons/{id}/room-token`, identity = user_id, права по роли).
- Токен в комнату — только в окне урока: с `starts_at` − 15 мин до
  `ends_at` + 15 мин ([ADR-031](../decisions.md)).
- **Egress** (запись): room composite → сегмент
  `s3://…/recordings/{id}/<время>-<случайное>.mp4` — у каждого запуска свой
  файл; S3 credentials передаются в конфиге egress-запроса. Экономный
  вариант — **audio-only** (`.ogg`, для транскрипта этого достаточно).
  Запуск — по `room_started`, страховка — по `participant_joined`, один
  сегмент на сессию комнаты и только в окне урока; статус live фиксируется
  только вместе с успешным запуском ([ADR-012](../decisions.md)).
- Webhook `egress_ended` → `POST /webhooks/livekit` (проверка подписи по
  API key/secret, тело ≤ 1 MiB) → сегмент complete или failed; урок
  остаётся live до конца окна. После окна — сразу или уборкой
  `recording_sweep` — `lessons.recording_s3_key` = самый большой сегмент
  и `status=processing` (без записи — done), [ADR-031](../decisions.md).
- Архивация учётки (D-5) выставляет человека из комнат идущих уроков:
  `RoomService/RemoveParticipant`, прежние токены в эту комнату
  гасятся ([ADR-024](../decisions.md)).

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
