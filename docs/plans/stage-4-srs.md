# Этап 4 — SRS (FSRS), карточки, cloze (оценка: 1–2 нед)

**Цель:** назначенные слова попадают в график повторений FSRS; студент
проходит карточки и cloze-тесты, результаты двигают график.

Доки: [движок практики](../architecture/flows/practice-engine.md) ·
[модель данных](../architecture/03-data-model.md), блок SRS

## Входные артефакты

| Артефакт | Откуда |
|---|---|
| `user_terms` (назначенные слова, статусы) | S3-5, S3-8 |
| Хук назначения слов (точка расширения) | S3-8 |
| `lesson_terms` (словарь урока) | S3-8 |
| `utterances` (предложения урока для cloze) | S2-2 |
| `terms` (перевод/дефиниция для карточек) | S3-2, S3-4 |
| `<ClickableText>` (рендер примеров) | S3-1 |

## Задачи

### S4-1 go-fsrs + инициализация карточек
Интеграция go-fsrs; в хук S3-8 — создание `srs_cards` при назначении
(**закрывает долг D-3**); бэкфилл srs_cards для существующих user_terms.
**DoD:** у каждого назначенного слова есть карточка с due_at.

### S4-2 Очередь повторений
`GET /practice/queue`: due-карточки (`due_at <= now`), сортировка по
просроченности.
**DoD:** очередь корректна на сидированных данных с разными due.

### S4-3 UI карточек + reviews
Карточка: term → перевод/дефиниция/пример; `POST /reviews`
(again/hard/good/easy) → `srs_reviews` → пересчёт FSRS → новое due_at.
**DoD:** рейтинг меняет интервал следующего показа предсказуемо.

### S4-4 Cloze-генератор
Источники предложений: `utterances` урока, материалы; пропуск = целевое
слово; дистракторы — из того же списка слов урока (LLM-дистракторы —
апгрейд S5-8). Таблицы `cloze_tests`, `cloze_items`.
**DoD:** по словарю урока генерируется валидный тест ≥ 5 пунктов.

### S4-5 Cloze-прохождение → FSRS
UI теста; `POST /cloze/{id}/attempt` → `cloze_attempts` (answers, score) →
маппинг в `srs_reviews(source='cloze')`: верно+быстро=easy, верно=good,
неверно=again.
**DoD:** результат теста сдвигает due_at затронутых карточек.

## Выходные артефакты

| Артефакт | Потребитель |
|---|---|
| `srs_cards`/`srs_reviews` + пересчёт FSRS | S5-5, S5-7 («забытые слова»: due/lapses) |
| `GET /practice/queue` | ежедневная практика студента |
| Cloze-генератор | S5-8 (LLM-дистракторы), cloze по сгенерированным текстам (S5-6) |

## Definition of Done этапа

- [ ] S4-1…S4-5 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: назначение слов → карточки due → прохождение → cloze → интервалы сдвинулись
- [ ] Долг D-3 закрыт и отмечен в INDEX; «Факт» заполнен

## Факт (заполняется по ходу реализации)

### S4-1 go-fsrs + инициализация карточек (2026-10-27, закрывает D-3)

- Решение — [ADR-036](../architecture/decisions.md). Зависимость
  `github.com/open-spaced-repetition/go-fsrs/v4 v4.0.0` (MIT, FSRS-6) в
  `backend/go.mod`; новых ключей `.env` нет.
- Миграция `backend/migrations/0021_srs` (up/down): `srs_cards` (PK
  `(user_id, term_id)`, поля карточки go-fsrs, FK на `user_terms`
  `ON DELETE CASCADE`, индекс `srs_cards_user_due_idx`) и `srs_reviews`
  (identity, `client_id uuid`, `UNIQUE (user_id, client_id)`, rating,
  source `card|cloze`, reviewed_at; FK на карточку с каскадом).
- `backend/internal/srs/` — планировщик (`NewScheduler`, `Review`),
  `NewCard`, `ParseRating`, состояния и оценки строками схемы, правило
  статуса слова `Status`; тесты `srs_test.go` с подставленным временем.
