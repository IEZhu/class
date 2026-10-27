package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
)

// Словарь урока (S3-7, S3-8): кандидаты из трёх источников (ADR-033),
// подтверждённые преподавателем lesson_terms и их назначение студентам
// (ADR-034).

// Источники кандидатов (lesson_term_candidates.source).
const (
	CandidateClick      = "click"
	CandidateWhiteboard = "whiteboard"
	CandidateManual     = "manual"
)

// JobWhiteboardTerms — разобрать доску кончившегося урока в кандидатов
// (S3-7). Ставится переходом урока из live (closeLessonIfOver), только
// если доску открывали.
const JobWhiteboardTerms = "whiteboard_terms"

// WhiteboardTermsJob — payload джобы JobWhiteboardTerms.
type WhiteboardTermsJob struct {
	LessonID int64 `json:"lesson_id"`
}

// CandidateTerm — термин, найденный в тексте урока: форма первой встречи
// и число вхождений.
type CandidateTerm struct {
	TermID  int64
	Surface string
	Count   int
}

// Candidate — кандидат словаря урока для чеклиста преподавателя.
type Candidate struct {
	TermID  int64
	Lemma   string
	Surface string
	// Translation — перевод из кэша термина; nil — слово не переводили
	Translation *string
	Source      string
	Count       int
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	// Confirmed — термин уже в словаре урока (lesson_terms)
	Confirmed bool
}

// LessonTerm — слово подтверждённого словаря урока.
type LessonTerm struct {
	TermID      int64
	Lemma       string
	Surface     string
	Translation *string
	ConfirmedAt time.Time
}

// AssignResult — итог назначения словаря урока: Added — строк user_terms
// заведено, Kept — слово у студента уже было, его статус не тронут.
type AssignResult struct {
	Students int
	Terms    int
	Added    int
	Kept     int
}

// RecordClickCandidate — клик по слову на уроке (S3-7): кандидат
// source='click' заводится с формой клика или его счётчик растёт на один.
// Пишется только клик участника из снапшота урока (lesson_participants),
// кроме самого преподавателя: его клики по чеклисту и транскрипту —
// не сигнал учеников. recorded — строка записана.
func (s *Store) RecordClickCandidate(ctx context.Context, lessonID, userID, termID int64, surface string) (recorded bool, err error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO lesson_term_candidates AS c (lesson_id, term_id, source, surface, added_by)
		 SELECT lp.lesson_id, $3, 'click', $4, lp.user_id
		 FROM lesson_participants lp JOIN lessons l ON l.id = lp.lesson_id
		 WHERE lp.lesson_id = $1 AND lp.user_id = $2 AND l.teacher_id <> lp.user_id
		 ON CONFLICT (lesson_id, term_id, source) DO UPDATE
		     SET count = c.count + 1, last_seen_at = now()`,
		lessonID, userID, termID, surface)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SaveWhiteboardCandidates — итог разбора доски: счётчик кандидата
// source='whiteboard' — число вхождений в сцене сейчас, а не сумма
// разборов, поэтому повторный разбор той же сцены ничего не меняет, кроме
// last_seen_at. Слово, стёртое с доски, остаётся кандидатом: на уроке
// оно было.
func (s *Store) SaveWhiteboardCandidates(ctx context.Context, lessonID int64, terms []CandidateTerm) error {
	if len(terms) == 0 {
		return nil
	}
	ids, surfaces, counts := candidateColumns(terms)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO lesson_term_candidates AS c (lesson_id, term_id, source, surface, count)
		 SELECT $1, t.term_id, 'whiteboard', t.surface, t.count
		 FROM unnest($2::bigint[], $3::text[], $4::int[]) AS t(term_id, surface, count)
		 ON CONFLICT (lesson_id, term_id, source) DO UPDATE
		     SET count = excluded.count, last_seen_at = now()`,
		lessonID, ids, surfaces, counts)
	return err
}

// AddManualCandidates — слова, которые преподаватель ввёл сам
// (source='manual'); уже введённое повтор не трогает.
func (s *Store) AddManualCandidates(ctx context.Context, lessonID, teacherID int64, terms []CandidateTerm) error {
	if len(terms) == 0 {
		return nil
	}
	ids, surfaces, _ := candidateColumns(terms)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO lesson_term_candidates (lesson_id, term_id, source, surface, added_by)
		 SELECT $1, t.term_id, 'manual', t.surface, $4
		 FROM unnest($2::bigint[], $3::text[]) AS t(term_id, surface)
		 ON CONFLICT (lesson_id, term_id, source) DO NOTHING`,
		lessonID, ids, surfaces, teacherID)
	return err
}

func candidateColumns(terms []CandidateTerm) (ids []int64, surfaces []string, counts []int32) {
	for _, t := range terms {
		ids = append(ids, t.TermID)
		surfaces = append(surfaces, t.Surface)
		counts = append(counts, int32(t.Count))
	}
	return ids, surfaces, counts
}

// LessonCandidates — кандидаты урока для чеклиста: по источникам, внутри
// источника — частые сверху; lang — язык перевода из кэша термина.
func (s *Store) LessonCandidates(ctx context.Context, lessonID int64, lang string) ([]Candidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.term_id, t.lemma, c.surface, t.translation_cache->$2::text->>'text', c.source,
		        c.count, c.first_seen_at, c.last_seen_at, lt.term_id IS NOT NULL
		 FROM lesson_term_candidates c
		 JOIN terms t ON t.id = c.term_id
		 LEFT JOIN lesson_terms lt ON lt.lesson_id = c.lesson_id AND lt.term_id = c.term_id
		 WHERE c.lesson_id = $1
		 ORDER BY c.source, c.count DESC, t.lemma`, lessonID, lang)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candidate, error) {
		var c Candidate
		err := row.Scan(&c.TermID, &c.Lemma, &c.Surface, &c.Translation, &c.Source,
			&c.Count, &c.FirstSeenAt, &c.LastSeenAt, &c.Confirmed)
		return c, err
	})
}

