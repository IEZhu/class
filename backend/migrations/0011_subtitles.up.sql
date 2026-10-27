-- Этап 5 (S5-1, S5-2): корпус субтитров для видео-практики и его сборщик
-- (ADR-022), плюс расписание периодических джоб (ADR-021): первые
-- периодические джобы — опрос RSS и сбор субтитров.

BEGIN;

-- Периодические джобы (ADR-021): slot — начало периода, за который джоба
-- уже поставлена. Условное обновление строки — единственная точка гонки:
-- из нескольких worker'ов период «выигрывает» ровно один.
CREATE TABLE job_schedules (
    kind text        PRIMARY KEY CHECK (kind <> ''),
    slot timestamptz NOT NULL
);

-- Кураторский список каналов и плейлистов YouTube (seed ниже)
CREATE TABLE subtitle_sources (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text        NOT NULL CHECK (kind IN ('channel', 'playlist')),
    -- UC… у канала, PL… у плейлиста: параметр RSS-фида
    external_id text        NOT NULL CHECK (external_id ~ '^[A-Za-z0-9_-]+$'),
    title       text        NOT NULL,
    enabled     boolean     NOT NULL DEFAULT true,
    -- Сверх модели данных (ADR-022): итог последнего опроса фида
    polled_at   timestamptz,
    last_error  text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, external_id)
);

CREATE TABLE videos (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source      text        NOT NULL CHECK (source IN ('youtube', 'ted')),
    external_id text        NOT NULL CHECK (external_id <> ''),
    title       text        NOT NULL,
    channel     text,
    -- RSS длительности не отдаёт: её пишет сборщик субтитров
    duration_s  integer     CHECK (duration_s >= 0),
    lang        text        NOT NULL DEFAULT 'en',
    added_at    timestamptz NOT NULL DEFAULT now(),
    -- Сверх модели данных (ADR-022): откуда пришло видео и состояние сбора.
    -- subtitles: какие субтитры сохранены (none — английских нет);
    -- NULL — ещё не собраны. Неудачные попытки копятся в harvest_attempts,
    -- harvested_at — время последней попытки.
    source_id        bigint      REFERENCES subtitle_sources (id) ON DELETE SET NULL,
    published_at     timestamptz,
    subtitles        text        CHECK (subtitles IN ('manual', 'auto', 'none')),
    harvest_attempts integer     NOT NULL DEFAULT 0,
    harvest_error    text,
    harvested_at     timestamptz,
    UNIQUE (source, external_id)
);

-- Очередь сборщика: видео без субтитров
CREATE INDEX videos_harvest_idx ON videos (harvest_attempts, published_at DESC)
    WHERE subtitles IS NULL;

CREATE TABLE subtitle_segments (
    video_id bigint  NOT NULL REFERENCES videos (id) ON DELETE CASCADE,
    idx      integer NOT NULL CHECK (idx >= 0),
    start_ms integer NOT NULL CHECK (start_ms >= 0),
    end_ms   integer NOT NULL,
    text     text    NOT NULL CHECK (text <> ''),
    -- Индексируется сырой английский текст словарём english (стемминг
    -- Snowball); видео-практика ищет только по этой колонке
    tsv      tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
    PRIMARY KEY (video_id, idx),
    CONSTRAINT subtitle_segments_span CHECK (end_ms >= start_ms)
);

CREATE INDEX subtitle_segments_tsv_idx ON subtitle_segments USING gin (tsv);

-- Seed сборщика: каналы проверены по их RSS-фидам 2026-10-26
INSERT INTO subtitle_sources (kind, external_id, title) VALUES
    ('channel', 'UCAuUUnT6oDeKwE6v1NGQxug', 'TED'),
    ('channel', 'UCsooa4yRKGN_zEE8iknghZA', 'TED-Ed'),
    ('channel', 'UCHaHD477h-FeBbVh9Sh7syA', 'BBC Learning English'),
    ('channel', 'UCKyTokYo0nK2OA-az-sDijA', 'VOA Learning English');

COMMIT;