- Хук: `store.SaveUserTerm` (`backend/internal/store/user_terms.go`) —
  новая строка `user_terms` и её карточка в одной транзакции; этим же
  вызовом назначает слова S3-8. Удаление слова уносит карточку и журнал.
- Бэкфилл: `store.BackfillSRSCards` (`backend/internal/store/srs.go`),
  идемпотентный; вызывает `cmd/worker` (роль core) на каждом старте,
  число заведённых карточек — в журнале worker.

### S4-2 Очередь повторений (2026-10-27)

- `GET /practice/queue?limit=` (student; `backend/internal/httpapi/reviews.go`)
  → `{items, due_total, next_due_at}`: свои карточки с `due_at <= сейчас`,
  по `due_at`, затем `term_id`, до 20 (потолок 50); в карточке лемма,
  перевод из кэша, до двух статей словаря, примеры Tatoeba, статус и
  состояние карточки. Store: `DueCards`, `DueStats`.

### S4-3 UI карточек + reviews (2026-10-27)

- `POST /reviews {review_id, term_id, rating}` (student) → пересчёт FSRS
  (`store.ReviewCard`), строка `srs_reviews` (`source='card'`), статус
  слова по карточке; 201 / 200 повтор `review_id` / 409 `not_due`,
  `review_conflict` / 404 чужая или нет карточки.
- web: `/practice/cards` (`web/app/practice/cards/page.tsx`,
  `cards-session.tsx`): лицо — лемма, оборот — перевод, статьи, примеры
  с подписями, все тексты через `<ClickableText>`; пробел/Enter — оборот,
  1–4 — оценка; прогресс «N из M», счётчики оценок, когда следующий показ.
  `web/lib/review-session.ts` (клавиши, `review_id`, подписи интервалов)
  с тестами `npm test`; типы `PracticeQueue`, `ReviewResult` в
  `web/lib/api.ts`; ссылка «Карточки» в шапке студента.
- Тесты — `backend/internal/httpapi/reviews_test.go`: карточка в
  транзакции слова (и откат слова при сбое карточки), бэкфилл дважды,
  очередь на сидированных due (только свои, порядок, limit, 400/401/403),
  оценки good → 10 мин → review → again (переобучение), журнал, статус,
  повтор и гонка одного `review_id`, `not_due`, чужая карточка — 404,
  откат и накат 0021.
- Для S4-5 и S5-5: оценки cloze — `store.ReviewCard` с `Source: "cloze"`
  (правило `not_due` решает S4-5); очередь и «забытые» — `srs_cards`
  (`due_at`, `lapses`, `state`).

### S4-4 Cloze-генератор (2026-10-27) — ◐

- Решение — [ADR-037](../architecture/decisions.md): тест — выдача
  `GET /practice/cloze`, а не строка `cloze_tests`; пункт — свой у
  студента. Новых зависимостей и ключей `.env` нет, джоб и путей S3 нет.
- Миграция `backend/migrations/0022_cloze` (up/down): `cloze_items`
  (пункт по слову и предложению: FK на `user_terms` с каскадом, источник
  `lesson|material|generated|tatoeba` со ссылкой на урок, материал, текст
  или предложение Tatoeba, снимок предложения, пропуск `blank_start` +
  `blank_len`, `context_hash`, `distractors text[3]`; `UNIQUE (user_id,
  term_id, context_hash)`; `option_source` — набор вариантов, закреплённый
  при первой отдаче), `cloze_misses` (слова без нового предложения,
  `checked_at`), `cloze_distractors` (кэш LLM, S5-8 часть 2),
  `cloze_attempts` (журнал ответов, `UNIQUE (user_id, client_id)`,
  `review_id` → `srs_reviews`); хранимые лексемы `utterances.text_tsv`
  и `materials.body_tsv` (generated `to_tsvector('english', …)`) с GIN
  `utterances_text_tsv_idx`, `materials_body_tsv_idx`.
