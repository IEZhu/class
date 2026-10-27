package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/cloze"
	"github.com/IEZhu/class/backend/internal/srs"
)

// Cloze (S4-4, S4-5, S5-8 часть 2; ADR-037): цели упражнения, источники
// предложений, пункты, кэш LLM-дистракторов и запись ответа в график
// FSRS. Пропуск и варианты выбирает internal/cloze; здесь — хранение.

// Источники предложения пункта (cloze_items.source).
const (
	ClozeSourceLesson    = "lesson"
	ClozeSourceMaterial  = "material"
	ClozeSourceGenerated = "generated"
	ClozeSourceTatoeba   = "tatoeba"
)

// ErrAttemptConflict — ключ попытки уже занят другим ответом (другой
// пункт или другой ответ) этого же пользователя.
var ErrAttemptConflict = errors.New("attempt id is taken by another attempt")

// ClozeTarget — слово словаря, по которому строится пункт.
type ClozeTarget struct {
	TermID int64
	Lemma  string
	// LessonID — урок, с которого слово пришло в словарь; nil — вне урока
	LessonID *int64
	// Checked — слову недавно искали новое предложение и не нашли
	// (свежая запись cloze_misses): искать снова рано
	Checked bool
}

// ClozeTargets — страница слов пользователя по очереди упражнения
// (limit слов после offset): сначала те, чей показ наступил (самые
// просроченные первыми), затем карточки на шагах обучения
// и переобучения, затем остальные по ближайшему показу. Checked —
// запись cloze_misses позже staleBefore и позже новых источников
// пользователя (ADR-037): материала его урока или подтверждения файла
// материала, своего сгенерированного текста, законченного импорта
// Tatoeba. Реплик среди источников нет — у транскрипта нет времени
// готовности, новые реплики слово получит, когда запись устареет.
func (s *Store) ClozeTargets(ctx context.Context, userID int64, now, staleBefore time.Time, limit, offset int) ([]ClozeTarget, error) {
	rows, err := s.pool.Query(ctx,
		`WITH fresh AS (
		     SELECT greatest($3::timestamptz,
		         (SELECT max(greatest(m.created_at, m.file_uploaded_at))
		          FROM lesson_participants lp JOIN materials m ON m.lesson_id = lp.lesson_id
		          WHERE lp.user_id = $1),
		         (SELECT max(g.created_at) FROM generated_texts g WHERE g.user_id = $1),
		         (SELECT max(ti.finished_at) FROM tatoeba_imports ti)) AS since
		 )
		 SELECT c.term_id, t.lemma, ut.source_lesson_id, coalesce(cm.checked_at > fresh.since, false)
		 FROM srs_cards c
		 JOIN user_terms ut USING (user_id, term_id)
		 JOIN terms t ON t.id = c.term_id
		 CROSS JOIN fresh
		 LEFT JOIN cloze_misses cm ON cm.user_id = c.user_id AND cm.term_id = c.term_id
		 WHERE c.user_id = $1
		 ORDER BY c.due_at <= $2 DESC, c.state IN ('learning', 'relearning') DESC, c.due_at, c.term_id
		 LIMIT $4 OFFSET $5`, userID, now, staleBefore, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClozeTarget, error) {
		var t ClozeTarget
		return t, row.Scan(&t.TermID, &t.Lemma, &t.LessonID, &t.Checked)
	})
}

// RecordClozeMisses запоминает, что словам termIDs при проверке в at не
// нашлось нового годного предложения (cloze_misses). Слово, которое уже
// убрали из словаря, пропускается.
func (s *Store) RecordClozeMisses(ctx context.Context, userID int64, termIDs []int64, at time.Time) error {
	if len(termIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cloze_misses (user_id, term_id, checked_at)
		 SELECT user_id, term_id, $3 FROM user_terms WHERE user_id = $1 AND term_id = ANY($2)
		 ON CONFLICT (user_id, term_id) DO UPDATE SET checked_at = EXCLUDED.checked_at`, userID, termIDs, at)
	if PgErrorCode(err) == "23503" { // foreign_key_violation: слово убрали между выборкой и вставкой
		return nil
	}
	return err
}

// ClozeWords — словарь пользователя (леммы и урок-источник): запас
// дистракторов из своего списка слов.
func (s *Store) ClozeWords(ctx context.Context, userID int64) ([]cloze.Word, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.lemma, ut.source_lesson_id FROM user_terms ut JOIN terms t ON t.id = ut.term_id
		 WHERE ut.user_id = $1 ORDER BY t.lemma`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (cloze.Word, error) {
		var w cloze.Word
		return w, row.Scan(&w.Lemma, &w.LessonID)
	})
}

