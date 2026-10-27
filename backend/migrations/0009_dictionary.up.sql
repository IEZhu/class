-- Этап 3 (S3-2, S3-4): общий словарь terms — лемма и кэш переводов
-- (ADR-017), и словарный датасет definitions из выгрузки Wiktionary
-- (ADR-019). Личное состояние слова (user_terms) появится в S3-5,
-- кандидаты словаря урока — в S3-7.

BEGIN;

CREATE TABLE terms (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lang              text        NOT NULL DEFAULT 'en' CHECK (lang IN ('en')),
    -- Ключ термина — лемма из internal/lemma (нижний регистр): формы
    -- run, ran, running — один термин с одним переводом
    lemma             text        NOT NULL CHECK (lemma <> ''),
    -- Форма, в которой слово кликнули первым (регистр: English, iPhone;
    -- разбор промахов лемматизации)
    surface           text        NOT NULL CHECK (surface <> ''),
    -- Кэш переводов по целевому языку: {"ru": {"text", "source", "at"}}.
    -- Повторный клик берёт перевод отсюда и не ходит во внешние сервисы
    translation_cache jsonb       NOT NULL DEFAULT '{}'
                                  CHECK (jsonb_typeof(translation_cache) = 'object'),
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- Индекс уникальности — он же поиск термина по лемме при клике
    CONSTRAINT terms_lang_lemma_key UNIQUE (lang, lemma)
);

-- Прогоны импорта (cmd/dictimport): контрольная точка, с которой
-- продолжается оборванный прогон, и метка, по которой завершённый
-- прогон удаляет статьи, пропавшие из нового дампа.
CREATE TABLE definition_imports (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source         text        NOT NULL CHECK (source <> ''),
    -- ETag/Last-Modified URL или размер и mtime файла: продолжать можно
    -- только ту же версию источника
    source_version text        NOT NULL DEFAULT '',
    -- Строк источника учтено; фиксируется в одной транзакции со статьями
    lines_done     bigint      NOT NULL DEFAULT 0,
    entries        bigint      NOT NULL DEFAULT 0,
    finished_at    timestamptz,
    -- Брошен рестартом (-restart): вместо него идёт новый прогон, который
    -- перезапишет или удалит статьи брошенного
    abandoned_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT definition_imports_closed_once CHECK (finished_at IS NULL OR abandoned_at IS NULL)
);

-- Незавершённый прогон на источник — один: повторный запуск продолжает
-- его, а не начинает второй
CREATE UNIQUE INDEX definition_imports_running_key ON definition_imports (source)
    WHERE finished_at IS NULL AND abandoned_at IS NULL;

CREATE TABLE definitions (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Заголовок статьи как в Wiktionary: регистр различает статьи (US, us)
    word            text        NOT NULL CHECK (word <> ''),
    -- lemma.Normalize(word) — по нему ищет клик
    lemma           text        NOT NULL CHECK (lemma <> ''),
    pos             text        NOT NULL CHECK (pos <> ''),
    -- Номер этимологии омонима (bank¹ и bank²); 0 — этимология одна
    etymology       smallint    NOT NULL DEFAULT 0 CHECK (etymology >= 0),
    glosses         text[]      NOT NULL CHECK (cardinality(glosses) > 0),
    translations_ru text[]      NOT NULL DEFAULT '{}',
    -- Строка источника: порядок статей как на странице Wiktionary
    source_line     bigint      NOT NULL,
    import_id       bigint      NOT NULL REFERENCES definition_imports (id),
    CONSTRAINT definitions_entry_key UNIQUE (word, pos, etymology)
);

CREATE INDEX definitions_lemma_idx ON definitions (lemma);

COMMIT;
