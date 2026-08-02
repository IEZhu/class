-- Этап 1 (S1-10): одноразовые ссылки-приглашения (ADR-008). В БД — только
-- sha256-дайджест токена, как у sessions (ADR-006): утечка дампа не даёт
-- рабочих ссылок.

BEGIN;

CREATE TABLE invites (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash       text        NOT NULL UNIQUE,
    email            text        NOT NULL,
    name             text        NOT NULL,
    role             text        NOT NULL CHECK (role IN ('admin', 'teacher', 'student')),
    -- Группа опциональна и назначается только админом (ADR-007); при удалении
    -- группы приглашение остаётся валидным, просто без зачисления.
    group_id         bigint      REFERENCES groups (id) ON DELETE SET NULL,
    created_by       bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    -- Одноразовость: непустой accepted_at закрывает ссылку навсегда
    accepted_at      timestamptz,
    accepted_user_id bigint      REFERENCES users (id) ON DELETE SET NULL
);

-- Список ожидающих приглашений — частый запрос админ-кабинета
CREATE INDEX invites_pending_idx ON invites (created_by, expires_at)
    WHERE accepted_at IS NULL;

COMMIT;
