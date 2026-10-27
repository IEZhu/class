package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/srs"
)

// Карточки FSRS и журнал оценок (S4-1…S4-3, ADR-036). График считает
// internal/srs; здесь — хранение, очередь и запись оценки.

var (
	// ErrNotDue — карточка ещё не к показу: оценка из второй вкладки или
	// повтор уже засчитанной оценки под новым ключом
	ErrNotDue = errors.New("card is not due")
	// ErrReviewConflict — ключ оценки уже занят другой оценкой (другое
	// слово или другая оценка) этого же пользователя
	ErrReviewConflict = errors.New("review id is taken by another review")
)

// cardColumns — колонки srs.Card в порядке cardArgs и scanCard. Ни
// у user_terms, ни у terms колонок с такими именами нет, поэтому список
// годится и в запросах с JOIN.
const cardColumns = `due_at, stability, difficulty, scheduled_days, reps, lapses, state, remaining_steps, last_review_at`

func cardArgs(c srs.Card) []any {
	return []any{c.Due, c.Stability, c.Difficulty, c.ScheduledDays, c.Reps, c.Lapses, c.State, c.RemainingSteps, c.LastReview}
}

func cardDest(c *srs.Card) []any {
	return []any{&c.Due, &c.Stability, &c.Difficulty, &c.ScheduledDays, &c.Reps, &c.Lapses, &c.State, &c.RemainingSteps, &c.LastReview}
}

// insertSRSCard — карточка нового слова в транзакции записи user_terms.
func insertSRSCard(ctx context.Context, tx pgx.Tx, userID, termID int64, c srs.Card) error {
	_, err := tx.Exec(ctx, `INSERT INTO srs_cards (user_id, term_id, `+cardColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, append([]any{userID, termID}, cardArgs(c)...)...)
	return err
}

// BackfillSRSCards заводит карточки словам словаря, у которых их нет:
// словам до миграции 0021 и добавленным старым api, пока шёл выкат
// (S4-1, долг D-3). Карточка — как srs.NewCard на момент добавления
// слова: старые слова встают в очередь первыми. Идемпотентно: у слова
// с карточкой ничего не меняется. Возвращает, сколько карточек заведено.
func (s *Store) BackfillSRSCards(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO srs_cards (user_id, term_id, due_at, state)
		 SELECT user_id, term_id, added_at, $1 FROM user_terms
		 ON CONFLICT (user_id, term_id) DO NOTHING`, srs.StateNew)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// QueueCard — карточка очереди повторений со словом словаря.
type QueueCard struct {
	TermID  int64
	Lemma   string
	Surface string
	// Translation — перевод из кэша термина; nil — слово не переводили
	Translation *string
	Status      int
	Card        srs.Card
}

// DueCards — карточки пользователя, чей показ наступил к now: самые
// просроченные первыми, не больше limit.
func (s *Store) DueCards(ctx context.Context, userID int64, now time.Time, lang string, limit int) ([]QueueCard, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.term_id, t.lemma, t.surface, t.translation_cache->$3::text->>'text', ut.status, `+cardColumns+`
		 FROM srs_cards c
		 JOIN user_terms ut USING (user_id, term_id)
		 JOIN terms t ON t.id = c.term_id
		 WHERE c.user_id = $1 AND c.due_at <= $2
		 ORDER BY c.due_at, c.term_id
		 LIMIT $4`, userID, now, lang, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (QueueCard, error) {
		var q QueueCard
		dest := append([]any{&q.TermID, &q.Lemma, &q.Surface, &q.Translation, &q.Status}, cardDest(&q.Card)...)
		return q, row.Scan(dest...)
	})
}