// ClozeItemRef — пункт пользователя по слову: ключ, предложение и
// сколько раз на него отвечали.
type ClozeItemRef struct {
	ID          int64
	TermID      int64
	ContextHash []byte
	Sentence    string
	Attempts    int
	// LastAttemptAt — nil, если ответов не было
	LastAttemptAt *time.Time
}

// ClozeItemRefs — пункты пользователя по словам termIDs, старые первыми.
func (s *Store) ClozeItemRefs(ctx context.Context, userID int64, termIDs []int64) ([]ClozeItemRef, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id, i.term_id, i.context_hash, i.sentence, count(a.id), max(a.created_at)
		 FROM cloze_items i LEFT JOIN cloze_attempts a ON a.item_id = i.id
		 WHERE i.user_id = $1 AND i.term_id = ANY($2)
		 GROUP BY i.id ORDER BY i.id`, userID, termIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClozeItemRef, error) {
		var r ClozeItemRef
		return r, row.Scan(&r.ID, &r.TermID, &r.ContextHash, &r.Sentence, &r.Attempts, &r.LastAttemptAt)
	})
}

// ClozeRef — источник предложения пункта: вид (cloze_items.source)
// и ссылка ровно на свой источник; у реплики и материала — их урок.
type ClozeRef struct {
	Source          string
	LessonID        *int64
	MaterialID      *int64
	GeneratedTextID *int64
	TatoebaID       *int64
}

// ClozeText — текст, из которого режутся предложения пункта.
type ClozeText struct {
	ClozeRef
	Text string
}

const (
	// clozeLessonTexts — реплик и отдельно материалов уроков на слово
	clozeLessonTexts = 40
	// clozeOtherTexts — своих сгенерированных текстов и предложений
	// Tatoeba на слово
	clozeOtherTexts = 10
)

// ClozeQuery — что искать в текстах уроков для одного слова: лемма и её
// неправильные формы (стеммер english находит runs и running, но не
// ran) и урок, с которого слово пришло (его тексты первыми).
type ClozeQuery struct {
	Words    []string
	LessonID *int64
}

// clozeQueriesSQL — tsquery каждого запроса ($2 — его номер у слова $1):
// лемма и формы через «или».
const clozeQueriesSQL = `
	SELECT w.ord, string_agg('(' || t::text || ')', ' | ')
	FROM unnest($1::text[], $2::int[]) AS w (word, ord), plainto_tsquery('english', w.word) AS t
	WHERE numnode(t) > 0
	GROUP BY w.ord ORDER BY w.ord`

// clozeWordTextsSQL — тексты уроков пользователя $1 со словом $2
// (tsquery): до $4 реплик транскриптов и до $4 материалов, урок слова
// ($3) первым, затем новые уроки. Урок — тот, где пользователь
// в снапшоте участников, как у вьюера транскрипта; материал — тот, что
// видят участники (visibleMaterials: без файла или с подтверждённой
// загрузкой). Слово сверяется с хранимыми лексемами (text_tsv, body_tsv,
// 0022), а не с to_tsvector, пересчитанным в запросе (ADR-037).
const clozeWordTextsSQL = `
	WITH lesson AS MATERIALIZED (
	    SELECT l.id, l.starts_at, l.id IS NOT DISTINCT FROM $3::bigint AS own
	    FROM lesson_participants lp JOIN lessons l ON l.id = lp.lesson_id
	    WHERE lp.user_id = $1
	)
	SELECT kind, lesson_id, material_id, text FROM (
	(SELECT 0 AS kind, l.id AS lesson_id, NULL::bigint AS material_id, u.text, l.own, l.starts_at, u.idx::bigint AS pos
	 FROM utterances u JOIN transcripts tr ON tr.id = u.transcript_id JOIN lesson l ON l.id = tr.lesson_id
	 WHERE u.text_tsv @@ $2::tsquery
	 ORDER BY own DESC, starts_at DESC, lesson_id, pos
	 LIMIT $4)
	UNION ALL
	(SELECT 1 AS kind, l.id AS lesson_id, m.id AS material_id, m.body_md AS text, l.own, l.starts_at, m.id AS pos
	 FROM materials m JOIN lesson l ON l.id = m.lesson_id
	 WHERE m.body_tsv @@ $2::tsquery AND (m.s3_key IS NULL OR m.file_uploaded_at IS NOT NULL)
	 ORDER BY own DESC, starts_at DESC, lesson_id, pos
	 LIMIT $4)
	) x
	ORDER BY kind, own DESC, starts_at DESC, lesson_id, pos`

// ClozeLessonTexts — тексты уроков пользователя для каждого запроса
// (в порядке queries): реплики транскриптов, затем материалы. tsquery
// запросов считается одним вызовом, тексты — запросом на слово
// (clozeWordTextsSQL) безымянным оператором: план строится под само
// слово, и планировщик по статистике лексем решает, идти ли по
// GIN-индексу (редкое слово) или по своим урокам (частое слово, короткая
// история). Точное вхождение проверяет cloze.Find.
func (s *Store) ClozeLessonTexts(ctx context.Context, userID int64, queries []ClozeQuery) ([][]ClozeText, error) {
	out := make([][]ClozeText, len(queries))
	if len(queries) == 0 {
		return out, nil
	}
	var words []string
	var ords []int32
	for i, q := range queries {
		for _, w := range q.Words {
			words, ords = append(words, w), append(ords, int32(i))
		}
	}
	rows, err := s.pool.Query(ctx, clozeQueriesSQL, words, ords)
	if err != nil {
		return nil, err
	}
	type wordQuery struct {
		ord   int
		query string
	}
	tsqueries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (wordQuery, error) {
		var q wordQuery
		return q, row.Scan(&q.ord, &q.query)
	})
	if err != nil {
		return nil, err
	}
	for _, q := range tsqueries {
		if out[q.ord], err = s.clozeWordTexts(ctx, userID, q.query, queries[q.ord].LessonID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// clozeWordTexts — тексты уроков пользователя со словом (clozeWordTextsSQL).
func (s *Store) clozeWordTexts(ctx context.Context, userID int64, tsquery string, lessonID *int64) ([]ClozeText, error) {
	rows, err := s.pool.Query(ctx, clozeWordTextsSQL, pgx.QueryExecModeExec, userID, tsquery, lessonID, clozeLessonTexts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClozeText, error) {
		var kind int
		var t ClozeText
		err := row.Scan(&kind, &t.LessonID, &t.MaterialID, &t.Text)
		t.Source = ClozeSourceLesson
		if kind == 1 {
			t.Source = ClozeSourceMaterial
		}
		return t, err
	})
}

// ClozeOtherTexts — тексты со словом вне уроков: свои сгенерированные
// тексты (новые первыми) и предложения Tatoeba с переводом (короткие
// первыми, store.Examples) — оба по леммам под GIN.
func (s *Store) ClozeOtherTexts(ctx context.Context, userID int64, lemma string) ([]ClozeText, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, body FROM generated_texts WHERE user_id = $1 AND lemmas @> ARRAY[$2::text]
		 ORDER BY created_at DESC, id DESC LIMIT $3`, userID, lemma, clozeOtherTexts)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClozeText, error) {
		t := ClozeText{ClozeRef: ClozeRef{Source: ClozeSourceGenerated}}
		return t, row.Scan(&t.GeneratedTextID, &t.Text)
	})
	if err != nil {
		return nil, err
	}
	examples, err := s.Examples(ctx, lemma, clozeOtherTexts)
	if err != nil {
		return nil, err
	}
	for _, e := range examples {
		out = append(out, ClozeText{ClozeRef: ClozeRef{Source: ClozeSourceTatoeba, TatoebaID: &e.ID}, Text: e.Text})
	}
	return out, nil
}

