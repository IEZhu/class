# Этап 3 — Словарь, ClickableText, доска (оценка: 2 нед)

**Цель:** слова становятся кликабельными везде, у студента появляется личный
словарь, у урока — доска и post-lesson выбор слов преподавателем.

Доки: [клик по слову](../architecture/flows/word-click.md) ·
[перевод/дефиниции](../architecture/integrations/translation.md) ·
[жизненный цикл урока](../architecture/flows/lesson-lifecycle.md), шаги 4, 8

## Входные артефакты

| Артефакт | Откуда |
|---|---|
| Тексты для рендера: `materials.body_md` и `utterances` | S0-4, S2-2 |
| Транскрипт-вьюер (реплики оборачиваются в ClickableText) | S2-4 |
| compose (добавить libretranslate, excalidraw) | S0-1 |
| S3 + presigned (бэкап сцен досок) | S1-3 |
| `/lesson/{id}` (панель словаря урока, post-lesson экран) | S0-5 |
| Участники группы (адресаты назначения слов) | S0-2 |

## Задачи

### S3-1 Компонент `<ClickableText>`
Токенизация, раскраска по `user_terms.status` (1..5), клик-попап.
Обернуть все существующие тексты: материалы, транскрипт.
**DoD:** одно место рендера текста; в транскрипте и материалах слова кликаются.

### S3-2 terms + лемматизация + /terms/click
Таблица `terms` (lemma, surface, translation_cache); английская
лемматизация в Go (выбор библиотеки → ADR); `POST /terms/click
{surface, context_sentence}` с порядком источников из
[translation.md](../architecture/integrations/translation.md).
Блок «примеры Tatoeba» в ответе — заглушка (**долг D-1**, закроет S5-4);
кнопка «объясни» скрыта (**долг D-2**, закроет S5-8).
**DoD:** повторный клик по слову не ходит во внешние сервисы (кэш).

### S3-3 LibreTranslate в compose
`--load-only en,ru`, лимит threads, внутренняя сеть; вызов из api при
кэш-миссе.
**DoD:** перевод незнакомого слова < 1 c на стенде; RAM в пределах оценки.

### S3-4 Словарный датасет (дефиниции)
Импорт открытых дампов (kaikki.org / Wiktionary) в PG (worker-джоба или
разовый скрипт); дефиниция в попапе.
**DoD:** у частотных слов в попапе есть англ. дефиниция.

### S3-5 user_terms и личный словарь
`POST /user-terms` («в мой словарь», смена статуса 1..5,
`source_lesson_id`); страница словаря студента с фильтрами по статусу.
**DoD:** добавленное на уроке слово видно в словаре с источником-уроком.

### S3-6 Доска Excalidraw
excalidraw-complete в compose; вкладка доски в `/lesson/{id}` (live):
`?room=lesson-{id}`; бэкап JSON-сцены → `s3://…/whiteboards/{lesson_id}.json`
(таблица `whiteboards`).
**DoD:** двое рисуют одновременно; сцена переживает перезапуск контейнера.

### S3-7 Кандидаты словаря урока
`lesson_term_candidates`: клики участников на уроке (`source='click'`),
парсер JSON-сцены доски — элементы `type:"text"` (`source='whiteboard'`),
ручной ввод преподавателя (`source='manual'`).
**DoD:** после урока список кандидатов собран из всех трёх источников.

### S3-8 Post-lesson word picker
Экран преподавателя: чеклист кандидатов → подтверждение → `lesson_terms` →
назначение всем/выборочно → `user_terms(status=1)`.
Создание `srs_cards` здесь — **точка расширения для S4-1** (пока no-op,
долг D-3).
**DoD:** назначенные слова появились в словарях выбранных студентов.

## Выходные артефакты

