-- Этап 5 (S5-4): корпус Tatoeba — английские предложения с русским
-- переводом для примеров в попапе слова и тексто-практики (ADR-028).
-- Наполняет cmd/tatoebaimport из поязыковых выгрузок tatoeba.org.

BEGIN;

-- Прогоны импорта: контрольная точка (фаза и строка её файла), с которой
-- продолжается оборванный прогон, и метка, по которой завершённый прогон
-- удаляет то, чего нет в новой выгрузке.
CREATE TABLE tatoeba_imports (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- ETag/Last-Modified или размер и mtime трёх файлов плюс потолки:
    -- продолжать можно только ту же выгрузку
    source_version text        NOT NULL DEFAULT '',
    -- ru — русские предложения, en — английские вместе с парами
    phase          text        NOT NULL DEFAULT 'ru' CHECK (phase IN ('ru', 'en')),
    -- Строк файла фазы учтено; фиксируется в одной транзакции с пачкой
    lines_done     bigint      NOT NULL DEFAULT 0,
    finished_at    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- Корпус один, поэтому и незавершённый прогон — один: повторный запуск
-- продолжает его, а не начинает второй
CREATE UNIQUE INDEX tatoeba_imports_running_key ON tatoeba_imports ((true))
    WHERE finished_at IS NULL;

CREATE TABLE tatoeba_sentences (
    -- id предложения в Tatoeba, а не свой (ADR-028): по нему ссылка
    -- атрибуции на tatoeba.org и upsert при повторном импорте
    id        bigint PRIMARY KEY CHECK (id > 0),
    lang      text   NOT NULL CHECK (lang IN ('en', 'ru')),
    text      text   NOT NULL CHECK (text <> ''),
    -- Имя автора на Tatoeba: CC BY 2.0 FR требует его в подписи;
    -- '' — у предложения нет владельца
    author    text   NOT NULL DEFAULT '',
    -- Леммы слов английского предложения (internal/lemma, ADR-017), по
    -- ним клик находит примеры термина; у русского — NULL
    lemmas    text[],
    import_id bigint NOT NULL REFERENCES tatoeba_imports (id),
    CONSTRAINT tatoeba_sentences_lemmas_en
        CHECK ((lang = 'en') = (lemmas IS NOT NULL AND cardinality(lemmas) > 0))
);

-- Поиск английских предложений по лемме: lemmas @> ARRAY[лемма]
CREATE INDEX tatoeba_sentences_lemmas_idx ON tatoeba_sentences USING gin (lemmas)
    WHERE lang = 'en';

-- Пары перевода en → ru (в выгрузке связь симметрична, храним одно
-- направление)
CREATE TABLE tatoeba_links (
    en_id     bigint NOT NULL REFERENCES tatoeba_sentences (id),
    ru_id     bigint NOT NULL REFERENCES tatoeba_sentences (id),
    import_id bigint NOT NULL REFERENCES tatoeba_imports (id),
    PRIMARY KEY (en_id, ru_id)
);

-- Обратный поиск: проверка внешнего ключа и уборка русских предложений
-- без пары при завершении импорта
CREATE INDEX tatoeba_links_ru_idx ON tatoeba_links (ru_id);

COMMIT;