// ClozeItem — пункт cloze со словом, кэшем LLM и подписью источника.
type ClozeItem struct {
	ID     int64
	UserID int64
	TermID int64
	// Lemma — лемма термина: ответ пункта
	Lemma string
	ClozeRef
	Sentence    string
	Blank       cloze.Blank
	ContextHash []byte
	// Distractors — из словаря или частых слов (леммы)
	Distractors []string
	// LLM — проверенные LLM-дистракторы из кэша; nil — их нет
	LLM []string
	// OptionSource — набор вариантов, закреплённый за пунктом
	// (cloze_items.option_source): list, llm или пусто — ещё не отдавали
	OptionSource string
	// Due — показ карточки слова наступил: ответ ляжет в график FSRS
	Due bool
	// Example — предложение Tatoeba с переводом и авторами (подпись
	// CC BY); nil — источник не Tatoeba или перевода больше нет
	Example *Example
}

// CreateClozeItem сохраняет новый пункт и возвращает его id; такой пункт
// уже есть (параллельная выдача) — id существующего. ErrNotFound —
// слова уже нет в словаре.
func (s *Store) CreateClozeItem(ctx context.Context, it *ClozeItem) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO cloze_items (user_id, term_id, source, lesson_id, material_id, generated_text_id, tatoeba_id,
		     sentence, blank_start, blank_len, context_hash, distractors)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (user_id, term_id, context_hash) DO NOTHING
		 RETURNING id`,
		it.UserID, it.TermID, it.Source, it.LessonID, it.MaterialID, it.GeneratedTextID, it.TatoebaID,
		it.Sentence, it.Blank.Start, it.Blank.Len, it.ContextHash, it.Distractors).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.pool.QueryRow(ctx,
			`SELECT id FROM cloze_items WHERE user_id = $1 AND term_id = $2 AND context_hash = $3`,
			it.UserID, it.TermID, it.ContextHash).Scan(&id)
	}
	if PgErrorCode(err) == "23503" { // foreign_key_violation: слово убрали
		return 0, ErrNotFound
	}
	return id, err
}

// ClozeItems — пункты пользователя по ids в их порядке; чужих и
// несуществующих в ответе нет. Due — по now.
func (s *Store) ClozeItems(ctx context.Context, userID int64, ids []int64, now time.Time) ([]ClozeItem, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id, i.user_id, i.term_id, t.lemma, i.source, i.lesson_id, i.material_id, i.generated_text_id,
		        i.tatoeba_id, i.sentence, i.blank_start, i.blank_len, i.context_hash, i.distractors, d.options,
		        coalesce(i.option_source, ''),
		        coalesce(c.due_at <= $3, false), ts.author, r.id, r.text, r.author
		 FROM unnest($2::bigint[]) WITH ORDINALITY AS want (id, ord)
		 JOIN cloze_items i ON i.id = want.id AND i.user_id = $1
		 JOIN terms t ON t.id = i.term_id
		 LEFT JOIN srs_cards c ON c.user_id = i.user_id AND c.term_id = i.term_id
		 LEFT JOIN cloze_distractors d ON d.term_id = i.term_id AND d.context_hash = i.context_hash
		 LEFT JOIN tatoeba_sentences ts ON ts.id = i.tatoeba_id
		 LEFT JOIN LATERAL (
		     SELECT ru.id, ru.text, ru.author FROM tatoeba_links l
		     JOIN tatoeba_sentences ru ON ru.id = l.ru_id
		     WHERE l.en_id = i.tatoeba_id ORDER BY ru.id LIMIT 1
		 ) r ON true
		 ORDER BY want.ord`, userID, ids, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClozeItem, error) {
		var it ClozeItem
		var author, translation, translationAuthor *string
		var translationID *int64
		err := row.Scan(&it.ID, &it.UserID, &it.TermID, &it.Lemma, &it.Source, &it.LessonID, &it.MaterialID,
			&it.GeneratedTextID, &it.TatoebaID, &it.Sentence, &it.Blank.Start, &it.Blank.Len, &it.ContextHash,
			&it.Distractors, &it.LLM, &it.OptionSource, &it.Due, &author, &translationID, &translation, &translationAuthor)
		if err == nil && it.TatoebaID != nil && translationID != nil {
			it.Example = &Example{
				ID: *it.TatoebaID, Text: it.Sentence, Author: *author,
				TranslationID: *translationID, Translation: *translation, TranslationAuthor: *translationAuthor,
			}
		}
		return it, err
	})
}

