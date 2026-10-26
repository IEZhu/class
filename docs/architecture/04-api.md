# API-эскиз

> REST/JSON, отдаёт api (Go). Auth: серверные cookie-сессии в Postgres,
> cookie `sid` ([ADR-006](decisions.md)); JWT отклонён.
> Роли: admin | teacher | student | guest ([ADR-007](decisions.md), [ADR-010](decisions.md)).
> Колонка «Этап» — когда эндпоинт появляется.
> Пути в таблице — внутренние (как их видит api): снаружи все они с
> префиксом `/api`, который стрипает caddy (`https://<домен>/api/...`).

## Эндпоинты

| Метод и путь | Назначение | Роль | Этап |
|---|---|---|---|
| `POST /auth/login` | вход (email+пароль → cookie `sid`, ADR-006); учётка в архиве — 403 `account_deactivated` после верного пароля (ADR-024) | все | 0 |
| `POST /auth/logout` | выход (удаление сессии) | все | 0 |
| `GET /auth/me` | текущий пользователь (для web-каркаса S0-5) | все | 0 |
| `POST /groups` | создать группу (name, level) | admin | 0 |
| `GET /groups` | список групп с участниками | admin, teacher | 0 |
| `POST /groups/{id}/members` | добавить участника по email | admin | 0 |
| `POST /lessons` | создать урок (группа, без title — подпись из группы) или событие (без группы, с непустым title); участники — снапшот группы | teacher | 0 |
| `GET /lessons` | список уроков по роли: teacher — свои, student — своих групп | все | 0 |
| `GET /lessons/{id}` | урок + материалы + состояние | участники | 0 |
| `PATCH /lessons/{id}` | перенос (starts_at/ends_at), только `scheduled` | teacher урока | 0 |
| `DELETE /lessons/{id}` | отмена урока, только `scheduled` | teacher урока | 0 |
| `POST /lessons/{id}/materials` | прикрепить материал; с `file: {name, content_type, size}` — заготовка файла и presigned PUT на 10 мин (тип по белому списку, до 50 MiB, ключ выбирает api, ADR-024) | teacher | 0 |
| `POST /lessons/{id}/homework` | прикрепить домашку; `file` — как у материала | teacher | 0 |
| `POST /materials/{id}/confirm` | подтвердить загрузку файла: объекта нет — 409, размер или тип не совпали — объект и материал удаляются, 422; повтор — 200 | teacher урока | 1 |
| `GET /materials/{id}/url` | presigned GET на файл материала, TTL 30 мин; не подтверждён — 409, файла нет — 404 | участники | 1 |
| `DELETE /materials/{id}` | удалить материал вместе с его файлом в хранилище | teacher урока | 1 |
| `GET /join/{token}` | предпросмотр события по гостевой ссылке; 404 — нет/отозвана/перевыпущена, 410 — событие прошло | без входа | 1 |
| `POST /join/{token}` | войти гостем: имя → участник события + сессия; 400 — имя пустое или длиннее 60, 404/410 — как у `GET` | без входа | 1 |
| `POST /lessons/{id}/guest-link` | выпустить/перевыпустить гостевую ссылку; только у события без группы (409 у урока с группой) | admin, teacher события | 1 |
| `DELETE /lessons/{id}/guest-link` | отозвать гостевую ссылку (идемпотентно); 409 у урока с группой | admin, teacher события | 1 |
| `GET /signup/{token}` | предпросмотр приглашения (кого и куда зовут) | без входа | 1 |
| `POST /signup/{token}` | принять приглашение: свой пароль → учётка и сессия одной транзакцией | без входа | 1 |
| `GET /invites` | ожидающие приглашения: admin — все, teacher — свои | admin, teacher | 1 |
| `POST /invites` | выпустить ссылку (email, name, role, group_id); URL в ответе один раз | admin, teacher | 1 |
| `DELETE /invites/{id}` | отозвать приглашение | admin, teacher | 1 |
| `PATCH /auth/me` | своё имя (email и роль себе не меняют) | все | 1 |
| `POST /auth/password` | смена своего пароля (current+new); прочие сессии гасятся | все | 1 |
| `GET /users` | люди: admin — все, teacher — из своих групп; без архива, `?archived=1` — только архив (ADR-024) | admin, teacher | 1 |
| `POST /users` | завести учётку со стартовым паролем; teacher — только student | admin, teacher | 1 |
| `PATCH /users/{id}` | имя; роль — только admin | admin, teacher | 1 |
| `POST /users/{id}/password` | сброс пароля, все сессии владельца гасятся | admin, teacher | 1 |
| `POST /users/{id}/deactivate` | в архив вместо удаления: вход закрыт, все сессии гасятся той же транзакцией; себя — 403 (ADR-024) | admin, teacher (только студенты своих групп) | 1 |
| `POST /users/{id}/reactivate` | вернуть из архива, вход по прежнему паролю | admin, teacher (только студенты своих групп) | 1 |
| `DELETE /groups/{id}/members/{user_id}` | убрать из группы (идемпотентно) | admin | 1 |
| `GET /auth/google` | статус Google Calendar: `available` (ключи на стенде), `connected` (ADR-023) | teacher | 1 |
| `POST /auth/google/start` | `{url}` экрана согласия Google; state и PKCE-verifier — в cookie `google_oauth`; 503 без ключей | teacher | 1 |
| `GET /auth/google/callback` | возврат с Google (`code`, `state`): обмен кода, шифрованный refresh token, джобы calendar_sync будущих уроков; всегда 303 на `/account?google=connected\|denied\|scope\|failed\|unavailable` | teacher | 1 |
| `DELETE /auth/google` | отключить календарь: токен стирается и отзывается в Google (идемпотентно) | teacher | 1 |
| `GET /lessons/{id}/room-token` | LiveKit access token, комната `lesson-{id}` | участники | 1 |
| `POST /lessons/{id}/finish` | ручное завершение урока | teacher | 1 |
| `POST /webhooks/livekit` | `room_started`/`participant_joined` → Egress и status=live, сбой запуска → 503 и повтор (ADR-012); `egress_ended` → recording_s3_key, status=processing и джоба recording_ready (без ASR — сразу done), сбой БД → 503 и повтор (ADR-013); `room_finished` → снимок доски сразу (S3-6) | LiveKit | 1 |
| `GET /media/{lesson_id}/url` | presigned GET на запись, TTL 30 мин | участники | 1 |
| `POST /webhooks/assemblyai` | `{transcript_id, status: completed\|error}` → транскрипт `fetching` + джоба `asr_result` (реплики, затем VTT — в worker, ADR-015); секрет в `X-Lingua-Webhook-Secret`, иначе 401; без секрета в api — 503; неизвестный транскрипт — 404; повтор — 200 без новой джобы | AssemblyAI | 2 |
| `GET /lessons/{id}/transcript` | `{status, utterances[{idx, speaker_label, user_id, start_ms, end_ms, text}], vtt_url, vtt_expires_at}`; VTT — presigned на 30 мин, `null` до выкладки; нет транскрипта — 404 `no_transcript` | участники | 2 |
| `POST /terms/click` | `{surface, context_sentence, lesson_id?}` → `{term_id, lemma, surface, translation: {text, source} \| null, definitions[{word, pos, glosses[], translations_ru[]}], examples: []}`; источники: кэш → датасет → LibreTranslate (без него `translation: null`, 200); 400 — не одно слово, контекст > 1000 символов; 403/404 — чужой/нет урока в `lesson_id` | все, включая гостя | 3 |
| `POST /user-terms` | «в мой словарь» / смена статуса | student | 3 |
| `POST /lessons/{id}/terms/confirm` | `{term_ids[], assign_to[]}` — словарь урока | teacher | 3 |
| `GET /lessons/{id}/whiteboard` | вход на доску: `{room, key, socket_path}` — случайное имя комнаты ретранслятора (из id урока не выводится), ключ AES-GCM, путь ретранслятора; доска заводится при первом входе; 409 — урок в processing/done, 503 — доска не включена (ADR-020) | участники | 3 |
| `GET /lessons/{id}/whiteboard/scene` | сцена файлом `.excalidraw` (элементы как есть, с удалёнными), и после урока; 404 — доску не открывали | участники | 3 |
| `PUT /lessons/{id}/whiteboard/scene` | `{elements: [...]}` → поэлементное слияние с сохранённой, `{version}`; снимок в S3 не чаще раза в минуту; 409/503 — как у входа, 413 — тело или сцена после слияния больше 1 MiB (сохранённая не меняется) | участники | 3 |
| `GET /whiteboard/auth` | решение forward_auth caddy для `/whiteboard/lesson-{id}/socket.io/` (урок — из `X-Forwarded-Uri`): 204 участнику незавершённого урока, 401/403/409 иначе | участники | 3 |
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
