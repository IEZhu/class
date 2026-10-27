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
| `GET /lessons/{id}` | урок + материалы + состояние; организатору события (admin, teacher) ещё `has_guest_link` — выпущена ли гостевая ссылка (ADR-032) | участники | 0 |
| `PATCH /lessons/{id}` | перенос (starts_at/ends_at), только `scheduled` | teacher урока | 0 |
| `DELETE /lessons/{id}` | отмена урока, только `scheduled`; файлы материалов удаляет worker (`material_files_delete`, ADR-024) | teacher урока | 0 |
| `POST /lessons/{id}/materials` | прикрепить материал; с `file: {name, content_type, size}` — заготовка файла и presigned PUT на 10 мин под `materials-pending/` (тип по белому списку, до 50 MiB, ключ выбирает api, расширение имени — по типу, ADR-024) | teacher | 0 |
| `POST /lessons/{id}/homework` | прикрепить домашку; `file` — как у материала | teacher | 0 |
| `POST /materials/{id}/confirm` | подтвердить загрузку файла: объекта нет — 409, размер или тип не совпали — объект и материал удаляются, 422; годный объект копируется в хранилище на постоянный ключ, ETag запоминается; повтор — 200 | teacher урока | 1 |
| `GET /materials/{id}/url` | presigned GET на файл материала, TTL 30 мин; не подтверждён, пропал или изменён после подтверждения (ETag, размер, тип) — 409, у материала нет файла — 404 | участники | 1 |
| `DELETE /materials/{id}` | удалить материал вместе с его файлом в хранилище; хранилище не ответило — объект удалит worker (джоба `material_files_delete`) | teacher урока | 1 |
| `GET /join/{token}` | предпросмотр события по гостевой ссылке; 404 — нет/отозвана/перевыпущена, 410 — событие прошло | без входа | 1 |
| `POST /join/{token}` | войти гостем: имя → участник события + сессия до `ends_at` + 2 ч (перенос события её сдвигает); 400 — имя пустое или длиннее 60, 404/410 — как у `GET`, 409 `event_full` — на событии уже 100 гостей (ADR-032) | без входа | 1 |
| `POST /lessons/{id}/guest-link` | выпустить/перевыпустить гостевую ссылку; только у события без группы (409 у урока с группой) | admin, teacher события | 1 |
| `DELETE /lessons/{id}/guest-link` | отозвать гостевую ссылку (идемпотентно); 409 у урока с группой | admin, teacher события | 1 |
| `GET /signup/{token}` | предпросмотр приглашения (кого и куда зовут) | без входа | 1 |
| `POST /signup/{token}` | принять приглашение: свой пароль → учётка и сессия одной транзакцией | без входа | 1 |
| `GET /invites` | ожидающие приглашения: admin — все, teacher — свои | admin, teacher | 1 |
| `POST /invites` | выпустить ссылку (email, name, role, group_id); URL в ответе один раз | admin, teacher | 1 |
| `DELETE /invites/{id}` | отозвать приглашение | admin, teacher | 1 |
| `PATCH /auth/me` | своё имя (email и роль себе не меняют); гостю — не длиннее 60, как при входе | все | 1 |
| `POST /auth/password` | смена своего пароля (current+new); прочие сессии гасятся | все | 1 |
| `GET /users` | люди: admin — все, teacher — из своих групп; без архива, `?archived=1` — только архив (ADR-024) | admin, teacher | 1 |
| `POST /users` | завести учётку со стартовым паролем; teacher — только student | admin, teacher | 1 |
| `PATCH /users/{id}` | имя; роль — только admin | admin, teacher | 1 |
| `POST /users/{id}/password` | сброс пароля, все сессии владельца гасятся | admin, teacher | 1 |
| `POST /users/{id}/deactivate` | в архив вместо удаления: вход закрыт, все сессии гасятся той же транзакцией, из комнат идущих уроков LiveKit выставляет; себя — 403 (ADR-024) | admin, teacher (только студенты своих групп) | 1 |
| `POST /users/{id}/reactivate` | вернуть из архива, вход по прежнему паролю | admin, teacher (только студенты своих групп) | 1 |
| `DELETE /groups/{id}/members/{user_id}` | убрать из группы (идемпотентно) | admin | 1 |
| `GET /auth/google` | статус Google Calendar: `available` (ключи на стенде), `connected` (ADR-023) | teacher | 1 |
| `POST /auth/google/start` | `{url}` экрана согласия Google; state, PKCE-verifier и срок — в cookie `__Host-google_oauth`, запечатанном для этого преподавателя (ADR-023); 503 без ключей | teacher | 1 |
| `GET /auth/google/callback` | возврат с Google (`code`, `state`): обмен кода, шифрованный refresh token, джобы calendar_sync будущих уроков; ответ — всегда 303: на `/account?google=connected\|denied\|scope\|failed\|unavailable`, без сессии — на `/login`, не преподавателю — `failed`; cookie подключения гасится | сессию проверяет хендлер (teacher) | 1 |
| `DELETE /auth/google` | отключить календарь: токен стирается и отзывается в Google; `200 {revoked}`, `false` — отозвать не вышло (снять доступ в аккаунте Google); повтор без подключения — `{revoked:true}` | teacher | 1 |
| `GET /lessons/{id}/room-token` | LiveKit access token, комната `lesson-{id}`; только в окне `starts_at` − 15 мин … `ends_at` + 15 мин, токен не дольше окна; 409 `room_not_open` — раньше, `lesson_finished` — позже или урок в processing/done (ADR-031) | участники | 1 |
| `POST /lessons/{id}/finish` | ручное завершение урока | teacher | 1 |
| `POST /webhooks/livekit` | тело ≤ 1 MiB (больше — 413), подпись обязательна (401); `room_started`/`participant_joined` в окне комнаты → сегмент записи (свой файл на запуск) и status=live, сбой запуска → 503 и повтор (ADR-012); `egress_ended` → сегмент complete/failed, урок остаётся live до конца окна; после окна (или уборкой `recording_sweep`) → recording_s3_key = самый большой сегмент, status=processing и джоба recording_ready (без ASR — сразу done), без записи — done; сбой БД → 503 и повтор (ADR-013, ADR-031); `room_finished` → снимок доски сразу (S3-6) | LiveKit | 1 |
| `GET /media/{lesson_id}/url` | presigned GET на запись, TTL 30 мин | участники | 1 |
| `POST /webhooks/assemblyai` | `{transcript_id, status: completed\|error}` → транскрипт `fetching` + джоба `asr_result` (реплики, затем VTT — в worker, ADR-015); секрет в `X-Lingua-Webhook-Secret`, иначе 401; без секрета в api — 503; неизвестный транскрипт — 503 `unknown_transcript` (AssemblyAI повторит: вебхук мог обогнать запись `external_id`; потерянный итог подберёт сверка `asr_poll`, ADR-016); повтор — 200 без новой джобы | AssemblyAI | 2 |
| `GET /lessons/{id}/transcript` | `{status, utterances[{idx, speaker_label, user_id, speaker_name, start_ms, end_ms, text, edited_at}], vtt_url, vtt_expires_at}`; `speaker_name` — имя по маппингу S2-5, `edited_at` — время правки S2-4 (`null` — текст ASR); VTT — presigned на 30 мин, `null` до выкладки; нет транскрипта — 404 `no_transcript` | участники | 2 |
| `PATCH /lessons/{id}/transcript/utterances/{idx}` | `{text}` → реплика; пробелы схлопываются, пустой/NUL/> 20 000 символов — 400; нет реплики — 404 `no_utterance`; тот же текст — 200 без изменений; правка ставит перерендер VTT через минуту (ADR-025) | teacher урока | 2 |
| `PUT /lessons/{id}/transcript/speakers/{label}` | `{user_id: N \| null}` → `{speaker_label, user_id, speaker_name}` для всех реплик метки; человек — teacher урока или участник, иначе 400 `not_participant`; нет метки — 404 `no_speaker`; `null` снимает маппинг (ADR-025) | teacher урока | 2 |
| `POST /terms/click` | `{surface, context_sentence, lesson_id?}` → `{term_id, lemma, surface, translation: {text, source} \| null, definitions[{word, pos, glosses[], translations_ru[]}], examples[{text, translation, source: "tatoeba", attribution: {author, url, translation_author, translation_url, license, license_url}}], status}`; источники: кэш → датасет → LibreTranslate (без него `translation: null`, 200); примеры — до 2 коротких предложений корпуса Tatoeba с леммой и переводом (ADR-028), без корпуса — `[]`; `status` — статус в словаре студента (S3-5), у остальных и вне словаря `null`; незнакомое слово (нет ни в OEWN, ни в датасете) — `term_id: 0`, `translation: null`, `examples: []`, `status: null`, термин не заводится (ADR-017); 400 — не одно слово, контекст > 1000 символов; 403/404 — чужой/нет урока в `lesson_id` | все, включая гостя | 3 |
| `POST /user-terms` | `{term_id, status?, source_lesson_id?}` — «в мой словарь» / смена статуса 1..5 одним upsert: 201 — добавлено, 200 — было; без `status` новое — 1, старое — прежний; источник — первый урок (чужой — 403, нет — 404); нет термина — 404 (ADR-027) | student | 3 |
| `GET /user-terms` | `?status=1,2&limit=&cursor=` → `{items[{term_id, lemma, surface, translation, status, source_lesson: {id, title, group_id, group_name, starts_at} \| null, added_at, updated_at}], next_cursor}`; новые сверху, `limit` до 500 (200) | student | 3 |
| `DELETE /user-terms/{term_id}` | убрать слово из своего словаря; нет в словаре — 404 | student | 3 |
| `POST /user-terms/lookup` | `{words[] ≤ 2000}` → `{lemmas: {слово: лемма}, statuses: {лемма: статус}}` — раскраска ClickableText (ADR-026); не-слова пропускаются, 413 — тело > 256 KiB | student | 3 |
| `POST /lessons/{id}/terms/confirm` | `{term_ids[], assign_to[]}` — словарь урока | teacher | 3 |
| `GET /lessons/{id}/whiteboard` | вход на доску: `{room, key, socket_path}` — случайное имя комнаты ретранслятора (из id урока не выводится), ключ AES-GCM, путь ретранслятора; доска заводится при первом входе; 409 — урок в processing/done, 503 — доска не включена (ADR-020) | участники | 3 |
| `GET /lessons/{id}/whiteboard/scene` | сцена файлом `.excalidraw` (элементы как есть, с удалёнными), и после урока; 404 — доску не открывали | участники | 3 |
| `PUT /lessons/{id}/whiteboard/scene` | `{elements: [...]}` → поэлементное слияние с сохранённой, `{version}`; снимок в S3 не чаще раза в минуту; 409/503 — как у входа, 413 — тело или сцена после слияния больше 1 MiB (сохранённая не меняется) | участники | 3 |
| `GET /whiteboard/auth` | решение forward_auth caddy для `/whiteboard/lesson-{id}/socket.io/` (урок — из `X-Forwarded-Uri`): 204 участнику незавершённого урока, 401/403/409 иначе | участники | 3 |
| `GET /practice/queue` | `?limit=` (20, до 50) → `{items[{term_id, lemma, surface, translation \| null, definitions[], examples[], status, card: {state, due_at, scheduled_days, reps, lapses, last_review_at}}], due_total, next_due_at}`; свои карточки с `due_at <= сейчас`, самые просроченные первыми; `definitions`/`examples` — как у `POST /terms/click` (до 2), перевод — только из кэша; `due_total` — всех к показу, `next_due_at` — ближайший будущий показ или `null` (ADR-036) | student | 4 |
| `POST /reviews` | `{review_id: uuid, term_id, rating: again\|hard\|good\|easy}` → 201 `{term_id, rating, duplicate: false, status, card}`: пересчёт FSRS, запись `srs_reviews`, статус слова по карточке; время — часы api. Повтор того же `review_id` с той же оценкой того же слова — 200 `duplicate: true` без пересчёта; 409 `review_conflict` — `review_id` занят другой оценкой, 409 `not_due` — показ ещё не наступил; 404 — у вызывающего нет карточки этого слова (чужие недостижимы); 400 — `review_id` не uuid, `term_id` ≤ 0, оценка не из четырёх (ADR-036) | student | 4 |
| `POST /cloze/{id}/attempt` | ответы cloze-теста → счёт → FSRS | student | 4 |
| `GET /practice/videos?words=…` | подбор видео по FTS субтитров | student | 5 |
| `GET /practice/texts` | свои сгенерированные тексты, новые первыми (до 50); поиск по словам среди generated/Tatoeba (`?words=…`) — S5-7 | student, teacher | 5 |
| `POST /practice/generate-text` | `{words[]?, level, genre?, max_words?}` → 201 `{id, level, genre, max_words, target_lemmas[], body, word_count, coverage, occurrences{лемма: n}, unknown_share, attempts, provider, model, created_at}`; LLM пишет текст, валидатор покрытия (каждое слово ≥ 2 раз, один ретрай) — ADR-029/030. 400 — слово не из букв, слов > 20, уровень не CEFR, жанр не из `story\|dialogue\|article\|letter\|description`, длина вне 50–400 или < 10 на слово, нет `words` и невыученных слов в личном словаре (без `words` цели — из словаря вызывающего: новые и «забытые», сколько уместит длина, ADR-030); 422 `coverage_failed` `{missing[], coverage}`; 429 — суточный лимит; 503 `llm_not_configured` без ключа, `llm_unavailable` — провайдер перегружен/недоступен; 502 `llm_failed` | student, teacher | 5 |

## Правила

- **Webhooks обязаны проверяться**: LiveKit — подпись по API key/secret;
  AssemblyAI — секретный заголовок/токен в webhook_url. Обработчики
  идемпотентны (повторная доставка не дублирует utterances/джобы).
- Webhook-обработчики только меняют состояние и ставят джобу в `jobs` —
  тяжёлая работа в worker. Исключение — запуск Egress по `room_started`/
  `participant_joined`: синхронный вызов LiveKit под захватом урока (ADR-012).
- Presigned GET выдаёт только api после проверки участия в уроке, TTL
  ~30 мин; presigned PUT на файл материала — только преподавателю урока,
  TTL 10 мин ([ADR-024](decisions.md)).
- Ошибки — JSON `{error, code}`; коды HTTP честные (403 для чужого урока,
  409 для конфликтов состояния).

Связано: [03-data-model.md](03-data-model.md) ·
[flows/lesson-lifecycle.md](flows/lesson-lifecycle.md) ·
[integrations/](integrations/)
