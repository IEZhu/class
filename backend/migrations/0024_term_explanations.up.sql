-- Этап 5 (S5-8, часть 1): «объясни» в попапе слова (ADR-039) — кэш
-- объяснений LLM по термину и предложению и отдельный суточный счётчик
-- объяснений в llm_usage.

BEGIN;

-- Объяснение слова в предложении: повторный клик по тому же слову в том
-- же предложении (у любого пользователя) провайдера не зовёт
CREATE TABLE term_explanations (
    term_id      bigint      NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
    -- sha256 нормализованного предложения (internal/explain.Context)
    context_hash bytea       NOT NULL CHECK (length(context_hash) = 32),
    -- Само нормализованное предложение: разбор ответов и проверка ключа
    context      text        NOT NULL CHECK (context <> ''),
    -- Ответ модели простым текстом, абзацы через пустую строку, обрезан
    -- до потолка explain.MaxRunes
    body         text        NOT NULL CHECK (body <> ''),
    provider     text        NOT NULL CHECK (provider <> ''),
    model        text        NOT NULL CHECK (model <> ''),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (term_id, context_hash)
);

-- Счётчик лимита — по виду вызова: объяснения дешевле генерации на
-- порядок и не съедают её лимит. Строки S5-6 — генерации
ALTER TABLE llm_usage ADD COLUMN kind text NOT NULL DEFAULT 'generate'
    CHECK (kind IN ('generate', 'explain'));
ALTER TABLE llm_usage ALTER COLUMN kind DROP DEFAULT;
ALTER TABLE llm_usage DROP CONSTRAINT llm_usage_pkey;
ALTER TABLE llm_usage ADD PRIMARY KEY (user_id, day, kind);

COMMIT;
