-- Этап 1 (S1-1): Google Calendar преподавателя (ADR-023). Колонка
-- lessons.gcal_event_id есть с 0001; здесь — refresh token организатора.

BEGIN;

-- Только шифртекст AES-256-GCM (версия ‖ nonce ‖ шифртекст с тегом,
-- ключ — GOOGLE_TOKEN_KEY в deploy/.env); открытый токен в БД не попадает.
-- NULL — календарь не подключён.
ALTER TABLE users ADD COLUMN google_refresh_token bytea;

COMMIT;
