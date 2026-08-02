# API-эскиз

> REST/JSON, отдаёт api (Go). Auth: серверные cookie-сессии в Postgres,
> cookie `sid` ([ADR-006](decisions.md)); JWT отклонён.
> Роли: admin | teacher | student ([ADR-007](decisions.md)).
> Колонка «Этап» — когда эндпоинт появляется.
> Пути в таблице — внутренние (как их видит api): снаружи все они с
> префиксом `/api`, который стрипает caddy (`https://<домен>/api/...`).

## Эндпоинты

| Метод и путь | Назначение | Роль | Этап |
|---|---|---|---|
| `POST /auth/login` | вход (email+пароль → cookie `sid`, ADR-006) | все | 0 |
| `POST /auth/logout` | выход (удаление сессии) | все | 0 |
| `GET /auth/me` | текущий пользователь (для web-каркаса S0-5) | все | 0 |
| `POST /groups` | создать группу (name, level) | admin | 0 |
| `GET /groups` | список групп с участниками | admin, teacher | 0 |
| `POST /groups/{id}/members` | добавить участника по email | admin | 0 |
| `POST /lessons` | создать урок (группа, дата, время); участники — снапшот группы | teacher | 0 |
| `GET /lessons` | список уроков по роли: teacher — свои, student — своих групп | все | 0 |
| `GET /lessons/{id}` | урок + материалы + состояние | участники | 0 |
| `PATCH /lessons/{id}` | перенос (starts_at/ends_at), только `scheduled` | teacher урока | 0 |
| `DELETE /lessons/{id}` | отмена урока, только `scheduled` | teacher урока | 0 |
| `POST /lessons/{id}/materials` | прикрепить материал | teacher | 0 |
| `POST /lessons/{id}/homework` | прикрепить домашку | teacher | 0 |
| `PATCH /auth/me` | своё имя (email и роль себе не меняют) | все | 1 |
| `POST /auth/password` | смена своего пароля (current+new); прочие сессии гасятся | все | 1 |
| `GET /users` | люди: admin — все, teacher — из своих групп | admin, teacher | 1 |
| `POST /users` | завести учётку со стартовым паролем; teacher — только student | admin, teacher | 1 |
| `PATCH /users/{id}` | имя; роль — только admin | admin, teacher | 1 |
| `POST /users/{id}/password` | сброс пароля, все сессии владельца гасятся | admin, teacher | 1 |
| `DELETE /groups/{id}/members/{user_id}` | убрать из группы (идемпотентно) | admin | 1 |
| `POST /auth/google/callback` | OAuth2 callback подключения календаря | teacher | 1 |
| `GET /lessons/{id}/room-token` | LiveKit access token, комната `lesson-{id}` | участники | 1 |
| `POST /lessons/{id}/finish` | ручное завершение урока | teacher | 1 |
| `POST /webhooks/livekit` | `egress_ended` → recording_s3_key, status=processing | LiveKit | 1 |
| `GET /media/{lesson_id}/url` | presigned GET на запись, TTL 30 мин | участники | 1 |
| `POST /webhooks/assemblyai` | `completed` → utterances, VTT | AssemblyAI | 2 |
| `POST /terms/click` | `{surface, context_sentence}` → перевод/дефиниция | все | 3 |
| `POST /user-terms` | «в мой словарь» / смена статуса | student | 3 |
| `POST /lessons/{id}/terms/confirm` | `{term_ids[], assign_to[]}` — словарь урока | teacher | 3 |
| `GET /practice/queue` | due-карточки по FSRS | student | 4 |
| `POST /reviews` | рейтинг карточки (again/hard/good/easy) | student | 4 |
| `POST /cloze/{id}/attempt` | ответы cloze-теста → счёт → FSRS | student | 4 |
| `GET /practice/videos?words=…` | подбор видео по FTS субтитров | student | 5 |
| `GET /practice/texts?words=…` | поиск текстов (generated/Tatoeba) | student | 5 |
| `POST /practice/generate-text` | LLM-генерация текста с валидатором | student/teacher | 5 |

## Правила

- **Webhooks обязаны проверяться**: LiveKit — подпись по API key/secret;
  AssemblyAI — секретный заголовок/токен в webhook_url. Обработчики
  идемпотентны (повторная доставка не дублирует utterances/джобы).
- Webhook-обработчики только меняют состояние и ставят джобу в `jobs` —
  тяжёлая работа в worker.
- Presigned URL выдаёт только api после проверки доступа (участник урока);
  TTL ~30 мин.
- Ошибки — JSON `{error, code}`; коды HTTP честные (403 для чужого урока,
  409 для конфликтов состояния).

Связано: [03-data-model.md](03-data-model.md) ·
[flows/lesson-lifecycle.md](flows/lesson-lifecycle.md) ·
[integrations/](integrations/)
