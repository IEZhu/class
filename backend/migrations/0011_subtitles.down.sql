-- Откат S5-1/S5-2: корпус субтитров и расписание периодических джоб
-- исчезают. Поставленные rss_poll/subtitles_fetch остаются в jobs и ждут
-- worker'а, который их выполнит, — после отката их стоит удалить руками.

BEGIN;

DROP TABLE subtitle_segments;
DROP TABLE videos;
DROP TABLE subtitle_sources;
DROP TABLE job_schedules;

COMMIT;