| Артефакт | Потребитель |
|---|---|
| `<ClickableText>` | S4-3 (карточки), S5-5 (панель субтитров), S5-7 (тексты) |
| Лемматизация (en) | S5-2 (индексация), S5-6 (валидатор покрытия) |
| `terms` + кэш переводов + дефиниции | S4-3 (лицевая/обратная сторона карточек) |
| `user_terms` со статусами | S4-1 (инициализация карточек), S5-5 («забытые слова») |
| `lesson_terms` (словарь урока) | S4-4 (cloze по уроку) |
| Хук назначения слов в S3-8 | S4-1 (FSRS init + бэкфилл) |
| Доска + бэкап сцен | материалы урока |

## Definition of Done этапа

- [ ] S3-1…S3-8 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: клик слова в транскрипте → попап → в словарь → после урока преподаватель подтвердил слова → студенты получили
- [ ] Долги D-1, D-2, D-3 записаны в INDEX; «Факт» заполнен

## Факт (заполняется по ходу реализации)

### S3-2 terms + лемматизация + `POST /terms/click` (☑)

- Миграция `backend/migrations/0009_dictionary` (up/down): таблица
  `terms` (id, lang `en`, lemma, surface — форма первого клика,
  translation_cache jsonb `{"ru": {"text", "source", "at"}}`, created_at),
  уникальность и поиск по лемме — `terms_lang_lemma_key (lang, lemma)`;
  в той же миграции `definitions` и `definition_imports` (S3-4).
- Лемматизация — `backend/internal/lemma` (ADR-017): morphy поверх
  встроенных данных Open English WordNet 2025 (`oewn/*.tsv.gz`,
  атрибуция `oewn/LICENSE`, генератор `lemma/gen`); `lemma.Lemma`,
  `lemma.Normalize`, `lemma.IsWord`, `lemma.Known`. Точность на выборке
  `lemma/testdata/inflections.tsv` — 114/119 (95.8%), промахи — только
  неоднозначные формы (building, tired, bit…).
- Эндпоинт `POST /terms/click` (`internal/httpapi/terms.go`, store —
  `internal/store/terms.go`), любой вошедший, включая гостя события.
  Тело `{surface, context_sentence, lesson_id?}`; ответ `{term_id, lemma,
  surface, translation: {text, source} | null, definitions: [{word, pos,
  glosses[], translations_ru[]}], examples: []}`. Порядок источников —
  [translation.md](../architecture/integrations/translation.md): кэш
  термина → `definitions` (по лемме и по самой форме) → LibreTranslate
  при кэш-миссе (переводится лемма, таймаут 3 с) → запись в кэш.
  Неудача перевода не кэшируется, ответ — 200 с `translation: null`.
  Термин заводится и переводится только для знакомого слова (лемма
  в OEWN или есть статьи датасета); незнакомое — 200 с `term_id: 0`,
  без перевода и записи в `terms` (правка ревью волны A, ADR-017).
  400 — не одно слово / контекст длиннее 1000 символов / плохой
  lesson_id; 403/404 — чужой или несуществующий урок в `lesson_id`.
- Точки расширения: `examples` всегда `[]` (долг D-1, S5-4), кнопки
  «объясни» в ответе нет (долг D-2, S5-8, по `term_id` и
  `context_sentence`); `lesson_id` пока только проверяется — кандидатов
  урока пишет S3-7; статус слова (`user_terms`) добавит S3-5.
- Тесты: `internal/httpapi/terms_test.go` (фейк LibreTranslate на
  httptest: первый клик — один вызов и запись кэша, повторы в любой
  форме — ноль вызовов; сервис лежит/недоступен/не настроен — 200 и
  `translation: null`; незнакомое слово — ни строки в `terms`, ни вызова;
  401/400/403/404), `internal/lemma/lemma_test.go`.

### S3-3 LibreTranslate в compose (◐)

- Клиент `backend/internal/translate` (`POST /translate`, JSON q/source/
  target/format, таймаут 3 с, тесты на httptest); api читает
  `LIBRETRANSLATE_URL`, пусто — перевод выключен (строка в логе на старте).
