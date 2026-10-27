package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// GeneratedText — строка generated_texts (S5-6, ADR-030).
type GeneratedText struct {
	ID           int64
	UserID       int64
	Level        string
	Genre        string
	MaxWords     int
	TargetLemmas []string
	Body         string
	WordCount    int
	Coverage     float64
	Occurrences  map[string]int
	UnknownShare float64
	Attempts     int
	Provider     string
	Model        string
	CreatedAt    time.Time
}

// Виды вызовов LLM — у каждого свой суточный счётчик llm_usage.kind
// (ADR-039): объяснение слова дешевле генерации на порядок и не должно
// съедать её лимит.
const (
	LLMKindGenerate = "generate"
	LLMKindExplain  = "explain"
)

// ReserveLLMRequest занимает место в суточном лимите вызовов вида kind
// у пользователя (ADR-029): false — лимит на сегодня (UTC) исчерпан.
// Проверка и инкремент — один оператор, поэтому параллельные запросы
// не проскочат мимо лимита.
func (s *Store) ReserveLLMRequest(ctx context.Context, userID int64, kind string, dailyLimit int) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO llm_usage (user_id, day, kind, requests)
		 VALUES ($1, (now() AT TIME ZONE 'UTC')::date, $2, 1)
		 ON CONFLICT (user_id, day, kind) DO UPDATE SET requests = llm_usage.requests + 1
		 WHERE llm_usage.requests < $3
		 RETURNING requests`, userID, kind, dailyLimit).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CreateGeneratedText сохраняет текст, прошедший валидатор; ID и
// CreatedAt заполняются из базы.
func (s *Store) CreateGeneratedText(ctx context.Context, t *GeneratedText) error {
	occ, err := json.Marshal(t.Occurrences)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO generated_texts (user_id, level, genre, max_words, target_lemmas, body,
		     word_count, coverage, occurrences, unknown_share, attempts, provider, model)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13)
		 RETURNING id, created_at`,
		t.UserID, t.Level, t.Genre, t.MaxWords, t.TargetLemmas, t.Body, t.WordCount, t.Coverage,
		occ, t.UnknownShare, t.Attempts, t.Provider, t.Model).Scan(&t.ID, &t.CreatedAt)
}

// GeneratedTextsByUser — свои тексты пользователя, новые первыми.
func (s *Store) GeneratedTextsByUser(ctx context.Context, userID int64, limit int) ([]GeneratedText, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, level, genre, max_words, target_lemmas, body, word_count, coverage,
		        occurrences, unknown_share, attempts, provider, model, created_at
		 FROM generated_texts WHERE user_id = $1
		 ORDER BY created_at DESC, id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	out := []GeneratedText{}
	for rows.Next() {
		var t GeneratedText
		if err := rows.Scan(&t.ID, &t.UserID, &t.Level, &t.Genre, &t.MaxWords, &t.TargetLemmas, &t.Body,
			&t.WordCount, &t.Coverage, &t.Occurrences, &t.UnknownShare, &t.Attempts, &t.Provider,
			&t.Model, &t.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
