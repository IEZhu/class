# Интеграция: Google Calendar

> Этап 1 (S1-1, S1-2). Календарь — канал доставки расписания студентам:
> событие с attendees прилетает в их личные календари автоматически.

## OAuth2

- Web flow **только для преподавателей**, scope `calendar.events`:
  `/account` → `POST /auth/google/start` → экран согласия Google →
  `GET /auth/google/callback` → `/account?google=<итог>`. Защита — `state`
  и PKCE S256, `access_type=offline` + `prompt=consent`
  ([ADR-023](../decisions.md)). Redirect URI —
  `https://<DOMAIN>/api/auth/google/callback`, настройка клиента — в
  [deploy/README.md](../../../deploy/README.md#google-calendar-s1-1).
- Refresh token — в `users.google_refresh_token`, **шифрованный**:
  AES-256-GCM, ключ `GOOGLE_TOKEN_KEY` в `.env`, id пользователя
  в associated data. Смена ключа — преподаватели подключаются заново.
- Отключение (`DELETE /auth/google`) стирает токен и отзывает его в Google.
- Без ключей `GOOGLE_*` подключение недоступно (503, на `/account` —
  «недоступно»), calendar_sync пропускает уроки, остальной стенд работает.
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
не создаёт дубликат события): id события назначаем сами в транзакции урока,
insert с занятым id получает 409 и становится update, удаление отсутствующего
события (404/410) — успех. Джоба ставится в той же транзакции, что
создание, перенос или отмена, и только если у организатора подключён
календарь; подключение ставит джобы для его будущих уроков. Календарь —
основной (`primary`) календарь преподавателя; `attendees` — участники урока
без гостей (у них синтетический email) и без самого преподавателя.
Отозванный в Google доступ (`invalid_grant`) снимает подключение.

Связано: [../flows/lesson-lifecycle.md](../flows/lesson-lifecycle.md) ·
план [stage-1](../../plans/stage-1-calendar-livekit.md) ·
риск «Testing mode» в [../07-risks.md](../07-risks.md)