// Наборы вариантов пункта (cloze_items.option_source).
const (
	ClozeOptionsList = "list"
	ClozeOptionsLLM  = "llm"
)

// LockClozeOptions закрепляет за пунктами пользователя набор вариантов
// при первой отдаче (ADR-037): list — дистракторы словаря, llm —
// LLM-дистракторы из кэша (закрепляются, только если запись кэша есть).
// Пункт с уже закреплённым набором (параллельная выдача) не меняется:
// после вызова набор пункта читается заново через ClozeItems.
func (s *Store) LockClozeOptions(ctx context.Context, userID int64, list, llm []int64) error {
	if len(llm) > 0 {
		if _, err := s.pool.Exec(ctx,
			`UPDATE cloze_items i SET option_source = 'llm' FROM cloze_distractors d
			 WHERE i.user_id = $1 AND i.id = ANY($2) AND i.option_source IS NULL
			   AND d.term_id = i.term_id AND d.context_hash = i.context_hash`, userID, llm); err != nil {
			return err
		}
	}
	if len(list) > 0 {
		if _, err := s.pool.Exec(ctx,
			`UPDATE cloze_items SET option_source = 'list'
			 WHERE user_id = $1 AND id = ANY($2) AND option_source IS NULL`, userID, list); err != nil {
			return err
		}
	}
	return nil
}

