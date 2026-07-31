# Интеграция: Google Calendar

> Этап 1 (S1-1, S1-2). Календарь — канал доставки расписания студентам:
> событие с attendees прилетает в их личные календари автоматически.

## OAuth2

- Web flow **только для преподавателей**, scope `calendar.events`.
- Refresh token — в `users.google_refresh_token`, **шифрованный**
  (ключ в `.env`; конкретный примитив выбрать в S1-1 и записать в ADR).
- **GCP-приложение перевести из Testing в Production сразу**, иначе refresh
  token живёт 7 дней. Unverified + sensitive scope = warning-экран и лимит
  ~100 пользователей — для нас приемлемо.

## Операции (worker, джоба calendar_sync)

| Действие на платформе | Вызов Calendar API |
|---|---|
| Создание урока | `events.insert`: `attendees` = email участников группы, `sendUpdates: all`, description = `https://platform/lesson/{id}` |
| Перенос урока | `events.update` |
| Отмена урока | `events.delete` |

`gcal_event_id` хранится в `lessons`; джобы идемпотентны (повторный запуск
не создаёт дубликат события).

Связано: [../flows/lesson-lifecycle.md](../flows/lesson-lifecycle.md) ·
план [stage-1](../../plans/stage-1-calendar-livekit.md) ·
риск «Testing mode» в [../07-risks.md](../07-risks.md)
