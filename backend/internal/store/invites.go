package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrInviteUnusable — токен есть, но воспользоваться им нельзя: просрочен
// или уже принят. Отделён от ErrNotFound, чтобы человек с честной, но
// протухшей ссылкой получил внятный ответ, а не «не найдено».
var ErrInviteUnusable = errors.New("invite expired or already accepted")

type Invite struct {
	ID          int64
	Email       string
	Name        string
	Role        string
	GroupID     *int64
	GroupName   string
	CreatedBy   int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	AcceptedAt  *time.Time
	InviterName string
}

func (s *Store) CreateInvite(ctx context.Context, tokenHash, email, name, role string, groupID *int64, createdBy int64, expiresAt time.Time) (*Invite, error) {
	inv := &Invite{Email: email, Name: name, Role: role, GroupID: groupID, CreatedBy: createdBy, ExpiresAt: expiresAt}
	// Имя группы достаём тем же запросом: иначе ответ на создание уезжает
	// с пустым group_name, хотя группа задана.
	err := s.pool.QueryRow(ctx,
		`WITH new_invite AS (
		     INSERT INTO invites (token_hash, email, name, role, group_id, created_by, expires_at)
		     VALUES ($1, $2, $3, $4, $5, $6, $7)
		     RETURNING id, created_at, group_id
		 )
		 SELECT i.id, i.created_at, COALESCE(g.name, '')
		 FROM new_invite i LEFT JOIN groups g ON g.id = i.group_id`,
		tokenHash, email, name, role, groupID, createdBy, expiresAt).
		Scan(&inv.ID, &inv.CreatedAt, &inv.GroupName)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// ListPendingInvites — непринятые и непросроченные. createdBy > 0 сужает
// выборку до приглашений одного автора (преподаватель видит свои).
func (s *Store) ListPendingInvites(ctx context.Context, createdBy int64) ([]Invite, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id, i.email, i.name, i.role, i.group_id, COALESCE(g.name, ''),
		        i.created_by, i.created_at, i.expires_at, u.name
		 FROM invites i
		 JOIN users u ON u.id = i.created_by
		 LEFT JOIN groups g ON g.id = i.group_id
		 WHERE i.accepted_at IS NULL AND i.expires_at > now()
		   AND ($1 = 0 OR i.created_by = $1)
		 ORDER BY i.created_at DESC`, createdBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Invite{}
	for rows.Next() {
		inv := Invite{}
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.Name, &inv.Role, &inv.GroupID, &inv.GroupName,
			&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.InviterName); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// InviteByTokenHash — предпросмотр для страницы приглашения.
func (s *Store) InviteByTokenHash(ctx context.Context, tokenHash string) (*Invite, error) {
	inv := &Invite{}
	var acceptedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT i.id, i.email, i.name, i.role, i.group_id, COALESCE(g.name, ''),
		        i.expires_at, i.accepted_at, u.name
		 FROM invites i
		 JOIN users u ON u.id = i.created_by
		 LEFT JOIN groups g ON g.id = i.group_id
		 WHERE i.token_hash = $1`, tokenHash).
		Scan(&inv.ID, &inv.Email, &inv.Name, &inv.Role, &inv.GroupID, &inv.GroupName,
			&inv.ExpiresAt, &acceptedAt, &inv.InviterName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Граница ровно как в SQL приёма (expires_at > now()): иначе
	// предпросмотр отдал бы 200 там, где приём уже вернёт 410.
	if acceptedAt != nil || !inv.ExpiresAt.After(time.Now()) {
		return inv, ErrInviteUnusable
	}
	return inv, nil
}

// DeleteInvite — отзыв. createdBy > 0 ограничивает отзыв своими.
func (s *Store) DeleteInvite(ctx context.Context, id, createdBy int64) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM invites WHERE id = $1 AND ($2 = 0 OR created_by = $2)`, id, createdBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AcceptInvite — одна транзакция: погасить приглашение, завести учётку,
// зачислить в группу, открыть сессию. Гашение первым и с условием
// accepted_at IS NULL — это и есть одноразовость: две параллельные
// попытки по одной ссылке не создадут двух пользователей.
func (s *Store) AcceptInvite(ctx context.Context, tokenHash, passwordHash, sessionTokenHash string, sessionExpiresAt time.Time) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // после Commit — no-op

	var (
		inviteID int64
		email    string
		name     string
		role     string
		groupID  *int64
	)
	err = tx.QueryRow(ctx,
		`UPDATE invites SET accepted_at = now()
		 WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now()
		 RETURNING id, email, name, role, group_id`, tokenHash).
		Scan(&inviteID, &email, &name, &role, &groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Ноль строк — либо приглашение негодное, либо его отозвали между
		// предпросмотром и приёмом. Разделяем: «отозвано» это ErrNotFound,
		// иначе человек получил бы «просрочено» на несуществующую ссылку.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM invites WHERE token_hash = $1)`, tokenHash).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
		return nil, ErrInviteUnusable
	}
	if err != nil {
		return nil, err
	}

	u := &User{Email: email, Role: role, Name: name}
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (email, role, name, password_hash)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		email, role, name, passwordHash).Scan(&u.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE invites SET accepted_user_id = $2 WHERE id = $1`, inviteID, u.ID); err != nil {
		return nil, err
	}
	if groupID != nil {
		if _, err := tx.Exec(ctx,
			`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, *groupID, u.ID); err != nil {
			return nil, err
		}
	}
	// Сессия — в этой же транзакции: иначе сбой её создания оставил бы
	// погашенное приглашение и учётку, в которую человек не вошёл.
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		u.ID, sessionTokenHash, sessionExpiresAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return u, nil
}
