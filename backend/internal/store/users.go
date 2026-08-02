package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Роли системы (ADR-007). admin заводит людей и тасует группы, teacher
// владеет уроками и своими студентами, student — участник.
const (
	RoleAdmin   = "admin"
	RoleTeacher = "teacher"
	RoleStudent = "student"
)

// UserWithGroups — строка списка людей в админ-кабинете: учётка плюс
// названия групп, в которых человек состоит.
type UserWithGroups struct {
	User
	Groups []string
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	u := &User{}
	var hash *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, role, name, password_hash FROM users WHERE id = $1`,
		id).Scan(&u.ID, &u.Email, &u.Role, &u.Name, &hash)
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

// CreateUser — заведение учётки со стартовым паролем (ADR-007).
// Дубль email → SQLSTATE 23505 (unique_violation) для маппинга в 409.
func (s *Store) CreateUser(ctx context.Context, email, role, name, passwordHash string) (*User, error) {
	u := &User{Email: email, Role: role, Name: name}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, role, name, password_hash)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		email, role, name, passwordHash).Scan(&u.ID)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ListUsers — все учётки с их группами (для admin).
func (s *Store) ListUsers(ctx context.Context) ([]UserWithGroups, error) {
	return s.listUsers(ctx, `SELECT u.id, u.email, u.role, u.name FROM users u ORDER BY u.id`)
}

// ListUsersOfTeacherGroups — учётки студентов из групп преподавателя плюс
// он сам: teacher видит и правит только своих (ADR-007).
func (s *Store) ListUsersOfTeacherGroups(ctx context.Context, teacherID int64) ([]UserWithGroups, error) {
	return s.listUsers(ctx,
		`SELECT u.id, u.email, u.role, u.name
		 FROM users u
		 WHERE u.id = $1
		    OR u.id IN (
		        SELECT gm.user_id FROM group_members gm
		        WHERE gm.group_id IN (
		            SELECT gm2.group_id FROM group_members gm2 WHERE gm2.user_id = $1
		            UNION
		            SELECT l.group_id FROM lessons l WHERE l.teacher_id = $1
		        )
		    )
		 ORDER BY u.id`, teacherID)
}

func (s *Store) listUsers(ctx context.Context, query string, args ...any) ([]UserWithGroups, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []UserWithGroups{}
	byID := map[int64]int{}
	for rows.Next() {
		u := UserWithGroups{Groups: []string{}}
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.Name); err != nil {
			return nil, err
		}
		byID[u.ID] = len(out)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// Группы отдельным запросом, а не JOIN'ом со строкой на пару
	// (пользователь, группа): список людей не должен размножаться.
	grows, err := s.pool.Query(ctx,
		`SELECT gm.user_id, g.name
		 FROM group_members gm
		 JOIN groups g ON g.id = gm.group_id
		 ORDER BY gm.user_id, g.name`)
	if err != nil {
		return nil, err
	}
	defer grows.Close()
	for grows.Next() {
		var userID int64
		var groupName string
		if err := grows.Scan(&userID, &groupName); err != nil {
			return nil, err
		}
		if i, ok := byID[userID]; ok {
			out[i].Groups = append(out[i].Groups, groupName)
		}
	}
	if err := grows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// TeacherManagesUser — состоит ли пользователь в группе преподавателя.
// Граница прав teacher'а из ADR-007: свои студенты, не все подряд.
func (s *Store) TeacherManagesUser(ctx context.Context, teacherID, userID int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM group_members gm
		     WHERE gm.user_id = $2
		       AND gm.group_id IN (
		           SELECT gm2.group_id FROM group_members gm2 WHERE gm2.user_id = $1
		           UNION
		           SELECT l.group_id FROM lessons l WHERE l.teacher_id = $1
		       )
		 )`, teacherID, userID).Scan(&exists)
	return exists, err
}

// UpdateUserProfile — имя и роль; пустая role оставляет текущую
// (смена роли доступна только админу, проверка — в хендлере).
func (s *Store) UpdateUserProfile(ctx context.Context, id int64, name, role string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`UPDATE users
		 SET name = COALESCE(NULLIF($2, ''), name),
		     role = COALESCE(NULLIF($3, ''), role)
		 WHERE id = $1
		 RETURNING id, email, role, name`,
		id, name, role).Scan(&u.ID, &u.Email, &u.Role, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// SetPassword — задать пароль и разлогинить владельца везде: и смена,
// и сброс инвалидируют все его сессии (ADR-007). Одной транзакцией,
// чтобы не осталось живой сессии со старым паролем.
func (s *Store) SetPassword(ctx context.Context, userID int64, passwordHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // после Commit — no-op

	tag, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveGroupMember — убрать человека из группы. Уже удалённый участник
// не ошибка: операция идемпотентна, как и добавление.
func (s *Store) RemoveGroupMember(ctx context.Context, groupID, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
	return err
}
