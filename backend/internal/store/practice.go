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
	// Lemmas — леммы всех слов текста (tatoeba.Lemmas): по ним ищет
	// тексто-практика (S5-7, ADR-035). Пишутся при сохранении, история
	// их не читает
	Lemmas       []string
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
		`INSERT INTO generated_texts (user_id, level, genre, max_words, target_lemmas, lemmas, body,
		     word_count, coverage, occurrences, unknown_share, attempts, provider, model)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13, $14)
		 RETURNING id, created_at`,
		t.UserID, t.Level, t.Genre, t.MaxWords, t.TargetLemmas, t.Lemmas, t.Body, t.WordCount, t.Coverage,
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

// Источники выдачи тексто-практики (S5-7, ADR-035).
const (
	PracticeSourceGenerated = "generated"
	PracticeSourceTatoeba   = "tatoeba"
)

// practiceCandidatesPerWord — сколько предложений Tatoeba с каждым словом
// поиска берётся в ранжирование. У частотных слов их десятки тысяч, и
// посчитать покрытие у всех — секунды; 50 на слово при 50 словах — до
// 2500 кандидатов, десятки миллисекунд на корпусе полного размера
// (ADR-035). Предложение со многими словами списка попадает в кандидаты
// по любому из них, поэтому редкие слова его не теряют.
const practiceCandidatesPerWord = 50

// PracticeTextCursor — позиция в выдаче поиска: ключ последней показанной
// строки в порядке выдачи (покрытие по убыванию, источник, порядок
// внутри источника, id).
type PracticeTextCursor struct {
	Covered int
	// Source — 0: свой сгенерированный текст, 1: Tatoeba
	Source int
	// Order — у сгенерированного −id (новые раньше), у предложения длина
	// в символах (короткие раньше)
	Order int64
	ID    int64
}

// PracticeText — текст тексто-практики: свой сгенерированный или
// предложение Tatoeba с русским переводом.
type PracticeText struct {
	Source string
	ID     int64
	Text   string
	// Matched — слова поиска, которые есть в тексте, в порядке слов поиска
	Matched []string
	Cursor  PracticeTextCursor
	// Только у сгенерированного
	Level     string
	Genre     string
	WordCount int
	CreatedAt *time.Time
	// Только у предложения Tatoeba: авторы для подписи CC BY 2.0 FR
	Author            string
	TranslationID     int64
	Translation       string
	TranslationAuthor string
}

// SearchPracticeTexts — тексто-практика по словам (S5-7, ADR-035): свои
// сгенерированные тексты пользователя и предложения Tatoeba с русским
// переводом, где есть хотя бы одно из lemmas (леммы, без повторов, по
// приоритету). Ранжирование — по числу слов списка в тексте, при равенстве
// свои тексты (новые первыми), затем предложения (короткие первыми).
// Предложения — из кандидатов practiceCandidatesPerWord на слово, без
// предложений короче exampleMinLemmas разных слов. after — курсор
// прошлой страницы; limit — строк страницы.
func (s *Store) SearchPracticeTexts(ctx context.Context, userID int64, lemmas []string, after *PracticeTextCursor, limit int) ([]PracticeText, error) {
	var covered *int
	var source int
	var order, afterID int64
	if after != nil {
		covered, source, order, afterID = &after.Covered, after.Source, after.Order, after.ID
	}
	rows, err := s.pool.Query(ctx, `
		WITH words AS MATERIALIZED (
		    SELECT w, n FROM unnest($2::text[]) WITH ORDINALITY AS u(w, n)
		), candidates AS MATERIALIZED (
		    SELECT DISTINCT c.id FROM words
		    CROSS JOIN LATERAL (
		        SELECT t.id FROM tatoeba_sentences t
		        WHERE t.lang = 'en' AND t.lemmas @> ARRAY[words.w] AND cardinality(t.lemmas) >= $3
		          AND EXISTS (SELECT 1 FROM tatoeba_links l WHERE l.en_id = t.id)
		        LIMIT $4
		    ) c
		), pool AS (
		    SELECT 0 AS src, g.id, -g.id AS ord, coalesce(g.lemmas, g.target_lemmas) AS lemmas
		    FROM generated_texts g WHERE g.user_id = $1
		    UNION ALL
		    SELECT 1, t.id, char_length(t.text)::bigint, t.lemmas
		    FROM candidates c JOIN tatoeba_sentences t ON t.id = c.id
		), ranked AS (
		    -- Леммы текста без повторов (tatoeba.Lemmas, Params.Lemmas):
		    -- число совпавших строк — число слов списка в тексте
		    SELECT p.src, p.id, p.ord, count(*)::int AS covered
		    FROM pool p
		    CROSS JOIN LATERAL unnest(p.lemmas) AS l(lemma)
		    JOIN words ON words.w = l.lemma
		    GROUP BY p.src, p.id, p.ord
		), page AS MATERIALIZED (
		    SELECT * FROM ranked
		    WHERE $5::int IS NULL OR (-covered, src, ord, id) > (-$5::int, $6::int, $7::bigint, $8::bigint)
		    ORDER BY covered DESC, src, ord, id
		    LIMIT $9
		)
		SELECT p.src, p.id, p.ord, p.covered, coalesce(g.body, t.text),
		       ARRAY(SELECT w FROM words WHERE w = ANY(coalesce(g.lemmas, g.target_lemmas, t.lemmas)) ORDER BY n),
		       coalesce(g.level, ''), coalesce(g.genre, ''), coalesce(g.word_count, 0), g.created_at,
		       coalesce(t.author, ''), coalesce(r.id, 0), coalesce(r.text, ''), coalesce(r.author, '')
		FROM page p
		LEFT JOIN generated_texts g ON p.src = 0 AND g.id = p.id
		LEFT JOIN tatoeba_sentences t ON p.src = 1 AND t.id = p.id
		LEFT JOIN LATERAL (
		    SELECT ru.id, ru.text, ru.author FROM tatoeba_links l
		    JOIN tatoeba_sentences ru ON ru.id = l.ru_id
		    WHERE p.src = 1 AND l.en_id = p.id ORDER BY ru.id LIMIT 1
		) r ON true
		ORDER BY p.covered DESC, p.src, p.ord, p.id`,
		userID, lemmas, exampleMinLemmas, practiceCandidatesPerWord, covered, source, order, afterID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PracticeText, error) {
		var t PracticeText
		c := &t.Cursor
		err := row.Scan(&c.Source, &t.ID, &c.Order, &c.Covered, &t.Text, &t.Matched,
			&t.Level, &t.Genre, &t.WordCount, &t.CreatedAt,
			&t.Author, &t.TranslationID, &t.Translation, &t.TranslationAuthor)
		c.ID = t.ID
		t.Source = PracticeSourceGenerated
		if c.Source == 1 {
			t.Source = PracticeSourceTatoeba
		}
		return t, err
	})
}