- `deploy/docker-compose.yml`: сервис `libretranslate`
  (`libretranslate/libretranslate:v1.9.6@sha256:1de2d705…bb32`, профиль
  `translate`, `LT_LOAD_ONLY=en,ru`, `LT_THREADS=1`, без веб-интерфейса
  и перевода файлов, `cpus: 1.5`, `mem_limit: 2g`, healthcheck
  `scripts/healthcheck.py`), разовый `libretranslate-models` (профиль
  `translate-setup`), том `lt_models`, сеть `translate` (`internal:
  true`) — в ней api и libretranslate, портов наружу нет (ADR-018).
- Новые ключи `.env`: `COMPOSE_PROFILES` (включить `translate`),
  `LIBRETRANSLATE_URL` (`http://libretranslate:5000`). Make-цель
  `translate-models`; запуск — [deploy/README.md](../../deploy/README.md).
- Образ вне блокирующего скана ADR-011 — долг D-10. Замер на одноразовом
  стенде на этом VPS: перевод незнакомого слова медиана 301 мс, максимум
  620 мс (20 слов), RAM ~376 MiB. **Осталось:** включить профиль на
  стенде (решение владельца, D-10) и повторить замер там.

### S3-4 Словарный датасет (◐)

- Таблицы `definitions` (word, lemma, pos, etymology, glosses[],
  translations_ru[], source_line, import_id; уникальность
  `definitions_entry_key (word, pos, etymology)`, индекс
  `definitions_lemma_idx`) и `definition_imports` (source,
  source_version, lines_done, entries, finished_at, abandoned_at) —
  миграция 0009. `-restart` помечает незавершённый прогон брошенным
  и начинает новый (правка ревью волны A: прежде он продолжал старый
  прогон, и статьи старой версии источника переживали рестарт).
- Импорт — пакет `backend/internal/dictionary` и команда
  `backend/cmd/dictimport` (в образе worker'а, `/usr/local/bin/dictimport`;
  make-цель `dict-import`). Источник — JSONL Wiktextract с kaikki.org
  (по умолчанию `raw-wiktextract-data.jsonl.gz`), отбор и потолки —
  ADR-019; продолжение с контрольной точки, `-restart`, удаление
  устаревших статей только по успешному завершению.
- Тесты на фикстуре `internal/dictionary/testdata/wiktextract-sample.jsonl`:
  `internal/dictionary/entry_test.go`, `internal/httpapi/dictionary_import_test.go`
  (повтор — тот же словарь, gzip, продолжение после обрыва, смена версии
  источника, `-restart` с другим содержимым v2 = чистый импорт v2, пустой
  и недокачанный источник, один импорт за раз, миграция up/down/up).
- Ожидаемый объём — 80–150 тыс. статей, 60–120 MB в PG (оценка
  по выборке первых 20 000 строк дампа, ADR-019). **Осталось:** полный
  импорт на стенде (`make dict-import`), после него — проверка DoD
  на частотных словах.

### S3-6 Доска Excalidraw (2026-10-26)

- Решение — [ADR-020](../architecture/decisions.md): excalidraw-complete
  закрыт автором, доска собрана из редактора `@excalidraw/excalidraw`
  0.18.1 в web, ретранслятора `excalidraw/excalidraw-room` (тег
  `sha-03ff435` + digest) и хранения сцены в api. Комната — случайное
  имя (`room_id`, из id урока не выводится), сообщения шифруются ключом
  комнаты; имя и ключ выдаются только участникам; вместо `?room=` — путь
  `/whiteboard/lesson-{id}/socket.io/` за forward_auth.
- Миграция `backend/migrations/0010_whiteboards.up.sql` (откат — `.down.sql`,
  заодно убирает ждущие снимки): таблица `whiteboards` — `lesson_id` (PK,
  каскад с уроком), `room_id` (UNIQUE), `room_key`, `scene jsonb` (массив элементов как есть,
  с удалёнными), `scene_version`, `snapshot_version`, `scene_s3_key`,
  `updated_at`, `created_at`.