- `backend/internal/cloze/` — `Sentences`, `Find` (пропуск формы слова:
  ровно одно вхождение, не часть составного слова, 4–30 слов, до 200
  символов, без разметки), `Parts`, `Hash`, `PickDistractors` (словарь
  студента той же части речи, урок слова первым, затем 143 частых слова),
  `Options` (порядок по ключу пункта); тесты `cloze_test.go`,
  `distractors_test.go`. В `internal/lemma` — `WordSpans` (места слов
  токенизатора `Words`) и `POS` (части речи OEWN).
- Store (`backend/internal/store/cloze.go`): `ClozeTargets` (первые
  слова очереди: к показу, шаги обучения, остальные; `Checked` — свежая
  запись `cloze_misses`; слова со свежей записью и без пунктов в очереди
  нет), `RecordClozeMisses`, `ClozeWords`, `ClozeItemRefs` (`NoOptions` —
  пункт с LLM-набором без записи кэша),
  `ClozeLessonTexts` (реплики и видимые участникам материалы своих уроков:
  tsquery слов одним запросом, тексты — запросом на слово по хранимым
  лексемам; тест плана `TestClozeLessonTextsUseIndexes` в
  `backend/internal/store/cloze_test.go`), `ClozeOtherTexts` (свои
  `generated_texts`, Tatoeba через `Examples`), `CreateClozeItem`,
  `ClozeItems`, `LockClozeOptions`.
- `GET /practice/cloze?limit=` (student, 10, до 20;
  `backend/internal/httpapi/cloze.go`) → `{items[{id, before, after,
  options[], distractor_source: list|llm|pending, source, lesson_id,
  attribution: {author, license, license_url}, due}], due_total,
  llm_available}`; ответа (леммы, термина, перевода) и ссылок на
  tatoeba.org в выдаче нет. У слова — неотвеченный пункт, иначе новое
  предложение, иначе давний; предложение — не больше чем в одном пункте
  выдачи; очередь — до 200 слов одной выборкой, страницами; слово без
  нового предложения час не ищется (раньше — после нового материала,
  своего текста, импорта Tatoeba) и, пока у него нет пунктов, места
  в очереди не занимает. Набор вариантов пункта закрепляется при первой
  отдаче; с ключом LLM пункты после первого — `pending`; пункт
  с LLM-набором без записи кэша не отдаётся. У непохожего ключа
  (t-shirt, café) среди дистракторов словаря есть такой же непохожий
  (`PickDistractors`), иначе пункта у слова нет.
- DoD «по словарю урока — валидный тест ≥ 5 пунктов» — тест
  `TestClozeLessonDictionaryGivesTest` (`backend/internal/httpapi/cloze_test.go`):
  пять слов урока, назначенных через `SaveUserTerm`, дают пять пунктов из
  реплик урока, дистракторы — слова того же урока.
- **Для ☑ не хватает.** Выдача на живых данных не проверена: на стенде
  из четырёх источников предложений нет трёх — реплик без ключа
  AssemblyAI (S2-1…S2-3), своих текстов без `LLM_API_KEY` (S5-6) и
  Tatoeba до `make tatoeba-import` (S5-4); после выката открыть
  `/practice/cloze` в браузере у студента со словами урока и получить
  пункты из материалов, а с ключами и импортом — из реплик, своих текстов
  и Tatoeba (DoD).

### S4-5 Cloze-прохождение → FSRS (2026-10-27) — ◐

- `POST /practice/cloze/{id}/attempts {attempt_id, answer, mode:
  choice|typing, response_ms}` (student) → 201 `{item_id, term_id, lemma,
  answer, sentence, correct, exact, rating, counted, duplicate, status,
  card, translation, attribution}` / 200 повтор `attempt_id` / 409 `attempt_conflict`
  / 404 чужой или нет пункта (или слово убрали) / 400.
- Маппинг (`cloze.Check`, `cloze.Rate`): верны ключ термина (и когда
  `lemma.Lemma` уводит его в другое слово: rose → rise) и любая форма
  слова; неверно
  — again, верно не дольше порога — easy (выбор 5 с, ввод 8 с; подобрать
  по `cloze_attempts`), иначе good. Засчитывается ответ по карточке к
  показу и промах по любой; верный ответ раньше показа — только в журнал
  (`review_id` NULL). `not_due` у `POST /reviews` не тронут.