// SetLessonTerms — подтверждённый словарь урока целиком: отмеченные
// термины добавляются, снятые убираются (назначенные студентам слова
// остаются в их словарях). Термин не из кандидатов урока — ничего
// не меняется, такие id возвращаются в notCandidates.
func (s *Store) SetLessonTerms(ctx context.Context, lessonID, teacherID int64, termIDs []int64) (notCandidates []int64, err error) {
	if termIDs == nil {
		termIDs = []int64{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT DISTINCT id FROM unnest($2::bigint[]) AS id
		 WHERE NOT EXISTS (SELECT 1 FROM lesson_term_candidates c WHERE c.lesson_id = $1 AND c.term_id = id)
		 ORDER BY id`, lessonID, termIDs)
	if err != nil {
		return nil, err
	}
	if notCandidates, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil || len(notCandidates) > 0 {
		return notCandidates, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM lesson_terms WHERE lesson_id = $1 AND term_id <> ALL($2::bigint[])`,
		lessonID, termIDs); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO lesson_terms (lesson_id, term_id, confirmed_by)
		 SELECT $1, id, $3 FROM unnest($2::bigint[]) AS id
		 ON CONFLICT (lesson_id, term_id) DO NOTHING`,
		lessonID, termIDs, teacherID); err != nil {
		return nil, err
	}
	return nil, tx.Commit(ctx)
}

// LessonTerms — словарь урока по алфавиту лемм.
func (s *Store) LessonTerms(ctx context.Context, lessonID int64, lang string) ([]LessonTerm, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT lt.term_id, t.lemma, t.surface, t.translation_cache->$2::text->>'text', lt.confirmed_at
		 FROM lesson_terms lt JOIN terms t ON t.id = lt.term_id
		 WHERE lt.lesson_id = $1
		 ORDER BY t.lemma`, lessonID, lang)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (LessonTerm, error) {
		var t LessonTerm
		err := row.Scan(&t.TermID, &t.Lemma, &t.Surface, &t.Translation, &t.ConfirmedAt)
		return t, err
	})
}

// AssignLessonTerms кладёт словарь урока в личные словари студентов тем же
// upsert'ом, что «в мой словарь» (SaveUserTerm, ADR-027), — других путей
// записи в user_terms нет, и хук карточек FSRS (S4-1, долг D-3) срабатывает
// на назначении так же, как на попапе. Статус не передаётся: новое слово
// получает 1, а у слова, которое студент уже учит, статус не меняется —
// назначение не откатывает выученное. Урок-источник заполняется, только
// если его не было. Повтор ничего не меняет. Проверка, что studentIDs —
// студенты урока, — на вызывающем.
func (s *Store) AssignLessonTerms(ctx context.Context, lessonID int64, studentIDs []int64) (AssignResult, error) {
	res := AssignResult{Students: len(studentIDs)}
	rows, err := s.pool.Query(ctx,
		`SELECT term_id FROM lesson_terms WHERE lesson_id = $1 ORDER BY term_id`, lessonID)
	if err != nil {
		return res, err
	}
	termIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return res, err
	}
	res.Terms = len(termIDs)
	for _, studentID := range studentIDs {
		for _, termID := range termIDs {
			created, err := s.SaveUserTerm(ctx, studentID, termID, nil, &lessonID)
			if err != nil {
				return res, err
			}
			if created {
				res.Added++
			} else {
				res.Kept++
			}
		}
	}
	return res, nil
}

// DefinedLemmas — какие из лемм есть в словарном датасете (definitions):
// второй признак знакомого слова после OEWN (ADR-017).
func (s *Store) DefinedLemmas(ctx context.Context, lemmas []string) (map[string]bool, error) {
	out := make(map[string]bool)
	if len(lemmas) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT lemma FROM definitions WHERE lemma = ANY($1::text[])`, lemmas)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, l := range found {
		out[l] = true
	}
	return out, nil
}

// scheduleWhiteboardTerms — урок кончился: слова его доски — в кандидаты
// (S3-7). Зовётся в транзакции перехода урока; у урока без доски
// разбирать нечего, джоба не ставится.
func scheduleWhiteboardTerms(ctx context.Context, db jobs.DB, lessonID int64) error {
	var exists bool
	if err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM whiteboards WHERE lesson_id = $1)`, lessonID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return jobs.EnqueueOnce(ctx, db, JobWhiteboardTerms, WhiteboardTermsJob{LessonID: lessonID}, 0)
}
