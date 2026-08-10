-- Откат S1-11. Открытые события и гости исчезают: у уроков без группы
-- её взять неоткуда, поэтому такие уроки удаляются вместе с гостями.

BEGIN;

DELETE FROM lessons WHERE group_id IS NULL;
DELETE FROM users WHERE role = 'guest';

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'teacher', 'student'));

ALTER TABLE lessons DROP COLUMN guest_token_hash;
ALTER TABLE lessons DROP CONSTRAINT lessons_named;
ALTER TABLE lessons DROP COLUMN title;
ALTER TABLE lessons ALTER COLUMN group_id SET NOT NULL;

COMMIT;