- Запись — `store.RecordClozeAttempt`: та же транзакция, что у карточек
  (`store.ReviewCard` разделён на `lockCard` и `applyReview`),
  `srs_reviews.source = 'cloze'`, статус слова по карточке.
- web: `/practice/cloze` (`web/app/practice/cloze/page.tsx`,
  `cloze-session.tsx`): текст до и после пропуска через `<ClickableText>`,
  после ответа — предложение целиком и перевод Tatoeba, ссылки на его
  страницу и перевод (до ответа — только автор и лицензия);
  варианты кнопками и клавишами 1–4 или ввод слова, Enter — дальше,
  прогресс «N из M», счётчики «верно/неверно», что стало с графиком;
  варианты `pending`-пунктов — одним `POST /practice/cloze/distractors`,
  пока их нет — «Подбираем варианты…» и ввод слова; время ответа такого
  пункта в режиме вариантов — от появления кнопок (`restartClock`).
  `web/lib/cloze-session.ts` (`distractorRequestIds`, `itemOptions`,
  `restartClock`, `clozeCredit`, `llmErrorNote` и др.) с тестами
  `npm test`; типы
  `ClozeItem`, `ClozePage`, `ClozeOptions`, `ClozeAttemptResult`
  в `web/lib/api.ts`; ссылка «Вставь слово» в шапке студента.
- Тесты правок ревью cloze (`backend/internal/httpapi/cloze_test.go`,
  `backend/internal/cloze/*_test.go`): ключ термина для rose, wound,
  found, bore, saw, fell, better выбором и вводом; 35 слов без
  предложений во главе очереди — полная выдача, запись `cloze_misses`
  и её устаревание через час; сброс записи своим текстом, импортом
  Tatoeba и подтверждением файла материала (новый транскрипт — нет);
  разные предложения в выдаче; материал без загруженного файла — не
  источник; свой сгенерированный текст — источник, чужой — нет; ответ
  у вариантов словаря — лемма, а не форма; конфликты `attempt_id` по
  пункту и режиму и гонка ключа до `UNIQUE` (409, оценка откатилась);
  каскады при удалении материала, отмене урока, удалении своего текста
  и предложения Tatoeba; составное слово, служебные слова и неизвестная
  часть речи в подборе дистракторов. Правки финального ревью:
  `TestClozeScanCapSkipsFreshMisses` (210 слов без предложений впереди:
  первая выдача `limit=1` — ровно 200 записей `cloze_misses`, вторая —
  полная), `TestClozeLLMSetWithoutCacheNotServed`,
  `TestClozeOddShapedAnswersStayOffLLM`, `TestClozeLockKeepsFirstSet`,
  сброс записи материалом без файла в `TestClozeMissCacheNewSources`,
  `TestPickDistractorsOddShapedTarget`, `TestLLMShaped`. Каждый новый
  тест убивает свой мутант (замер и мутанты — в результате задачи).
- Тесты — `backend/internal/httpapi/cloze_test.go`: свои слова и тексты
  (чужой урок не виден, урок слова раньше нового, материалы, Tatoeba,
  смена предложения после ответа), оценки easy → ранний верный не в
  графике → промах до показа (переобучение) → good → easy с картой,
  совпадающей с планировщиком, журнал `source = 'cloze'`, повтор и
  конфликт `attempt_id`, чужой пункт — 404, 401/403, 400, удаление слова,
  откат и накат 0022.
- **Для ☑ не хватает.** Прохождение в браузере не проверялось (тесты —
  API и `web/lib/cloze-session.test.ts`): на стенде пройти пункты выбором
  (кнопки и клавиши 1–4) и вводом слова и убедиться, что ответ по
  карточке к показу и промах сдвинули её `due_at` (DoD); пункты будут
  только из источников, которые есть на стенде (S4-4).

## Риски этапа

Неправильный маппинг cloze→FSRS ломает график повторений — правило
зафиксировано в [practice-engine.md](../architecture/flows/practice-engine.md);
порог «быстро» для easy подобрать на реальных попытках (ADR при выборе).
