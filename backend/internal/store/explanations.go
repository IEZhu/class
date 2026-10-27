package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Explanation — строка term_explanations (S5-8, ADR-039): объяснение
// термина в нормализованном предложении.
type Explanation struct {
	TermID      int64
	ContextHash []byte
	Context     string
	Body        string
	Provider    string
	Model       string
	CreatedAt   time.Time
}

// TermExplanation — объяснение из кэша; ErrNotFound — его ещё нет.
func (s *Store) TermExplanation(ctx context.Context, termID int64, contextHash []byte) (*Explanation, error) {
	e := &Explanation{}
	err := s.pool.QueryRow(ctx,
		`SELECT term_id, context_hash, context, body, provider, model, created_at
		 FROM term_explanations WHERE term_id = $1 AND context_hash = $2`, termID, contextHash).
		Scan(&e.TermID, &e.ContextHash, &e.Context, &e.Body, &e.Provider, &e.Model, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// SaveTermExplanation кладёт объяснение в кэш. Параллельный запрос
// с тем же ключом мог записать своё раньше — тогда e заменяется
// записанным: оба ответа и все следующие клики видят один текст.
func (s *Store) SaveTermExplanation(ctx context.Context, e *Explanation) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO term_explanations (term_id, context_hash, context, body, provider, model)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (term_id, context_hash) DO NOTHING
		 RETURNING created_at`,
		e.TermID, e.ContextHash, e.Context, e.Body, e.Provider, e.Model).Scan(&e.CreatedAt)
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	stored, err := s.TermExplanation(ctx, e.TermID, e.ContextHash)
	if err != nil {
		return err
	}
	*e = *stored
	return nil
}
