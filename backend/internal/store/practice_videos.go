package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Откуда веса целевых слов видео-практики (S5-5, ADR-038).
const (
	// WeightsSRS — по карточкам FSRS (srs_cards, S4-1): к повторению
	// сейчас — тяжелее, и тем тяжелее, чем чаще слово забывали
	WeightsSRS = "srs"
	// WeightsStatus — по статусу в словаре: чем новее слово, тем тяжелее
	WeightsStatus = "status"
)

// LearningWord — невыученное слово словаря и его вес в ранжировании видео.
type LearningWord struct {
	Lemma  string
	Weight float64
}

// srsCardsReady — в базе есть srs_cards с колонками, по которым считаются
// веса (03-data-model.md). Таблицу заводит S4-1; до неё и при другой
// схеме веса — по статусу. Проверка — на каждый запрос: миграция S4-1
// не требует перезапуска api.
const srsCardsReady = `SELECT count(*) = 4 FROM pg_attribute
	WHERE attrelid = to_regclass('srs_cards') AND NOT attisdropped
	  AND attname IN ('user_id', 'term_id', 'due_at', 'lapses')`

// learningWordsBySRS: карточка к повторению (или ещё не заведена) —
// 2 + забывания (до 3), не к повторению — 1; сначала тяжёлые, среди
// равных — дольше всех просроченные. $3 — MaxTermStatus.
const learningWordsBySRS = `SELECT t.lemma,
	       CASE WHEN c.due_at > now() THEN 1 ELSE 2 + least(coalesce(c.lapses, 0), 3) END::float8 AS weight
	FROM user_terms ut
	JOIN terms t ON t.id = ut.term_id
	LEFT JOIN srs_cards c ON c.user_id = ut.user_id AND c.term_id = ut.term_id
	WHERE ut.user_id = $1 AND t.lang = $2 AND ut.status < $3
	ORDER BY weight DESC, c.due_at NULLS FIRST, ut.updated_at, ut.term_id
	LIMIT $4`

// learningWordsByStatus: вес 5 − статус (новое — 4, почти выученное — 1),
// порядок — как у PracticeLemmas: новые, затем дольше всех не тронутые.
const learningWordsByStatus = `SELECT t.lemma, ($3 - ut.status)::float8
	FROM user_terms ut
	JOIN terms t ON t.id = ut.term_id
	WHERE ut.user_id = $1 AND t.lang = $2 AND ut.status < $3
	ORDER BY ut.status, ut.updated_at, ut.term_id
	LIMIT $4`

// LearningWords — до limit невыученных слов словаря пользователя с весами
// и способ, которым веса посчитаны (WeightsSRS или WeightsStatus).
func (s *Store) LearningWords(ctx context.Context, userID int64, lang string, limit int) ([]LearningWord, string, error) {
	var srs bool
	if err := s.pool.QueryRow(ctx, srsCardsReady).Scan(&srs); err != nil {
		return nil, "", err
	}
	query, weighting := learningWordsByStatus, WeightsStatus
	if srs {
		query, weighting = learningWordsBySRS, WeightsSRS
	}
	rows, err := s.pool.Query(ctx, query, userID, lang, MaxTermStatus, limit)
	if err != nil {
		return nil, "", err
	}
	words, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LearningWord, error) {
		var w LearningWord
		err := row.Scan(&w.Lemma, &w.Weight)
		return w, err
	})
	return words, weighting, err
}

// VideoTarget — целевое слово поиска по субтитрам: лемма, её формы
// (сама лемма и lemma.Forms) и вес.
type VideoTarget struct {
	Lemma  string
	Forms  []string
	Weight float64
}

// PracticeVideo — видео, подобранное под целевые слова. Words и
// Segments[].Words — номера целей, найденных в видео и в сегменте.
type PracticeVideo struct {
	ID         int64
	ExternalID string
	Title      string
	Channel    *string
	// DurationMS — длительность из videos.duration_s, без неё — конец
	// последнего сегмента
	DurationMS int
	Subtitles  string
	// Coverage — доля веса целей, найденных в видео (0..1)
	Coverage float64
	// Occurrences — пар «сегмент, цель»; PerMinute — их же на минуту
	// видео, короче минуты — как минута
	Occurrences int
	PerMinute   float64
	Words       []int
	Segments    []PracticeSegment
}

