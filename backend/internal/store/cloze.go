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
}

// ClozeTargets — слова пользователя по очереди упражнения: сначала те,
// чей показ наступил (самые просроченные первыми), затем карточки на
// шагах обучения и переобучения, затем остальные по ближайшему показу.
func (s *Store) ClozeTargets(ctx context.Context, userID int64, now time.Time, limit int) ([]ClozeTarget, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.term_id, t.lemma, ut.source_lesson_id
		 FROM srs_cards c
		 JOIN user_terms ut USING (user_id, term_id)
		 JOIN terms t ON t.id = c.term_id
		 WHERE c.user_id = $1
		 ORDER BY c.due_at <= $2 DESC, c.state IN ('learning', 'relearning') DESC, c.due_at, c.term_id
		 LIMIT $3`, userID, now, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClozeTarget, error) {
		var t ClozeTarget
		return t, row.Scan(&t.TermID, &t.Lemma, &t.LessonID)
	})
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

// ClozeItemRef — пункт пользователя по слову: ключ и сколько раз на него
// отвечали.
type ClozeItemRef struct {
	ID          int64
	TermID      int64
	ContextHash []byte
	Attempts    int
	// LastAttemptAt — nil, если ответов не было
	LastAttemptAt *time.Time
}

// ClozeItemRefs — пункты пользователя по словам termIDs, старые первыми.
func (s *Store) ClozeItemRefs(ctx context.Context, userID int64, termIDs []int64) ([]ClozeItemRef, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id, i.term_id, i.context_hash, count(a.id), max(a.created_at)
		 FROM cloze_items i LEFT JOIN cloze_attempts a ON a.item_id = i.id
		 WHERE i.user_id = $1 AND i.term_id = ANY($2)
		 GROUP BY i.id ORDER BY i.id`, userID, termIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClozeItemRef, error) {
		var r ClozeItemRef
		return r, row.Scan(&r.ID, &r.TermID, &r.ContextHash, &r.Attempts, &r.LastAttemptAt)
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

// ClozeLessonTexts — тексты уроков пользователя для каждого запроса
// (в порядке queries): реплики транскриптов, затем материалы; урок слова
// первым, затем новые уроки. Урок — тот, где пользователь в снапшоте
// участников, как у вьюера транскрипта. Индекса FTS у реплик нет,
// поэтому tsvector каждой реплики и материала считается один раз на всю
// пачку слов (MATERIALIZED), а не на каждое. Точное вхождение проверяет
// cloze.Find.
func (s *Store) ClozeLessonTexts(ctx context.Context, userID int64, queries []ClozeQuery) ([][]ClozeText, error) {
	out := make([][]ClozeText, len(queries))
	if len(queries) == 0 {
		return out, nil
	}
	var words []string
	var ords []int32
	lessons := make([]*int64, len(queries))
	for i, q := range queries {
		for _, w := range q.Words {
			words, ords = append(words, w), append(ords, int32(i))
		}
		lessons[i] = q.LessonID
	}
	rows, err := s.pool.Query(ctx, `
		WITH want AS (
		    SELECT w.ord, string_agg('(' || t::text || ')', ' | ')::tsquery AS query
		    FROM unnest($2::text[], $3::int[]) AS w (word, ord), plainto_tsquery('english', w.word) AS t
		    WHERE numnode(t) > 0
		    GROUP BY w.ord
		), lesson AS MATERIALIZED (
		    SELECT l.id, l.starts_at FROM lesson_participants lp JOIN lessons l ON l.id = lp.lesson_id
		    WHERE lp.user_id = $1
		), doc AS MATERIALIZED (
		    SELECT 0 AS kind, l.id AS lesson_id, l.starts_at, NULL::bigint AS material_id, u.idx AS pos,
		           u.text, to_tsvector('english', u.text) AS tsv
		    FROM lesson l JOIN transcripts tr ON tr.lesson_id = l.id JOIN utterances u ON u.transcript_id = tr.id
		    UNION ALL
		    SELECT 1, l.id, l.starts_at, m.id, m.id, m.body_md, to_tsvector('english', m.body_md)
		    FROM lesson l JOIN materials m ON m.lesson_id = l.id
		)
		SELECT want.ord, x.kind, x.lesson_id, x.material_id, x.text
		FROM want CROSS JOIN LATERAL (
		    SELECT * FROM (
		        SELECT d.kind, d.lesson_id, d.material_id, d.text,
		               row_number() OVER (PARTITION BY d.kind
		                   ORDER BY d.lesson_id IS NOT DISTINCT FROM ($4::bigint[])[want.ord + 1] DESC,
		                            d.starts_at DESC, d.lesson_id, d.pos) AS rn
		        FROM doc d WHERE d.tsv @@ want.query
		    ) ranked WHERE rn <= $5
		) x
		ORDER BY want.ord, x.kind, x.rn`,
		userID, words, ords, lessons, clozeLessonTexts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ord, kind int
		var t ClozeText
		if err := rows.Scan(&ord, &kind, &t.LessonID, &t.MaterialID, &t.Text); err != nil {
			return nil, err
		}
		t.Source = ClozeSourceLesson
		if kind == 1 {
			t.Source = ClozeSourceMaterial
		}
		out[ord] = append(out[ord], t)
	}
	return out, rows.Err()
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
			&it.Distractors, &it.LLM, &it.Due, &author, &translationID, &translation, &translationAuthor)
		if err == nil && it.TatoebaID != nil && translationID != nil {
			it.Example = &Example{
				ID: *it.TatoebaID, Text: it.Sentence, Author: *author,
				TranslationID: *translationID, Translation: *translation, TranslationAuthor: *translationAuthor,
			}
		}
		return it, err
	})
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
