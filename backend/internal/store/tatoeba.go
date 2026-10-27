package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const (
	// exampleCandidates — сколько предложений с леммой просматривает поиск
	// примеров. У частотных слов (be, have) их сотни тысяч, и сортировка
	// всех по длине — почти полный проход таблицы. Порядок строк в таблице
	// с длиной не связан, поэтому кратчайшие из первых 500 — уже из самых
	// коротких предложений корпуса (ADR-028).
	exampleCandidates = 500
	// exampleMinLemmas — предложение из одного-двух слов («Run!») — само
	// слово, а не пример употребления: такие идут последними
	exampleMinLemmas = 3
)

// Example — пример употребления из корпуса Tatoeba (S5-4, ADR-028):
// английское предложение и его русский перевод с авторами для подписи.
type Example struct {
	ID                int64
	Text              string
	Author            string
	TranslationID     int64
	Translation       string
	TranslationAuthor string
}

// Examples — до limit коротких английских предложений с леммой и
// русским переводом (младший id из пар): предложения хотя бы из трёх
// разных слов первыми, внутри — кратчайшие. Пустой корпус — пустой
// список.
func (s *Store) Examples(ctx context.Context, lemma string, limit int) ([]Example, error) {
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS MATERIALIZED (
		    SELECT id, text, author, lemmas FROM tatoeba_sentences
		    WHERE lang = 'en' AND lemmas @> ARRAY[$1::text]
		    LIMIT $3
		), best AS (
		    SELECT * FROM candidates
		    ORDER BY cardinality(lemmas) < $4, char_length(text), id
		    LIMIT $2
		)
		SELECT b.id, b.text, b.author, r.id, r.text, r.author
		FROM best b
		CROSS JOIN LATERAL (
		    SELECT ru.id, ru.text, ru.author FROM tatoeba_links l
		    JOIN tatoeba_sentences ru ON ru.id = l.ru_id
		    WHERE l.en_id = b.id ORDER BY ru.id LIMIT 1
		) r
		ORDER BY cardinality(b.lemmas) < $4, char_length(b.text), b.id`,
		lemma, limit, exampleCandidates, exampleMinLemmas)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Example, error) {
		var e Example
		err := row.Scan(&e.ID, &e.Text, &e.Author, &e.TranslationID, &e.Translation, &e.TranslationAuthor)
		return e, err
	})
}