- `backend/internal/store/whiteboards.go` — `OpenWhiteboard`
  (`WhiteboardRoom`: имя и ключ комнаты), `WhiteboardScene`,
  `SaveWhiteboardScene` (поэлементное слияние `mergeElements` по правилу
  `reconcileElements`, порядок по `index`; итог больше предела —
  `ErrSceneTooLarge`, сцена не меняется),
  `ScheduleWhiteboardSnapshot`, `WhiteboardForSnapshot`,
  `MarkWhiteboardSnapshot`, `WhiteboardFile` (формат `.excalidraw`).
  Джоба `whiteboard_snapshot` (`store.JobWhiteboardSnapshot`, payload
  `{"lesson_id"}`).
- `backend/internal/jobs/jobs.go` — `EnqueueOnce`: одна ждущая джоба
  на kind+payload, срок только приближается, ретрай не подтягивается.
- `backend/internal/storage/s3.go` — `PutObject` (обычный подписанный PUT
  с `ContentLength`, без необязательной контрольной суммы; после ревью
  волны A — единственный хелпер записи объекта, им же пишется VTT S2-3),
  `WhiteboardKey` → `whiteboards/{lesson_id}.json`.
- `backend/internal/httpapi/whiteboard.go` — `GET /lessons/{id}/whiteboard`,
  `GET` и `PUT /lessons/{id}/whiteboard/scene` (предел 1 MiB и на тело,
  и на сцену после слияния — 413 `scene_too_large`),
  `GET /whiteboard/auth` для forward_auth; `webhooks.go` — `room_finished`
  ставит снимок сразу. Ключ `.env` api: `WHITEBOARD_ENABLED` (без `1` — 503).
- `backend/internal/worker/whiteboard.go` + `worker.RegisterWhiteboard`;
  `cmd/worker` читает `S3_*` и без хранилища kind не регистрирует (джобы
  ждут в очереди).
- deploy: сервис `whiteboard` в профиле `whiteboard` (`node`, read_only,
  без портов), маршрут в `Caddyfile` (forward_auth → rewrite →
  ретранслятор), worker получил `S3_*`; ключи `.env.example`:
  `COMPOSE_PROFILES`, `WHITEBOARD_ENABLED`.
- web: вкладки `app/lesson/[id]/live-tabs.tsx` («Класс» / «Доска», только
  в фазе live; живая секция шире колонки), `whiteboard.tsx` (next/dynamic
  без SSR), `whiteboard-canvas.tsx` (синхронизация, сохранение через 2 с
  и сразу при уходе со страницы — `pagehide`/`visibilitychange`, запрос
  `keepalive`; ответ новичкам `SCENE_INIT` не чаще раза в 10 с на сокет,
  одной рассылкой через 1 с; курсоры), `whiteboard-sync.ts` (AES-GCM); `scripts/excalidraw-assets.mjs`
  (`predev`/`prebuild`) кладёт шрифты в `public/excalidraw/` (в .gitignore).
  Зависимости: `@excalidraw/excalidraw` 0.18.1, `socket.io-client` ^4.8.4,
  `overrides` для nanoid, lodash-es, katex.
- Для S3-7: текстовые элементы — `whiteboards.scene` (`type = 'text'`,
  `isDeleted = false`), тот же массив — в снимке `whiteboards/{lesson_id}.json`.
- Проверено на изолированном стенде (compose-проект `classci-a3`, S3 —
  заглушка): двое участников в комнате через caddy и WebSocket, чужой
  студент — 403, аноним — 401; двухбраузерная проверка (Playwright):
  прямоугольник преподавателя доходит до студента через ретранслятор,
  его удаление сохраняется; периодический снимок записан через минуту,
  после перезапуска контейнера `whiteboard` сцена на месте и совпадает
  со снимком. Ретранслятор занимает ~12 MiB RAM.