// SaveClozeDistractors кладёт проверенные LLM-дистракторы слова
// в предложении в общий кэш; запись уже есть (параллельный вызов) —
// остаётся первая.
func (s *Store) SaveClozeDistractors(ctx context.Context, termID int64, contextHash []byte, options []string, provider, model string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cloze_distractors (term_id, context_hash, options, provider, model)
		 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (term_id, context_hash) DO NOTHING`,
		termID, contextHash, options, provider, model)
	return err
}

// ClozeAttempt — ответ на пункт; Correct и Rating посчитаны по пункту
// (cloze.Check, cloze.Rate), At — часы api.
type ClozeAttempt struct {
	UserID int64
	ItemID int64
	// ClientID — ключ идемпотентности (uuid)
	ClientID   string
	Mode       cloze.Mode
	Answer     string
	Correct    bool
	ResponseMS int
	Rating     srs.Rating
	At         time.Time
}

// ClozeAttemptResult — что стало с ответом. Counted — ответ ушёл в
// график FSRS (srs_reviews, source cloze); Duplicate — ключ уже засчитан:
// результат — первой попытки, карточка — как сейчас в базе.
type ClozeAttemptResult struct {
	TermID    int64
	Correct   bool
	Rating    srs.Rating
	Counted   bool
	Duplicate bool
	Card      srs.Card
	Status    int
}

// RecordClozeAttempt записывает ответ в журнал cloze_attempts и, если
// он засчитывается, — оценкой карточки слова, одной транзакцией
// (ADR-037). Засчитывается ответ по карточке, чей показ наступил, и
// промах по любой: правило not_due карточек (ADR-036) защищает график от
// повторной оценки, а промах — новое знание о слове, которое FSRS
// должен получить сразу. Верный ответ раньше показа только пишется
// в журнал: иначе зубрёжка одного слова отодвигала бы его показ.
// ErrNotFound — пункта нет у пользователя или слово убрали из словаря,
// ErrAttemptConflict — ключ занят другим ответом.
func (s *Store) RecordClozeAttempt(ctx context.Context, sched *srs.Scheduler, a ClozeAttempt) (*ClozeAttemptResult, error) {
	var res *ClozeAttemptResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var termID int64
		err := tx.QueryRow(ctx, `SELECT term_id FROM cloze_items WHERE id = $1 AND user_id = $2`,
			a.ItemID, a.UserID).Scan(&termID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		status, card, err := lockCard(ctx, tx, a.UserID, termID)
		if err != nil {
			return err
		}
		// Повтор ключа — после блокировок, как у ReviewCard: параллельный
		// дубль по тому же слову ждёт и видит засчитанную попытку
		var prev struct {
			itemID   int64
			mode     cloze.Mode
			answer   string
			correct  bool
			rating   srs.Rating
			reviewID *int64
		}
		err = tx.QueryRow(ctx,
			`SELECT item_id, mode, answer, correct, rating, review_id FROM cloze_attempts
			 WHERE user_id = $1 AND client_id = $2`, a.UserID, a.ClientID).
			Scan(&prev.itemID, &prev.mode, &prev.answer, &prev.correct, &prev.rating, &prev.reviewID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case prev.itemID != a.ItemID || prev.mode != a.Mode || prev.answer != a.Answer:
			return ErrAttemptConflict
		default:
			res = &ClozeAttemptResult{
				TermID: termID, Correct: prev.correct, Rating: prev.rating, Counted: prev.reviewID != nil,
				Duplicate: true, Card: card, Status: status,
			}
			return nil
		}
		res = &ClozeAttemptResult{TermID: termID, Correct: a.Correct, Rating: a.Rating, Card: card, Status: status}
		var reviewID *int64
		if res.Counted = !card.Due.After(a.At) || a.Rating == srs.Again; res.Counted {
			rr, id, err := applyReview(ctx, tx, sched, Review{
				UserID: a.UserID, TermID: termID, Rating: a.Rating, Source: srs.SourceCloze, At: a.At,
			}, card)
			if err != nil {
				return err
			}
			res.Card, res.Status, reviewID = rr.Card, rr.Status, &id
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO cloze_attempts (item_id, user_id, client_id, mode, answer, correct, response_ms, rating, review_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			a.ItemID, a.UserID, a.ClientID, a.Mode, a.Answer, a.Correct, a.ResponseMS, a.Rating, reviewID)
		if PgErrorCode(err) == "23505" { // unique_violation
			// Тот же ключ одновременно с ответом по другому слову: строки
			// разные, блокировки друг друга не ждали
			return ErrAttemptConflict
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
