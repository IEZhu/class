-- Этап 1 (S1-11): открытые события и гостевой доступ (ADR-010).
-- Событие — это урок без группы: тот же жизненный цикл, та же комната
-- lesson-{id}, та же запись и транскрипт.

BEGIN;

-- Группы у открытого события нет: люди приходят по ссылке, а не составом
ALTER TABLE lessons ALTER COLUMN group_id DROP NOT NULL;

-- Заголовок нужен там, где нет имени группы. У обычного урока остаётся
-- пустым — подпись берётся из группы, как и раньше.
ALTER TABLE lessons ADD COLUMN title text;

-- Хотя бы что-то одно должно давать уроку название
ALTER TABLE lessons ADD CONSTRAINT lessons_named
    CHECK (group_id IS NOT NULL OR title IS NOT NULL);

-- Гостевая ссылка на событие: в БД, как обычно, только sha256-дайджест
-- (ADR-006). Одна ссылка на событие; отзыв — NULL, перевыпуск — новый хэш.
-- В отличие от приглашений (ADR-008) ссылка многоразовая: по ней заходят все.
ALTER TABLE lessons ADD COLUMN guest_token_hash text UNIQUE;

-- Четвёртая роль: гость живёт только на время события, пароля не имеет
-- и войти повторно не может (ADR-010).
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'teacher', 'student', 'guest'));

COMMIT;
