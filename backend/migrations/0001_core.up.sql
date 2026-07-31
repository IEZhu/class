-- Этап 0 (S0-2): ядро схемы — users, groups, group_members, lessons,
-- lesson_participants, materials. Скетч: docs/architecture/03-data-model.md,
-- конвенции DDL: ADR-005 (identity PK, timestamptz, text+CHECK, created_at).

BEGIN;

CREATE TABLE users (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      text        NOT NULL UNIQUE,
    role       text        NOT NULL CHECK (role IN ('teacher', 'student')),
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE groups (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL,
    level      text        NOT NULL -- CEFR
               CHECK (level IN ('A1', 'A2', 'B1', 'B2', 'C1', 'C2')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE group_members (
    group_id bigint NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    user_id  bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user_idx ON group_members (user_id);

CREATE TABLE lessons (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_id         bigint      NOT NULL REFERENCES groups (id),
    teacher_id       bigint      NOT NULL REFERENCES users (id),
    starts_at        timestamptz NOT NULL,
    ends_at          timestamptz NOT NULL,
    status           text        NOT NULL DEFAULT 'scheduled'
                     CHECK (status IN ('scheduled', 'live', 'processing', 'done')),
    -- Поля будущих этапов (ADR-005): заполняются в S1/S2; FK на transcripts
    -- добавит миграция этапа 2 — таблицы ещё нет.
    gcal_event_id    text,
    livekit_room     text,
    recording_s3_key text,
    transcript_id    bigint,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);
CREATE INDEX lessons_group_idx   ON lessons (group_id, starts_at);
CREATE INDEX lessons_teacher_idx ON lessons (teacher_id, starts_at);

CREATE TABLE lesson_participants (
    lesson_id bigint  NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    user_id   bigint  NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    attended  boolean NOT NULL DEFAULT false,
    PRIMARY KEY (lesson_id, user_id)
);
CREATE INDEX lesson_participants_user_idx ON lesson_participants (user_id);

CREATE TABLE materials (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lesson_id  bigint      NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('material', 'homework')),
    title      text        NOT NULL,
    body_md    text        NOT NULL DEFAULT '',
    s3_key     text, -- файлы в S3 — этап 1 (долг D-4)
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX materials_lesson_idx ON materials (lesson_id);

COMMIT;