// DueStats — сколько карточек пользователя к показу на now и когда
// наступит ближайший из будущих показов; nil — будущих нет.
func (s *Store) DueStats(ctx context.Context, userID int64, now time.Time) (due int, next *time.Time, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE due_at <= $2), min(due_at) FILTER (WHERE due_at > $2)
		 FROM srs_cards WHERE user_id = $1`, userID, now).Scan(&due, &next)
	return due, next, err
}

// Review — оценка карточки (user_id, term_id) в момент At.
type Review struct {
	UserID int64
	TermID int64
	// ClientID — ключ идемпотентности (uuid); пусто — без ключа
	ClientID string
	Rating   srs.Rating
	Source   string
	At       time.Time
}

// ReviewResult — карточка и статус слова после оценки. Duplicate — ключ
// уже засчитан: график не тронут, карточка — как сейчас в базе.
type ReviewResult struct {
	Card      srs.Card
	Status    int
	Duplicate bool
}

// ReviewCard записывает оценку: карточка пересчитывается планировщиком,
// оценка ложится в srs_reviews, статус слова — по карточке (srs.Status),
// всё в одной транзакции. ErrNotFound — у пользователя нет карточки
// этого слова (в том числе чужая), ErrNotDue — показ ещё не наступил,
// ErrReviewConflict — ключ занят другой оценкой.
func (s *Store) ReviewCard(ctx context.Context, sched *srs.Scheduler, r Review) (*ReviewResult, error) {
	var res *ReviewResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Строки берутся в порядке удаления слова (user_terms, затем
		// каскадом карточка): параллельное «убрать из словаря» ждёт,
		// а не ловит взаимную блокировку. Оценки одной карточки идут
		// друг за другом, поэтому повтор ключа видит засчитанную оценку
		var status int
		err := tx.QueryRow(ctx,
			`SELECT status FROM user_terms WHERE user_id = $1 AND term_id = $2 FOR NO KEY UPDATE`,
			r.UserID, r.TermID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var card srs.Card
		err = tx.QueryRow(ctx, `SELECT `+cardColumns+` FROM srs_cards
			WHERE user_id = $1 AND term_id = $2 FOR NO KEY UPDATE`, r.UserID, r.TermID).Scan(cardDest(&card)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if r.ClientID != "" {
			dup, err := reviewByClientID(ctx, tx, r)
			if err != nil || dup {
				res = &ReviewResult{Card: card, Status: status, Duplicate: dup}
				return err
			}
		}
		if card.Due.After(r.At) {
			return ErrNotDue
		}
		next, err := sched.Review(card, r.Rating, r.At)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE srs_cards SET (`+cardColumns+`) = ($3, $4, $5, $6, $7, $8, $9, $10, $11)
			WHERE user_id = $1 AND term_id = $2`, append([]any{r.UserID, r.TermID}, cardArgs(next)...)...); err != nil {
			return err
		}
		var clientID *string
		if r.ClientID != "" {
			clientID = &r.ClientID
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO srs_reviews (user_id, term_id, client_id, rating, source, reviewed_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`, r.UserID, r.TermID, clientID, r.Rating, r.Source, r.At)
		if PgErrorCode(err) == "23505" { // unique_violation
			// Тот же ключ одновременно с другим словом: строки разные,
			// блокировки друг друга не ждали, развёл уникальный индекс
			return ErrReviewConflict
		}
		if err != nil {
			return err
		}
		// updated_at — только при смене статуса, как у SaveUserTerm
		status = srs.Status(next)
		if _, err := tx.Exec(ctx,
			`UPDATE user_terms SET status = $3,
			     updated_at = CASE WHEN status = $3 THEN updated_at ELSE now() END
			 WHERE user_id = $1 AND term_id = $2`, r.UserID, r.TermID, status); err != nil {
			return err
		}
		res = &ReviewResult{Card: next, Status: status}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// reviewByClientID — ключ уже засчитан той же оценкой того же слова
// (true) или свободен (false); занят другой оценкой — ErrReviewConflict.
func reviewByClientID(ctx context.Context, tx pgx.Tx, r Review) (bool, error) {
	var termID int64
	var rating srs.Rating
	err := tx.QueryRow(ctx,
		`SELECT term_id, rating FROM srs_reviews WHERE user_id = $1 AND client_id = $2`,
		r.UserID, r.ClientID).Scan(&termID, &rating)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	case termID != r.TermID || rating != r.Rating:
		return false, ErrReviewConflict
	}
	return true, nil
}