- Долг D-11: образ ретранслятора вне блокирующего скана.

### S3-1 `<ClickableText>` (2026-10-26)

- Решение — [ADR-026](../architecture/decisions.md): токенизация в
  браузере, леммы и статусы — от api.
- `web/lib/tokenize.ts` — `tokenize` (слова и всё между ними, склейка
  токенов = исходный текст), `wordKey`, `uniqueKeys`, `sentenceAround`
  (`context_sentence` ≤ 1000 символов); `web/lib/term-status.ts` —
  `TermStatuses` (кэш статусов на вкладку, пачки lookup по 2000 слов),
  `TermStatus`, `STATUS_LABELS`. Тесты — `web/lib/tokenize.test.ts`,
  `web/lib/term-status.test.ts`, `npm test` (`node --test`, шаг web
  в `.github/workflows/ci.yml`).
- `web/app/clickable-text.tsx` — `ClickableText` (по умолчанию) и
  `ClickableTextScope` (`lessonId`, `dictionary`); попап — лемма и форма,
  перевод, статус, дефиниции с подписью «Wiktionary, CC BY-SA», примеры
  Tatoeba с переводом и подписью (`ExampleAuthors`: авторы предложения и
  перевода со ссылками на tatoeba.org, под списком «Tatoeba, CC BY 2.0 FR»,
  ADR-028), если есть. `web/app/term-status-control.tsx`
  — `TermStatusControl` и общий кэш `termStatuses`. `term_id` 0 (слово
  api не знает, термин не заведён) — попап без «в мой словарь» и статуса.
- Обёрнуты: тексты материалов и домашки (`body_md`) на странице урока —
  её видят и гости события. Контекст урока —
  `web/app/lesson/[id]/layout.tsx` (раскраска и статус — у студента).
- Транскрипт (интеграция волны B, 2026-10-27): `UtteranceText`
  (`web/app/lesson/[id]/utterance-text.tsx`) рендерит реплику
  `<ClickableText text={u.text} />`, урок и раскраску даёт layout урока.
  Клик по слову (`stopPropagation` в `ClickableText`) и клики в попапе
  не всплывают в перемотку реплики S2-4. Проверено `tsc` и сборкой
  (WEB_CI_OK), в браузере после интеграции не прогонялось.
- Для S3-7: клик на странице урока уходит в `POST /terms/click` с
  `lesson_id`, api проверяет урок в `checkClickLesson` — запись
  кандидата `source='click'` добавляется там же, web менять не нужно.
- Проверено в браузере (Chromium, Playwright, одноразовый стенд
  `classci-b2`): один lookup на все тексты страницы, клик → попап с
  дефиницией и атрибуцией → «в мой словарь» → Running/ran/runs
  перекрашены, статус 3 перекрасил все формы и пережил перезагрузку;
  слово дефиниции переводит тот же попап; у преподавателя попап без
  статуса и без запросов `/user-terms`.
- Долг D-17: `body_md` показывается текстом, разметка markdown не
  рендерится.

### S3-5 user_terms и личный словарь (2026-10-26)

- Решение — [ADR-027](../architecture/decisions.md). Миграция
  `backend/migrations/0015_user_terms` (up/down): таблица `user_terms`
  (user_id, term_id, status 1..5, source_lesson_id `ON DELETE SET NULL`,
  added_at, updated_at; PK `(user_id, term_id)`, индекс
  `user_terms_user_added_idx`).
- `backend/internal/store/user_terms.go` — `SaveUserTerm` (upsert:
  статус, первый урок-источник), `DeleteUserTerm`, `GetUserTerm`,
  `ListUserTerms` (фильтр по статусам, курсор), `UserTermStatus`,
  `UserTermStatusesByLemma`; `MinTermStatus`/`MaxTermStatus`.
