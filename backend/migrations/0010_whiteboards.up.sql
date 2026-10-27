-- Этап 3 (S3-6): доска урока (ADR-020). Ретранслятор Excalidraw состояния
-- не держит, поэтому текущая сцена живёт здесь, а worker снимает её копию
-- в хранилище: whiteboards/{lesson_id}.json (05-storage-s3.md).

BEGIN;

-- Одна доска на урок; строка заводится при первом открытии доски
CREATE TABLE whiteboards (
    lesson_id        bigint      PRIMARY KEY REFERENCES lessons (id) ON DELETE CASCADE,
    -- Сверх модели данных (ADR-020): комната ретранслятора, 128 случайных
    -- бит base64url. Ретранслятор пускает в любую комнату по её имени,
    -- поэтому имя не выводится из id урока и выдаётся только участникам
    room_id          text        NOT NULL UNIQUE CHECK (length(room_id) = 22),
    -- Сверх модели данных: ключ AES-GCM комнаты, base64url
    -- 128 бит. Им клиенты шифруют сообщения через ретранслятор, и
    -- выдаётся он только участникам урока
    room_key         text        NOT NULL CHECK (length(room_key) = 22),
    -- Сверх модели данных: элементы сцены Excalidraw как есть, вместе
    -- с удалёнными (isDeleted), — по ним сливаются сохранения участников.
    -- Текстовые элементы отсюда разберёт S3-7
    scene            jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(scene) = 'array'),
    -- Растёт с каждым сохранением; снимок помнит, какую версию он унёс
    scene_version    bigint      NOT NULL DEFAULT 0,
    snapshot_version bigint      NOT NULL DEFAULT 0,
    -- Путь снимка в хранилище; пусто, пока снимка не было
    scene_s3_key     text,
    -- Время последнего изменения сцены
    updated_at       timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now()
);

COMMIT;
