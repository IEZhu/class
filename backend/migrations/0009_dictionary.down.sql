-- Откат S3-2/S3-4: исчезают термины с кэшем переводов и импортированный
-- словарь; после повторного наката словарь импортируется заново
-- (cmd/dictimport), кэш переводов наполнится кликами.

BEGIN;

DROP TABLE definitions;
DROP TABLE definition_imports;
DROP TABLE terms;

COMMIT;
