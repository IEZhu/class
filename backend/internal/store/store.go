// Package store — доступ к Postgres (pgx). Единственная точка SQL-запросов api.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

type User struct {
	ID           int64
	Email        string
	Role         string
	Name         string
	PasswordHash string
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	var hash *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, role, name, password_hash FROM users WHERE email = $1`,
		email).Scan(&u.ID, &u.Email, &u.Role, &u.Name, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if hash != nil {
		u.PasswordHash = *hash
	}
	return u, nil
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt)
	return err
}

func (s *Store) UserBySessionTokenHash(ctx context.Context, tokenHash string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT u.id, u.email, u.role, u.name
		 FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1 AND s.expires_at > now()`,
		tokenHash).Scan(&u.ID, &u.Email, &u.Role, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteExpiredSessions — ленивая уборка протухших сессий при логине владельца (ADR-006).
func (s *Store) DeleteExpiredSessions(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND expires_at <= now()`, userID)
	return err
}
