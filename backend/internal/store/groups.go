package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Group struct {
	ID    int64
	Name  string
	Level string
}

type GroupMember struct {
	UserID int64
	Email  string
	Name   string
	Role   string
}

type GroupWithMembers struct {
	Group
	Members []GroupMember
}

// PgErrorCode возвращает SQLSTATE-код ошибки Postgres ("" — не pg-ошибка).
// Используется хендлерами для маппинга в честные HTTP-коды (04-api.md).
func PgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func (s *Store) CreateGroup(ctx context.Context, name, level string) (*Group, error) {
	g := &Group{Name: name, Level: level}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO groups (name, level) VALUES ($1, $2) RETURNING id`,
		name, level).Scan(&g.ID); err != nil {
		return nil, err
	}
	return g, nil
}

func (s *Store) ListGroupsWithMembers(ctx context.Context) ([]GroupWithMembers, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, level FROM groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := map[int64]*GroupWithMembers{}
	order := []int64{}
	for rows.Next() {
		g := GroupWithMembers{Members: []GroupMember{}}
		if err := rows.Scan(&g.ID, &g.Name, &g.Level); err != nil {
			return nil, err
		}
		byID[g.ID] = &g
		order = append(order, g.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	mrows, err := s.pool.Query(ctx,
		`SELECT gm.group_id, u.id, u.email, u.name, u.role
		 FROM group_members gm
		 JOIN users u ON u.id = gm.user_id
		 ORDER BY gm.group_id, u.id`)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var groupID int64
		m := GroupMember{}
		if err := mrows.Scan(&groupID, &m.UserID, &m.Email, &m.Name, &m.Role); err != nil {
			return nil, err
		}
		if g, ok := byID[groupID]; ok {
			g.Members = append(g.Members, m)
		}
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	out := make([]GroupWithMembers, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// AddGroupMemberByEmail — идемпотентно (повторное добавление не ошибка).
// Пользователь не найден → ErrNotFound; группа не найдена → FK-ошибка 23503.
func (s *Store) AddGroupMemberByEmail(ctx context.Context, groupID int64, email string) (*GroupMember, error) {
	m := &GroupMember{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, role FROM users WHERE email = $1`,
		email).Scan(&m.UserID, &m.Email, &m.Name, &m.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, groupID, m.UserID); err != nil {
		return nil, err
	}
	return m, nil
}
