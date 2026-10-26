-- Этап 1 (S1-2): очередь фоновых задач в Postgres (ADR-003, семантика —
-- ADR-013). Джобу берут SELECT … FOR UPDATE SKIP LOCKED, выполненная
-- удаляется, поэтому в таблице живут только ожидающие, захваченные
-- и «мёртвые» джобы.

BEGIN;

CREATE TABLE jobs (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind       text        NOT NULL CHECK (kind <> ''),
    payload    jsonb       NOT NULL DEFAULT '{}',
    -- Раньше этого момента джобу не брать: так откладываются ретраи
    run_at     timestamptz NOT NULL DEFAULT now(),
    -- Сколько раз джобу захватывали; заодно токен владения: worker
    -- отписывает результат только за ту попытку, которую захватил сам
    attempts   integer     NOT NULL DEFAULT 0,
    last_error text,
    -- Захват: кто и когда взял. Захват старше таймаута — брошенный
    -- (worker упал), его подбирает следующий
    locked_at  timestamptz,
    locked_by  text,
    -- Сверх модели данных (ADR-013): попытки исчерпаны — джоба остаётся
    -- для разбора, но больше не берётся
    failed_at  timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jobs_lock_pair CHECK ((locked_at IS NULL) = (locked_by IS NULL))
);

-- Запрос захвата: живые джобы по run_at. Мёртвые в индекс не попадают
-- и не мешают очереди, сколько бы их ни накопилось.
CREATE INDEX jobs_ready_idx ON jobs (run_at, id) WHERE failed_at IS NULL;

COMMIT;
