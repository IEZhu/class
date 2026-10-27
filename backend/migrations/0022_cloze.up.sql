-- Этап 4 (S4-4, S4-5) и S5-8, часть 2: cloze (ADR-037) — пункты
-- «вставь слово» по словам личного словаря из текстов студента,
-- отрицательный кэш генератора, индексы поиска по текстам уроков, кэш
-- LLM-дистракторов и журнал ответов. Ответ, пришедшийся на карточку
-- к показу (или промах), пишется в общий журнал FSRS srs_reviews с
-- source = 'cloze'; попытка ссылается на эту оценку.

BEGIN;

-- Пункт — предложение с пропуском на месте формы слова. Свой у каждого
-- студента: дистракторы — из его словаря. Пункт — часть слова словаря:
-- удаление слова (и учётки) уносит пункты и их попытки
CREATE TABLE cloze_items (
    id                bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           bigint      NOT NULL,
    -- Ответ — лемма этого термина (любая её форма засчитывается)
    term_id           bigint      NOT NULL,
    -- Откуда предложение: реплика транскрипта урока, материал урока,
    -- свой сгенерированный текст, предложение Tatoeba
    source            text        NOT NULL CHECK (source IN ('lesson', 'material', 'generated', 'tatoeba')),
    lesson_id         bigint      REFERENCES lessons (id) ON DELETE CASCADE,
    material_id       bigint      REFERENCES materials (id) ON DELETE CASCADE,
    generated_text_id bigint      REFERENCES generated_texts (id) ON DELETE CASCADE,
    tatoeba_id        bigint      REFERENCES tatoeba_sentences (id) ON DELETE CASCADE,
    -- Снимок предложения: правка источника пункт не меняет
    sentence          text        NOT NULL CHECK (sentence <> '' AND char_length(sentence) <= 300),
    -- Пропуск — символы [blank_start, blank_start + blank_len) предложения
    blank_start       integer     NOT NULL CHECK (blank_start >= 0),
    blank_len         integer     NOT NULL CHECK (blank_len > 0),
    -- sha256 предложения и места пропуска (cloze.Hash): ключ пункта
    -- и кэша LLM-дистракторов
    context_hash      bytea       NOT NULL CHECK (length(context_hash) = 32),
    -- Дистракторы из словаря студента или частых слов (леммы)
    distractors       text[]      NOT NULL CHECK (cardinality(distractors) = 3),
    -- Какой набор вариантов студент уже получил: list — дистракторы
    -- словаря, llm — из кэша cloze_distractors. Набор закрепляется при
    -- первой отдаче и больше не меняется: два набора у одного пункта
    -- выдали бы ответ пересечением. NULL — вариантов ещё не отдавали
    option_source     text        CHECK (option_source IN ('list', 'llm')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT cloze_items_blank_inside CHECK (blank_start + blank_len <= char_length(sentence)),
    -- Ссылка ровно на свой источник; у реплики и материала — их урок
    CONSTRAINT cloze_items_source_ref CHECK (CASE source
        WHEN 'lesson' THEN lesson_id IS NOT NULL AND material_id IS NULL
                           AND generated_text_id IS NULL AND tatoeba_id IS NULL
        WHEN 'material' THEN lesson_id IS NOT NULL AND material_id IS NOT NULL
                             AND generated_text_id IS NULL AND tatoeba_id IS NULL
        WHEN 'generated' THEN lesson_id IS NULL AND material_id IS NULL
                              AND generated_text_id IS NOT NULL AND tatoeba_id IS NULL
        WHEN 'tatoeba' THEN lesson_id IS NULL AND material_id IS NULL
                            AND generated_text_id IS NULL AND tatoeba_id IS NOT NULL
    END),
    FOREIGN KEY (user_id, term_id) REFERENCES user_terms (user_id, term_id) ON DELETE CASCADE,
    -- Одно предложение с одним пропуском — один пункт у пользователя
    CONSTRAINT cloze_items_context_key UNIQUE (user_id, term_id, context_hash),
    -- Цель составного FK попыток: попытка — того же пользователя, что пункт
    CONSTRAINT cloze_items_owner_key UNIQUE (id, user_id)
);

-- Каскады удаления источников без полного прохода таблицы
CREATE INDEX cloze_items_lesson_idx ON cloze_items (lesson_id) WHERE lesson_id IS NOT NULL;
CREATE INDEX cloze_items_material_idx ON cloze_items (material_id) WHERE material_id IS NOT NULL;
CREATE INDEX cloze_items_generated_idx ON cloze_items (generated_text_id) WHERE generated_text_id IS NOT NULL;
CREATE INDEX cloze_items_tatoeba_idx ON cloze_items (tatoeba_id) WHERE tatoeba_id IS NOT NULL;

-- Отрицательный кэш генератора: слово, для которого при проверке не
-- нашлось нового годного предложения. Пока запись свежая (ADR-037: час
-- и не раньше новых материалов, своих текстов, импорта Tatoeba), выдача
-- не ищет слову предложения заново
CREATE TABLE cloze_misses (
    user_id    bigint      NOT NULL,
    term_id    bigint      NOT NULL,
    checked_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, term_id),
    FOREIGN KEY (user_id, term_id) REFERENCES user_terms (user_id, term_id) ON DELETE CASCADE
);

