-- Seed стенда (S0-2): преподаватель, тестовая группа, 3 студента, ближайший
-- урок с домашкой. Идемпотентен: повторный запуск ничего не дублирует.
-- Запуск: make seed (psql -v ON_ERROR_STOP=1). Данные обезличенные —
-- репозиторий публичный.

BEGIN;

INSERT INTO users (email, role, name) VALUES
    ('teacher@lingua.local',  'teacher', 'Test Teacher'),
    ('student1@lingua.local', 'student', 'Test Student One'),
    ('student2@lingua.local', 'student', 'Test Student Two'),
    ('student3@lingua.local', 'student', 'Test Student Three')
ON CONFLICT (email) DO NOTHING;

INSERT INTO groups (name, level)
SELECT 'Test Group', 'A2'
WHERE NOT EXISTS (SELECT 1 FROM groups WHERE name = 'Test Group');

INSERT INTO group_members (group_id, user_id)
SELECT g.id, u.id
FROM groups g
JOIN users u ON u.email IN
    ('student1@lingua.local', 'student2@lingua.local', 'student3@lingua.local')
WHERE g.name = 'Test Group'
ON CONFLICT DO NOTHING;

-- Урок через 2 дня, 60 минут, только если у группы ещё нет уроков
INSERT INTO lessons (group_id, teacher_id, starts_at, ends_at)
SELECT g.id, t.id,
       date_trunc('hour', now()) + interval '2 days',
       date_trunc('hour', now()) + interval '2 days 1 hour'
FROM groups g
JOIN users t ON t.email = 'teacher@lingua.local'
WHERE g.name = 'Test Group'
  AND NOT EXISTS (SELECT 1 FROM lessons l WHERE l.group_id = g.id);

-- Участники — только уроки фикстурной группы (не трогаем чужие уроки
-- при запуске на непустой базе)
INSERT INTO lesson_participants (lesson_id, user_id)
SELECT l.id, gm.user_id
FROM groups g
JOIN lessons l ON l.group_id = g.id
JOIN group_members gm ON gm.group_id = g.id
WHERE g.name = 'Test Group'
ON CONFLICT DO NOTHING;

-- Домашка — к самому раннему уроку фикстурной группы без домашки
INSERT INTO materials (lesson_id, kind, title, body_md)
SELECT l.id, 'homework', 'First homework',
       E'# Homework\n\nRead the text and mark unknown words.'
FROM groups g
JOIN lessons l ON l.group_id = g.id
WHERE g.name = 'Test Group'
  AND NOT EXISTS (
      SELECT 1 FROM materials m WHERE m.lesson_id = l.id AND m.kind = 'homework'
  )
ORDER BY l.starts_at
LIMIT 1;

COMMIT;
