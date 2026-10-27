-- Этап 5 (S5-6): тексты, сгенерированные LLM по целевым словам
-- (ADR-029, ADR-030), и счётчик генераций для суточного лимита.
-- Хранится только текст, прошедший валидатор покрытия.

BEGIN;

CREATE TABLE generated_texts (
    id            bigint           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Кто заказал: история GET /practice/texts — только своя
    user_id       bigint           NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Параметры промпта: уровень CEFR, жанр из белого списка, потолок слов
    level         text             NOT NULL CHECK (level IN ('A1', 'A2', 'B1', 'B2', 'C1', 'C2')),
    genre         text             NOT NULL CHECK (genre <> ''),
    max_words     integer          NOT NULL CHECK (max_words > 0),
    -- Целевые слова — леммами из internal/lemma, тем же ключом, что
    -- terms.lemma: список из запроса может содержать слова, которых в
    -- terms ещё нет (ADR-030)
    target_lemmas text[]           NOT NULL CHECK (cardinality(target_lemmas) > 0),
    -- Текст абзацами через пустую строку, без разметки
    body          text             NOT NULL CHECK (body <> ''),
    word_count    integer          NOT NULL CHECK (word_count > 0),
    -- Итог валидатора: доля целевых лемм, встреченных не реже двух раз,
    -- вхождения по леммам {"run": 3} и доля слов вне словаря OEWN
    coverage      double precision NOT NULL CHECK (coverage BETWEEN 0 AND 1),
    occurrences   jsonb            NOT NULL CHECK (jsonb_typeof(occurrences) = 'object'),
    unknown_share double precision NOT NULL CHECK (unknown_share BETWEEN 0 AND 1),
    -- 2 — первый черновик не прошёл валидатор, принят ретрай
    attempts      smallint         NOT NULL CHECK (attempts IN (1, 2)),
    provider      text             NOT NULL CHECK (provider <> ''),
    model         text             NOT NULL CHECK (model <> ''),
    created_at    timestamptz      NOT NULL DEFAULT now()
);

CREATE INDEX generated_texts_user_idx ON generated_texts (user_id, created_at DESC);

-- Суточный лимит генераций на пользователя (ADR-029): запрос резервирует
-- место до вызова провайдера, сутки — по UTC
CREATE TABLE llm_usage (
    user_id  bigint  NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    day      date    NOT NULL,
    requests integer NOT NULL CHECK (requests > 0),
    PRIMARY KEY (user_id, day)
);

COMMIT;
