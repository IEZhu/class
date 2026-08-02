-- Этап 1 (S1-8): третья роль admin (ADR-007). Админ заводит людей и тасует
-- группы; teacher остаётся владельцем уроков, student — участником.

BEGIN;

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'teacher', 'student'));

COMMIT;
