-- Этап 0 (S0-3): auth — пароли и серверные сессии (ADR-006).
-- pgcrypto: bcrypt-хэши в seed (crypt/gen_salt); в PG13+ расширение trusted,
-- владельцу БД суперпользователь не нужен.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

ALTER TABLE users ADD COLUMN password_hash text;

CREATE TABLE sessions (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- В БД не хранится сам токен — только sha256-дайджест (hex)
    token_hash text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

COMMIT;
