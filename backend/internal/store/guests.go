package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrEventOver — событие закончилось: гостевая ссылка больше не пускает.
var ErrEventOver = errors.New("event is over")

// ErrEventFull — на событии уже MaxGuestsPerEvent гостей.
var ErrEventFull = errors.New("event is full")

// GuestSessionGrace — сколько сессия гостя живёт после конца события:
// запас на переподключение и на затянувшийся урок (ADR-010). Срок
// считается от ends_at и при входе, и при переносе события.
const GuestSessionGrace = 2 * time.Hour

// MaxGuestsPerEvent — потолок гостевых учёток на одно событие. Ссылка
// многоразовая и расходится широко; без потолка один её держатель
// заводит гостей скриптом без счёта (ADR-032).
const MaxGuestsPerEvent = 100

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
// Сессия живёт до ends_at плюс GuestSessionGrace; ends_at берётся из той же
// заблокированной строки, поэтому перенос события вход не обгоняет.
func (s *Store) JoinAsGuest(ctx context.Context, tokenHash, name, syntheticEmail, sessionTokenHash string) (*User, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // после Commit — no-op

	// FOR SHARE держит строку урока до конца транзакции: перевыпуск или
	// отзыв (UPDATE guest_token_hash) ждёт, пока гость дозайдёт, а если
	// успел раньше — выборка перепроверяет хэш на новой версии строки
	// и старый токен не находит. Без блокировки гость мог пройти по уже
	// отозванной ссылке. Параллельные гости друг друга не ждут.
	var lessonID int64
	var endsAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT id, ends_at FROM lessons WHERE guest_token_hash = $1 FOR SHARE`, tokenHash).Scan(&lessonID, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	if endsAt.Before(time.Now()) {
		return nil, 0, ErrEventOver
	}
	// Считаем под той же блокировкой строки урока: отзыв ссылки и удаление
	// события ждут. Блокировка разделяемая, параллельные входы друг друга
	// не ждут, поэтому одновременные гости могут превысить потолок на число
	// таких входов — для защиты от заведения гостей без счёта этого хватает.
	var guests int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM lesson_participants lp JOIN users u ON u.id = lp.user_id
		 WHERE lp.lesson_id = $1 AND u.role = $2`, lessonID, RoleGuest).Scan(&guests); err != nil {
		return nil, 0, err
	}
	if guests >= MaxGuestsPerEvent {
		return nil, 0, ErrEventFull
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
		u.ID, sessionTokenHash, endsAt.Add(GuestSessionGrace)); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return u, lessonID, nil
}