- `backend/internal/httpapi/user_terms.go`, только student:
  `POST /user-terms` (201/200), `GET /user-terms?status=&limit=&cursor=`
  → `{items, next_cursor}`, `DELETE /user-terms/{term_id}`,
  `POST /user-terms/lookup {words ≤ 2000}` → `{lemmas, statuses}`.
  `POST /terms/click` отдаёт `status` (у не-студента `null`).
- web: страница `/dictionary` (`web/app/dictionary/page.tsx`, фильтры
  «Все», «Учу (1–4)», 1…5, урок-источник, страницы по курсору),
  `dictionary-status.tsx`; ссылка «Словарь» в шапке у студента.
- Тесты — `internal/httpapi/user_terms_test.go`: добавление, смена и
  удаление; 400 на статус вне 1..5 и кривые фильтры, CHECK и PK в схеме;
  параллельные добавления — одна строка; урок-источник (чужой — 403,
  нет — 404, первый не переписывается, отмена урока обнуляет);
  страницы курсора; lookup отдаёт только статусы вошедшего, пачка
  2000 слов — 72 мс под `-race`; 401/403 для гостя, преподавателя,
  админа; откат и повторный накат 0015.
- Для S3-8: назначение слов — `store.SaveUserTerm(ctx, studentID,
  termID, &1, &lessonID)`; для S4-1 — строки `user_terms`.

### S3-7 Кандидаты словаря урока (2026-10-27)

- Решение — [ADR-033](../architecture/decisions.md). Миграция
  `backend/migrations/0019_lesson_terms` (up/down; down снимает и ждущие
  джобы `whiteboard_terms`): таблица `lesson_term_candidates` (lesson_id
  `ON DELETE CASCADE`, term_id, source click/whiteboard/manual, surface,
  count, added_by, first_seen_at, last_seen_at; PK
  `(lesson_id, term_id, source)`); в той же миграции `lesson_terms` (S3-8).
- Клики: `POST /terms/click` с `lesson_id`
  (`backend/internal/httpapi/terms.go`, `recordClickCandidate`) —
  у знакомого слова `store.RecordClickCandidate`
  (`backend/internal/store/lesson_terms.go`): upsert со счётчиком, только
  участник из `lesson_participants`, не преподаватель урока.
- Доска: пакет `backend/internal/lessonterms` — `SceneTexts` (живые
  элементы `type: "text"`, `originalText`, иначе `text`), `TextWords`
  (`lemma.Words`, без `lemma.FunctionWord`, по леммам со счётчиком),
  `Known` (OEWN или `store.DefinedLemmas`), `Terms` (`EnsureTerm`),
  `CollectWhiteboard` (не больше `MaxWhiteboardTerms` = 500, счётчик —
  вхождения в текущей сцене). Образец сцены —
  `backend/internal/lessonterms/testdata/scene.json`.
- Токенизатор и служебные слова перенесены из `internal/textgen`
  в `backend/internal/lemma/words.go` (`lemma.Words`, `lemma.FunctionWord`;
  тест `words_test.go`), `lemma.Lang = "en"`; валидатор покрытия S5-6
  зовёт их же.
- Джоба `whiteboard_terms` (`store.JobWhiteboardTerms`, payload
  `{lesson_id}`): ставится в транзакции перехода урока из live
  (`closeLessonIfOver`, `backend/internal/store/recordings.go`), если
  доску открывали; хендлер — `worker.RegisterLessonTerms`
  (`backend/internal/worker/worker.go`, вызов в `cmd/worker`), ключей
  не требует.
- Эндпоинты (`backend/internal/httpapi/lesson_terms.go`, teacher урока):
  `GET /lessons/{id}/term-candidates` → `{items[{term_id, lemma, surface,
  translation, source, count, first_seen_at, last_seen_at, confirmed}]}`;
  `POST /lessons/{id}/term-candidates {words[] ≤ 100}` → `{added[{term_id,
  lemma, surface}], rejected[{word, reason: not_a_word|unknown}]}`;
  `POST /lessons/{id}/term-candidates/whiteboard` → `{candidates}`.
