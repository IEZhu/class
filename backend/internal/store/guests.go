package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrEventOver — событие закончилось: гостевая ссылка больше не пускает.
var ErrEventOver = errors.New("event is over")

// GuestEvent — что видит человек на странице входа по гостевой ссылке.
type GuestEvent struct {
	LessonID    int64
	Title       string
	TeacherName string
	StartsAt    time.Time
	EndsAt      time.Time
}

// SetGuestToken — выпуск или перевыпуск гостевой ссылки. Пустой хэш
// отзывает её: старая ссылка перестаёт работать сразу.
func (s *Store) SetGuestToken(ctx context.Context, lessonID int64, tokenHash string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE lessons SET guest_token_hash = NULLIF($2, '') WHERE id = $1`,
		lessonID, tokenHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GuestEventByToken — событие по гостевой ссылке. ErrEventOver, если оно
// уже закончилось: заходить некуда, а комната всё равно закрыта (S1-4).
func (s *Store) GuestEventByToken(ctx context.Context, tokenHash string) (*GuestEvent, error) {
	e := &GuestEvent{}
	err := s.pool.QueryRow(ctx,
		`SELECT l.id, COALESCE(NULLIF(l.title, ''), g.name, ''), t.name, l.starts_at, l.ends_at
		 FROM lessons l
		 LEFT JOIN groups g ON g.id = l.group_id
		 JOIN users t ON t.id = l.teacher_id
		 WHERE l.guest_token_hash = $1`, tokenHash).
		Scan(&e.LessonID, &e.Title, &e.TeacherName, &e.StartsAt, &e.EndsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.EndsAt.Before(time.Now()) {
		return e, ErrEventOver
	}
	return e, nil
}

// JoinAsGuest заводит гостя и сажает его в участники события — одной
// транзакцией, чтобы не осталось учётки без участия. Гость без пароля:
// войти второй раз он не сможет, сессию получает сразу (ADR-010).
//
// Email синтетический и уникальный: колонка UNIQUE NOT NULL, а настоящего
// адреса у гостя мы не спрашиваем — он пришёл по ссылке представиться именем.
func (s *Store) JoinAsGuest(ctx context.Context, tokenHash, name, syntheticEmail, sessionTokenHash string, sessionExpiresAt time.Time) (*User, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // после Commit — no-op

	var lessonID int64
	var endsAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT id, ends_at FROM lessons WHERE guest_token_hash = $1`, tokenHash).Scan(&lessonID, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	if endsAt.Before(time.Now()) {
		return nil, 0, ErrEventOver
	}

	u := &User{Email: syntheticEmail, Role: RoleGuest, Name: name}
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, role, name) VALUES ($1, $2, $3) RETURNING id`,
		syntheticEmail, RoleGuest, name).Scan(&u.ID); err != nil {
		return nil, 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO lesson_participants (lesson_id, user_id) VALUES ($1, $2)`,
		lessonID, u.ID); err != nil {
		return nil, 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		u.ID, sessionTokenHash, sessionExpiresAt); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return u, lessonID, nil
}
