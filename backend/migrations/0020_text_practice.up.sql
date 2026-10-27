-- Этап 5 (S5-7) и долг D-15 (ADR-035): леммы сгенерированного текста для
-- поиска тексто-практики и индекс глобальной уборки протухших сессий.

BEGIN;

-- Леммы всех слов текста (tatoeba.Lemmas — тот же ключ, что у
-- tatoeba_sentences.lemmas и terms.lemma): по ним поиск тексто-практики
-- считает, сколько слов из словаря покрывает текст. NULL — текст сохранён
-- до 0020: поиск берёт target_lemmas, их валидатор гарантирует в тексте.
-- Индекса нет: ищутся только свои тексты, их выбирает generated_texts_user_idx.
ALTER TABLE generated_texts ADD COLUMN lemmas text[]
    CONSTRAINT generated_texts_lemmas_nonempty CHECK (cardinality(lemmas) > 0);

-- Уборка session_cleanup удаляет протухшие сессии пачками от самых старых:
-- без индекса каждая пачка — полный проход sessions
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

COMMIT;
