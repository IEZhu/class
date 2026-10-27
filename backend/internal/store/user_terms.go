package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Статусы слова в личном словаре (S3-5): 1 — новое, 5 — знаю.
const (
	MinTermStatus = 1
	MaxTermStatus = 5
)

// UserTerm — слово личного словаря (user_terms, ADR-027) вместе с
// термином и уроком-источником.
type UserTerm struct {
	TermID  int64
	Lemma   string
	Surface string
	// Translation — перевод из кэша термина на запрошенный язык; nil —
	// слово ещё не переводили
	Translation *string
	Status      int
	// SourceLesson — урок, на котором слово добавили; nil — добавлено
	// вне урока или урок отменён
	SourceLesson *SourceLesson
	AddedAt      time.Time
	UpdatedAt    time.Time
}

// SourceLesson — подпись урока-источника для страницы словаря.
type SourceLesson struct {
	ID        int64
	Title     string
	GroupID   *int64
	GroupName string
	StartsAt  time.Time
}

// UserTermCursor — позиция страницы словаря: последняя показанная строка.
type UserTermCursor struct {
	AddedAt time.Time
	TermID  int64
}

// UserTermFilter — выборка словаря: статусы (пусто — все), курсор и потолок.
type UserTermFilter struct {
	Statuses []int
	After    *UserTermCursor
	Limit    int
	// Lang — язык перевода в ответе (terms.translation_cache)
	Lang string
}

// SaveUserTerm кладёт слово в словарь пользователя или меняет его статус
// одним upsert. status nil — у нового слова 1, у существующего прежний.
// Источник — первый урок, с которого слово пришло: у слова, добавленного
// вне урока, его заполнит следующий запрос с уроком, но не перепишет.
// created — строка вставлена, а не обновлена.
func (s *Store) SaveUserTerm(ctx context.Context, userID, termID int64, status *int, sourceLessonID *int64) (created bool, err error) {
	// xmax = 0 только у версии строки, которую создал этот же INSERT:
	// так один запрос отличает добавление от смены статуса
	err = s.pool.QueryRow(ctx,
		`INSERT INTO user_terms (user_id, term_id, status, source_lesson_id)
		 VALUES ($1, $2, coalesce($3::smallint, 1), $4)
		 ON CONFLICT (user_id, term_id) DO UPDATE SET
		     status = coalesce($3::smallint, user_terms.status),
		     source_lesson_id = coalesce(user_terms.source_lesson_id, EXCLUDED.source_lesson_id),
		     updated_at = CASE WHEN user_terms.status = coalesce($3::smallint, user_terms.status)
		                       THEN user_terms.updated_at ELSE now() END
		 RETURNING xmax = 0`, userID, termID, status, sourceLessonID).Scan(&created)
	return created, err
}