// PracticeSegment — сегмент субтитров с найденными в нём целями.
type PracticeSegment struct {
	Idx     int
	StartMS int
	EndMS   int
	Text    string
	Words   []int
}

// targetsCTE — цели как строки (i — номер в запросе с нуля, вес, tsquery):
// формы приводятся словарём english, как tsv сегментов, и сливаются через
// OR; цель только из стоп-слов (be, have) остаётся без запроса и ничего
// не находит. $1 — формы целей через пробел, $2 — веса.
const targetsCTE = `t AS MATERIALIZED (
	    SELECT w.i::int - 1 AS i, w.weight,
	           (SELECT string_agg('(' || q::text || ')', ' | ')::tsquery
	            FROM unnest(string_to_array(w.forms, ' ')) AS f, plainto_tsquery('english', f) AS q
	            WHERE numnode(q) > 0) AS query
	    FROM unnest($1::text[], $2::float8[]) WITH ORDINALITY AS w(forms, weight, i)
	)`

// playableVideo — видео, которое плеер практики покажет: YouTube
// (встраивание, ADR-038) и сохранённые английские субтитры.
const playableVideo = `v.source = 'youtube' AND v.subtitles IN ('manual', 'auto')`

// videoDurationMS — длительность видео v в мс: RSS её не даёт, сборщик
// пишет из info.json; без неё — конец последнего сегмента (по PK).
const videoDurationMS = `coalesce(v.duration_s * 1000,
	    (SELECT e.end_ms FROM subtitle_segments e WHERE e.video_id = v.id ORDER BY e.idx DESC LIMIT 1), 0)`

// practiceVideosSQL — ранжирование видео под цели (ADR-038). Каждая цель —
// отдельный проход по GIN-индексу tsv; по видео копятся найденные цели и
// вхождения. Порядок: доля веса найденных целей, затем вхождения на
// минуту, затем id — страницы детерминированы. $3 — сумма весов,
// $4/$5 — LIMIT/OFFSET.
const practiceVideosSQL = `WITH ` + targetsCTE + `,
	hits AS (
	    SELECT s.video_id, t.i, t.weight, count(*) AS n
	    FROM t JOIN subtitle_segments s ON s.tsv @@ t.query
	    GROUP BY s.video_id, t.i, t.weight
	), ranked AS (
	    SELECT video_id, sum(weight) AS covered, sum(n)::int AS occurrences, array_agg(i ORDER BY i) AS words
	    FROM hits GROUP BY video_id
	)
	SELECT v.id, v.external_id, v.title, v.channel, v.subtitles, d.ms,
	       r.covered / $3, r.occurrences, (r.occurrences * 60000.0 / greatest(d.ms, 60000))::float8 AS per_minute, r.words
	FROM ranked r
	JOIN videos v ON v.id = r.video_id
	CROSS JOIN LATERAL (SELECT ` + videoDurationMS + ` AS ms) d
	WHERE ` + playableVideo + `
	ORDER BY r.covered DESC, per_minute DESC, v.id
	LIMIT $4 OFFSET $5`

// bestSegmentsSQL — лучшие места видео страницы: сегменты с целями, по
// весу найденных целей, затем по времени; не больше $4 на видео.
// $3 — id видео.
const bestSegmentsSQL = `WITH ` + targetsCTE + `,
	m AS (
	    SELECT s.video_id, s.idx, s.start_ms, s.end_ms, s.text,
	           array_agg(t.i ORDER BY t.i) AS words, sum(t.weight) AS covered
	    FROM t JOIN subtitle_segments s ON s.tsv @@ t.query
	    WHERE s.video_id = ANY($3::bigint[])
	    GROUP BY s.video_id, s.idx
	)
	SELECT video_id, idx, start_ms, end_ms, text, words FROM (
	    SELECT m.*, row_number() OVER (PARTITION BY video_id ORDER BY covered DESC, idx) AS rn FROM m
	) best
	WHERE rn <= $4
	ORDER BY video_id, rn`

