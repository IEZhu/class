package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Term — строка общего словаря terms (S3-2): лемма и кэш переводов.
type Term struct {
	ID      int64
	Lang    string
	Lemma   string
	Surface string
	// Translations — terms.translation_cache по целевому языку
	Translations map[string]Translation
}

// Translation — перевод в кэше термина.
type Translation struct {
	Text   string    `json:"text"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

// EnsureTerm возвращает термин леммы, заводя его при первом клике;
// surface запоминается от первого клика. Попадание в кэш — один SELECT
// без записи.
func (s *Store) EnsureTerm(ctx context.Context, lang, lemma, surface string) (*Term, error) {
	t, err := s.termByLemma(ctx, lang, lemma)
	if !errors.Is(err, ErrNotFound) {
		return t, err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO terms (lang, lemma, surface) VALUES ($1, $2, $3)
		 ON CONFLICT (lang, lemma) DO NOTHING`, lang, lemma, surface); err != nil {
		return nil, err
	}
	// Отдельный запрос, а не RETURNING: при параллельном первом клике
	// строку вставил другой запрос, и увидит её только новый снимок
	return s.termByLemma(ctx, lang, lemma)
}

func (s *Store) termByLemma(ctx context.Context, lang, lemma string) (*Term, error) {
	return s.scanTerm(s.pool.QueryRow(ctx,
		`SELECT id, lang, lemma, surface, translation_cache FROM terms
		 WHERE lang = $1 AND lemma = $2`, lang, lemma))
}

// TermByID — термин по id из ответа клика («объясни», S5-8);
// ErrNotFound — такого нет.
func (s *Store) TermByID(ctx context.Context, id int64) (*Term, error) {
	return s.scanTerm(s.pool.QueryRow(ctx,
		`SELECT id, lang, lemma, surface, translation_cache FROM terms WHERE id = $1`, id))
}

func (s *Store) scanTerm(row pgx.Row) (*Term, error) {
	t := &Term{}
	err := row.Scan(&t.ID, &t.Lang, &t.Lemma, &t.Surface, &t.Translations)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// CacheTranslation кладёт перевод в кэш термина по целевому языку,
// не трогая переводы на другие языки.
func (s *Store) CacheTranslation(ctx context.Context, termID int64, lang string, tr Translation) error {
	body, err := json.Marshal(tr)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE terms SET translation_cache = translation_cache || jsonb_build_object($2::text, $3::jsonb)
		 WHERE id = $1`, termID, lang, body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Definition — статья словарного датасета (S3-4).
type Definition struct {
	Word           string
	Pos            string
	Glosses        []string
	TranslationsRU []string
}

// Definitions — статьи по ключам поиска в порядке ключей (сначала лемма
// термина, затем сама словоформа: у building — и build, и building),
// внутри ключа статья с точным регистром кликнутого слова идёт первой
// (US и us), дальше — порядок страницы Wiktionary.
func (s *Store) Definitions(ctx context.Context, keys []string, surface string, limit int) ([]Definition, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT word, pos, glosses, translations_ru FROM definitions
		 WHERE lemma = ANY($1)
		 ORDER BY array_position($1, lemma), word = $2 DESC, source_line
		 LIMIT $3`, keys, surface, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Definition, error) {
		var d Definition
		err := row.Scan(&d.Word, &d.Pos, &d.Glosses, &d.TranslationsRU)
		return d, err
	})
}