-- Поиск предложений слова в репликах и материалах уроков
-- (store.ClozeLessonTexts): лексемы хранятся, а не считаются в запросе —
-- to_tsvector заново на каждое слово выдачи дороже самого поиска, когда
-- план идёт не по индексу (ADR-037). GIN — по самим колонкам
ALTER TABLE utterances ADD COLUMN text_tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED;
ALTER TABLE materials ADD COLUMN body_tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', body_md)) STORED;
CREATE INDEX utterances_text_tsv_idx ON utterances USING gin (text_tsv);
CREATE INDEX materials_body_tsv_idx ON materials USING gin (body_tsv);

-- Кэш LLM-дистракторов (S5-8, часть 2): прошедшие проверку варианты для
-- слова в предложении с пропуском. Общий: пункт другого студента с тем же
-- предложением провайдера не зовёт
CREATE TABLE cloze_distractors (
    term_id      bigint      NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
    context_hash bytea       NOT NULL CHECK (length(context_hash) = 32),
    options      text[]      NOT NULL CHECK (cardinality(options) = 3),
    provider     text        NOT NULL CHECK (provider <> ''),
    model        text        NOT NULL CHECK (model <> ''),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (term_id, context_hash)
);

-- Журнал ответов. review_id — оценка FSRS, в которую ушёл ответ; NULL —
-- ответ в график не засчитан (верный ответ по карточке, чей показ ещё
-- не наступил, ADR-037)
CREATE TABLE cloze_attempts (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id     bigint      NOT NULL,
    user_id     bigint      NOT NULL,
    -- Ключ идемпотентности клиента: повтор запроса засчитывается один раз
    client_id   uuid        NOT NULL,
    mode        text        NOT NULL CHECK (mode IN ('choice', 'typing')),
    answer      text        NOT NULL CHECK (answer <> '' AND octet_length(answer) <= 64),
    correct     boolean     NOT NULL,
    -- Время ответа по часам браузера: от показа пункта до ответа
    response_ms integer     NOT NULL CHECK (response_ms BETWEEN 0 AND 600000),
    rating      text        NOT NULL CHECK (rating IN ('again', 'good', 'easy')),
    review_id   bigint      REFERENCES srs_reviews (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (item_id, user_id) REFERENCES cloze_items (id, user_id) ON DELETE CASCADE,
    CONSTRAINT cloze_attempts_client_key UNIQUE (user_id, client_id)
);

-- Попытки пункта (выбор следующего предложения слова) и SET NULL при
-- удалении оценки
CREATE INDEX cloze_attempts_item_idx ON cloze_attempts (item_id, created_at);
CREATE INDEX cloze_attempts_review_idx ON cloze_attempts (review_id) WHERE review_id IS NOT NULL;

COMMIT;
