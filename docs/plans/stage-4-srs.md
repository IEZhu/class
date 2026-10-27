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

## Риски этапа

Неправильный маппинг cloze→FSRS ломает график повторений — правило
зафиксировано в [practice-engine.md](../architecture/flows/practice-engine.md);
порог «быстро» для easy подобрать на реальных попытках (ADR при выборе).