// DeleteUserTerm убирает слово из словаря пользователя; ErrNotFound —
// его там не было.
func (s *Store) DeleteUserTerm(ctx context.Context, userID, termID int64) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM user_terms WHERE user_id = $1 AND term_id = $2`, userID, termID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// userTermSelect — строка словаря с термином и уроком-источником; $1 —
// пользователь, $2 — язык перевода.
const userTermSelect = `SELECT ut.term_id, t.lemma, t.surface, t.translation_cache->$2::text->>'text',
	       ut.status, l.id, coalesce(l.title, ''), l.group_id, coalesce(g.name, ''), l.starts_at,
	       ut.added_at, ut.updated_at
	FROM user_terms ut
	JOIN terms t ON t.id = ut.term_id
	LEFT JOIN lessons l ON l.id = ut.source_lesson_id
	LEFT JOIN groups g ON g.id = l.group_id
	WHERE ut.user_id = $1`

func scanUserTerm(row pgx.CollectableRow) (UserTerm, error) {
	var (
		ut       UserTerm
		lessonID *int64
		lesson   SourceLesson
		startsAt *time.Time
	)
	err := row.Scan(&ut.TermID, &ut.Lemma, &ut.Surface, &ut.Translation, &ut.Status,
		&lessonID, &lesson.Title, &lesson.GroupID, &lesson.GroupName, &startsAt,
		&ut.AddedAt, &ut.UpdatedAt)
	if err != nil {
		return ut, err
	}
	if lessonID != nil && startsAt != nil {
		lesson.ID, lesson.StartsAt = *lessonID, *startsAt
		ut.SourceLesson = &lesson
	}
	return ut, nil
}

// GetUserTerm — одно слово словаря пользователя; ErrNotFound — его нет.
func (s *Store) GetUserTerm(ctx context.Context, userID, termID int64, lang string) (*UserTerm, error) {
	rows, err := s.pool.Query(ctx, userTermSelect+` AND ut.term_id = $3`, userID, lang, termID)
	if err != nil {
		return nil, err
	}
	ut, err := pgx.CollectExactlyOneRow(rows, scanUserTerm)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ut, nil
}

// ListUserTerms — словарь пользователя, новые сверху. Курсор — строка,
// на которой кончилась прошлая страница: добавленные тем временем слова
// не сдвигают выдачу.
func (s *Store) ListUserTerms(ctx context.Context, userID int64, f UserTermFilter) ([]UserTerm, error) {
	statuses := f.Statuses
	if statuses == nil {
		statuses = []int{}
	}
	var afterAt *time.Time
	var afterID int64
	if f.After != nil {
		afterAt, afterID = &f.After.AddedAt, f.After.TermID
	}
	rows, err := s.pool.Query(ctx, userTermSelect+`
		  AND (cardinality($3::smallint[]) = 0 OR ut.status = ANY($3::smallint[]))
		  AND ($4::timestamptz IS NULL OR (ut.added_at, ut.term_id) < ($4::timestamptz, $5::bigint))
		ORDER BY ut.added_at DESC, ut.term_id DESC
		LIMIT $6`, userID, f.Lang, statuses, afterAt, afterID, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUserTerm)
}

// UserTermStatus — статус термина в словаре пользователя; nil — слова
// в словаре нет.
func (s *Store) UserTermStatus(ctx context.Context, userID, termID int64) (*int, error) {
	var status int
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM user_terms WHERE user_id = $1 AND term_id = $2`, userID, termID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &status, nil
}

// UserTermStatusesByLemma — статусы слов словаря пользователя среди
// лемм видимого текста: лемма → статус, леммы вне словаря в ответ не
// попадают. Один запрос на пачку: ключ (user_id, term_id) и
// terms_lang_lemma_key держат его быстрым и на словаре двухчасового
// транскрипта.
func (s *Store) UserTermStatusesByLemma(ctx context.Context, userID int64, lang string, lemmas []string) (map[string]int, error) {
	out := make(map[string]int)
	if len(lemmas) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT t.lemma, ut.status FROM user_terms ut
		 JOIN terms t ON t.id = ut.term_id
		 WHERE ut.user_id = $1 AND t.lang = $2 AND t.lemma = ANY($3::text[])`, userID, lang, lemmas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var lemma string
		var status int
		if err := rows.Scan(&lemma, &status); err != nil {
			return nil, err
		}
		out[lemma] = status
	}
	return out, rows.Err()
}

// PracticeLemmas — целевые слова генерации текста из словаря пользователя
// (S5-6, ADR-030): ещё не выученные (статус ниже MaxTermStatus), сначала
// самые новые по статусу, среди равных — дольше всех не тронутые, то есть
// «забытые»; не больше limit. Пустой словарь — пустой список.
func (s *Store) PracticeLemmas(ctx context.Context, userID int64, lang string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.lemma FROM user_terms ut
		 JOIN terms t ON t.id = ut.term_id
		 WHERE ut.user_id = $1 AND t.lang = $2 AND ut.status < $3
		 ORDER BY ut.status, ut.updated_at, ut.term_id
		 LIMIT $4`, userID, lang, MaxTermStatus, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
