-- Откат S5-4: корпус Tatoeba исчезает, клик по слову отвечает без
-- примеров (examples: []); после повторного наката корпус импортируется
-- заново (cmd/tatoebaimport).

BEGIN;

DROP TABLE tatoeba_links;
DROP TABLE tatoeba_sentences;
DROP TABLE tatoeba_imports;

COMMIT;
