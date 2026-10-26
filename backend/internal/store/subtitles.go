package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Какие субтитры сохранены у видео (videos.subtitles, ADR-022).
const (
	SubtitlesManual = "manual"
	SubtitlesAuto   = "auto"
	// SubtitlesNone — английских субтитров у видео нет, повторять незачем
	SubtitlesNone = "none"
)

// MaxHarvestAttempts — после стольких неудачных попыток видео выпадает из
// очереди сборщика; вернуть — обнулить videos.harvest_attempts.
const MaxHarvestAttempts = 3

// SubtitleSource — канал или плейлист YouTube из subtitle_sources.
type SubtitleSource struct {
	ID         int64
	Kind       string // channel | playlist
	ExternalID string
	Title      string
}

// FeedVideo — запись RSS-фида источника.
type FeedVideo struct {
	ExternalID  string
	Title       string
	Channel     string
	PublishedAt *time.Time
}

// HarvestVideo — видео в очереди сборщика субтитров.
type HarvestVideo struct {
	ID         int64
	ExternalID string
}

// SubtitleSegment — реплика субтитров; tsvector считает сам Postgres.
type SubtitleSegment struct {
	StartMS int
	EndMS   int
	Text    string
}

// SubtitleMatch — сегмент, найденный полнотекстовым поиском.
type SubtitleMatch struct {
	VideoID    int64
	Source     string
	ExternalID string
	Title      string
	Idx        int
	StartMS    int
	EndMS      int
	Text       string
}

// EnabledSubtitleSources — включённые источники сборщика.
func (s *Store) EnabledSubtitleSources(ctx context.Context) ([]SubtitleSource, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, external_id, title FROM subtitle_sources WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SubtitleSource, error) {
		var src SubtitleSource
		err := row.Scan(&src.ID, &src.Kind, &src.ExternalID, &src.Title)
		return src, err
	})
}

// SetSourcePolled записывает итог опроса фида; пустой pollError — успех.
func (s *Store) SetSourcePolled(ctx context.Context, sourceID int64, pollError string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE subtitle_sources SET polled_at = now(), last_error = NULLIF(left($2, 1000), '')
		 WHERE id = $1`, sourceID, pollError)
	return err
}

// AddFeedVideos добавляет новые видео источника одной транзакцией и
// возвращает, сколько из них новых. Уже известные пропускаются, поэтому
// повторный опрос того же фида ничего не меняет (джоба at-least-once).
func (s *Store) AddFeedVideos(ctx context.Context, sourceID int64, videos []FeedVideo) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	added := 0
	for _, v := range videos {
		tag, err := tx.Exec(ctx,
			`INSERT INTO videos (source, external_id, title, channel, source_id, published_at)
			 VALUES ('youtube', $1, $2, NULLIF($3, ''), $4, $5)
			 ON CONFLICT (source, external_id) DO NOTHING`,
			v.ExternalID, v.Title, v.Channel, sourceID, v.PublishedAt)
		if err != nil {
			return 0, err
		}
		added += int(tag.RowsAffected())
	}
	return added, tx.Commit(ctx)
}

// NextVideoToHarvest — следующее видео YouTube без субтитров: сначала без
// неудачных попыток и свежие. Неудачная попытка откладывает видео на
// 6 ч × число попыток. ErrNotFound — собирать нечего.
func (s *Store) NextVideoToHarvest(ctx context.Context) (*HarvestVideo, error) {
	v := &HarvestVideo{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, external_id FROM videos
		 WHERE source = 'youtube' AND subtitles IS NULL AND harvest_attempts < $1
		   AND (harvested_at IS NULL
		        OR harvested_at < now() - make_interval(hours => 6 * harvest_attempts))
		 ORDER BY harvest_attempts, published_at DESC NULLS LAST, id
		 LIMIT 1`, MaxHarvestAttempts).Scan(&v.ID, &v.ExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// SaveSubtitles заменяет сегменты видео и отмечает, какие субтитры
// сохранены. Замена целиком делает повтор идемпотентным. durationS 0 —
// длительность неизвестна и не трогается.
func (s *Store) SaveSubtitles(ctx context.Context, videoID int64, kind string, durationS int, segments []SubtitleSegment) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE videos SET subtitles = $2, duration_s = COALESCE(NULLIF($3, 0), duration_s),
		        harvested_at = now(), harvest_error = NULL
		 WHERE id = $1`, videoID, kind, durationS)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_segments WHERE video_id = $1`, videoID); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"subtitle_segments"},
		[]string{"video_id", "idx", "start_ms", "end_ms", "text"},
		pgx.CopyFromSlice(len(segments), func(i int) ([]any, error) {
			seg := segments[i]
			return []any{videoID, i, seg.StartMS, seg.EndMS, seg.Text}, nil
		})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FailHarvest — неудачная попытка собрать субтитры видео.
func (s *Store) FailHarvest(ctx context.Context, videoID int64, reason string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE videos SET harvest_attempts = harvest_attempts + 1,
		        harvest_error = left($2, 1000), harvested_at = now()
		 WHERE id = $1`, videoID, reason)
	return err
}

// SearchSubtitles — полнотекстовый поиск по корпусу субтитров для
// видео-практики (S5-5): сегменты, где есть хотя бы одно из слов, по видео
// и порядку. Слова нормализуются тем же словарём english, что и tsv,
// поэтому «running» находит «runs». Стоп-слова и пустые строки
// отбрасываются; если не осталось ни одного слова, результат пуст.
func (s *Store) SearchSubtitles(ctx context.Context, words []string, limit int) ([]SubtitleMatch, error) {
	rows, err := s.pool.Query(ctx,
		`WITH q AS (
		     SELECT string_agg('(' || t::text || ')', ' | ')::tsquery AS query
		     FROM unnest($1::text[]) AS w, plainto_tsquery('english', w) AS t
		     WHERE numnode(t) > 0)
		 SELECT v.id, v.source, v.external_id, v.title, s.idx, s.start_ms, s.end_ms, s.text
		 FROM q, subtitle_segments s JOIN videos v ON v.id = s.video_id
		 WHERE s.tsv @@ q.query
		 ORDER BY v.id, s.idx
		 LIMIT $2`, words, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SubtitleMatch, error) {
		var m SubtitleMatch
		err := row.Scan(&m.VideoID, &m.Source, &m.ExternalID, &m.Title, &m.Idx, &m.StartMS, &m.EndMS, &m.Text)
		return m, err
	})
}
