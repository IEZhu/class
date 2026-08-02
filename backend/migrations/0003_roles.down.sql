-- Откат S1-8: возврат к двум ролям. Существующие админы понижаются до
-- teacher — иначе CHECK не примут уже лежащие строки; это осознанная
-- потеря прав, а не данных (ADR-007).

BEGIN;

UPDATE users SET role = 'teacher' WHERE role = 'admin';

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('teacher', 'student'));

COMMIT;