- Тесты: `internal/lessonterms/lessonterms_test.go` (тексты сцены-образца,
  слова по леммам), `internal/httpapi/lesson_terms_test.go`
  (`TestClickCandidatesFromParticipants`: формы и 20 параллельных кликов —
  один счётчик, незнакомое слово, клик без урока, клик преподавателя и
  чужого студента — без кандидата; `TestWhiteboardCandidatesFromScene`:
  разбор по кнопке и повтор, 401/403/404, урок без доски, конец урока
  через уборку записей → джоба → кандидаты;
  `TestManualCandidatesByLessonTeacher`: rejected, повтор, 400/401/403/404;
  `TestLessonTermsMigrationReversible`; `TestDictionaryMigrationReverts`
  откатывает и 0019 — она ссылается на `terms`). Новых ключей `.env` нет.
- Долг D-22: кандидаты — только одиночные слова.

### S3-8 Post-lesson word picker (2026-10-27)

- Решение — [ADR-034](../architecture/decisions.md). Таблица `lesson_terms`
  (lesson_id `ON DELETE CASCADE`, term_id, confirmed_by, confirmed_at;
  PK `(lesson_id, term_id)`), миграция `0019_lesson_terms`.
- Store (`backend/internal/store/lesson_terms.go`): `LessonCandidates`,
  `SetLessonTerms` (словарь целиком, только кандидаты урока),
  `LessonTerms`, `AssignLessonTerms` — по паре
  `SaveUserTerm(ctx, studentID, termID, nil, &lessonID)`: новое слово —
  статус 1 и урок-источник, у существующего статус не меняется.
- Эндпоинты (`backend/internal/httpapi/lesson_terms.go`):
  `GET /lessons/{id}/terms` (участники) и `PUT /lessons/{id}/terms
  {term_ids[]}` (teacher урока) → `{items[{term_id, lemma, surface,
  translation, confirmed_at}]}`, 400 `not_candidates`;
  `POST /lessons/{id}/terms/assignments {all: true} | {student_ids[]}`
  (teacher урока) → `{students, terms, added, kept}`, 400 `not_student`,
  409 `no_lesson_terms` / `no_students`.
- web: `web/app/lesson/[id]/term-picker.tsx` (`TermPicker`: группы
  «Клики учеников», «С доски», «Добавлены вручную», слова через
  `<ClickableText>`, сохранение, ручной ввод, «Собрать слова с доски»,
  назначение всем/выбранным) — у преподавателя урока с фазы live;
  `web/app/lesson/[id]/lesson-dictionary.tsx` — словарь урока у остальных
  участников; типы `TermCandidate`, `ManualCandidatesResult`,
  `LessonTerm`, `AssignResult` в `web/lib/api.ts`.
- Тесты: `TestPickerConfirmAndAssign` — не кандидаты (400), снятие
  термина, назначение выбранной студентке (слово со статусом 4 остаётся
  4, источник заполнен), Боб без слов, повтор (`added: 0`, `updated_at`
  на месте), всем, слова в `GET /user-terms` с уроком-источником, 400/
  401/403/409, чтение словаря урока участниками, удаление урока уносит
  кандидатов и словарь, но не `user_terms`. Проверено `tsc` и сборкой
  (WEB_CI_OK), в браузере не прогонялось.
- Для S4-1 (долг D-3): хук карточек — в `store.SaveUserTerm` при
  `created = true`; назначение S3-8 идёт через него
  (`store.AssignLessonTerms`). Для S4-4 — слова урока в `lesson_terms`.

## Риски этапа

RAM LibreTranslate (лечится `--load-only`); качество лемматизации решает
слипание/дребезг терминов — выбрать библиотеку осознанно (ADR); парсер сцены
Excalidraw зависит от формата JSON — закрепить версией контейнера.