// PracticeVideosArgs — аргументы practiceVideosSQL; тест плана запроса
// (EXPLAIN) берёт их отсюда же, чтобы проверять ровно тот запрос.
func PracticeVideosArgs(targets []VideoTarget, limit, offset int) (string, []any) {
	forms, weights := targetArrays(targets)
	return practiceVideosSQL, []any{forms, weights, totalWeight(targets), limit, offset}
}

func targetArrays(targets []VideoTarget) ([]string, []float64) {
	forms := make([]string, len(targets))
	weights := make([]float64, len(targets))
	for i, t := range targets {
		forms[i], weights[i] = strings.Join(t.Forms, " "), t.Weight
	}
	return forms, weights
}

func totalWeight(targets []VideoTarget) float64 {
	var sum float64
	for _, t := range targets {
		sum += t.Weight
	}
	return sum
}

// PracticeVideos — страница видео под цели (offset, не больше limit) и
// у каждого до segments лучших сегментов. Видео без YouTube-плеера или
// без субтитров не попадают; нет целей — пустой список.
func (s *Store) PracticeVideos(ctx context.Context, targets []VideoTarget, limit, offset, segments int) ([]PracticeVideo, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	query, args := PracticeVideosArgs(targets, limit, offset)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	videos, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PracticeVideo, error) {
		var v PracticeVideo
		err := row.Scan(&v.ID, &v.ExternalID, &v.Title, &v.Channel, &v.Subtitles, &v.DurationMS,
			&v.Coverage, &v.Occurrences, &v.PerMinute, &v.Words)
		return v, err
	})
	if err != nil || len(videos) == 0 {
		return videos, err
	}
	ids := make([]int64, len(videos))
	byID := make(map[int64]*PracticeVideo, len(videos))
	for i := range videos {
		ids[i] = videos[i].ID
		byID[videos[i].ID] = &videos[i]
	}
	forms, weights := targetArrays(targets)
	rows, err = s.pool.Query(ctx, bestSegmentsSQL, forms, weights, ids, segments)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var videoID int64
		var seg PracticeSegment
		if err := rows.Scan(&videoID, &seg.Idx, &seg.StartMS, &seg.EndMS, &seg.Text, &seg.Words); err != nil {
			return nil, err
		}
		byID[videoID].Segments = append(byID[videoID].Segments, seg)
	}
	return videos, rows.Err()
}

// PracticeVideoByID — видео для плеера практики со всеми сегментами
// субтитров по порядку и найденными в них целями; счётчики — как
// в PracticeVideos. ErrNotFound — нет видео, оно не с YouTube или без
// субтитров.
func (s *Store) PracticeVideoByID(ctx context.Context, id int64, targets []VideoTarget) (*PracticeVideo, error) {
	v := &PracticeVideo{ID: id}
	err := s.pool.QueryRow(ctx,
		`SELECT v.external_id, v.title, v.channel, v.subtitles, `+videoDurationMS+`
		 FROM videos v WHERE v.id = $1 AND `+playableVideo, id).
		Scan(&v.ExternalID, &v.Title, &v.Channel, &v.Subtitles, &v.DurationMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	forms, weights := targetArrays(targets)
	rows, err := s.pool.Query(ctx, `WITH `+targetsCTE+`
		SELECT s.idx, s.start_ms, s.end_ms, s.text,
		       ARRAY(SELECT t.i FROM t WHERE s.tsv @@ t.query ORDER BY t.i)
		FROM subtitle_segments s WHERE s.video_id = $3 ORDER BY s.idx`, forms, weights, id)
	if err != nil {
		return nil, err
	}
	v.Segments, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PracticeSegment, error) {
		var seg PracticeSegment
		err := row.Scan(&seg.Idx, &seg.StartMS, &seg.EndMS, &seg.Text, &seg.Words)
		return seg, err
	})
	if err != nil {
		return nil, err
	}
	found := map[int]bool{}
	for _, seg := range v.Segments {
		v.Occurrences += len(seg.Words)
		for _, i := range seg.Words {
			found[i] = true
		}
	}
	var covered float64
	for i := range targets {
		if found[i] {
			v.Words = append(v.Words, i)
			covered += targets[i].Weight
		}
	}
	if total := totalWeight(targets); total > 0 {
		v.Coverage = covered / total
	}
	v.PerMinute = float64(v.Occurrences) * 60000 / float64(max(v.DurationMS, 60000))
	return v, nil
}
