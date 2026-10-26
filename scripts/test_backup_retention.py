"""Retention 7/30 бэкапов Postgres (S1-7, ADR-014): retention_plan из deploy/backup-lib.sh.

Функция чистая — «сегодня» аргументом, ключи на stdin, — поэтому её решение
проверяется без хранилища и часов; backup-pg.sh --prune-only удаляет ровно
то, что она назвала delete.
"""
import datetime
import pathlib
import subprocess
import unittest

LIB = pathlib.Path(__file__).resolve().parent.parent / "deploy" / "backup-lib.sh"
TODAY = datetime.date(2026, 10, 26)
KEEP_DAYS = 30


def nightly(day):
    return f"backups/pg/{day.isoformat()}.dump.zst"


def days_back(n, today=TODAY):
    """Ночные ключи за сегодня и n предыдущих дней."""
    return [nightly(today - datetime.timedelta(days=i)) for i in range(n + 1)]


def plan(today, keys):
    proc = subprocess.run(
        ["bash", "-c", 'SCRIPT_NAME=retention-test; source "$1"; retention_plan "$2"',
         "retention-test", str(LIB), today.isoformat()],
        input="".join(k + "\n" for k in keys), capture_output=True, text=True, check=False)
    verdicts = {"keep": set(), "delete": set(), "future": set()}
    for line in proc.stdout.splitlines():
        verdict, key = line.split(" ", 1)
        verdicts[verdict].add(key)
    return proc.returncode, verdicts


class RetentionPlanTests(unittest.TestCase):
    def assertPlan(self, nightly_keys, keep, extra=()):
        """Ночные ключи делятся ровно на keep и delete, extra не упомянуты."""
        rc, got = plan(TODAY, list(nightly_keys) + list(extra))
        self.assertEqual(rc, 0)
        self.assertEqual(got["keep"], set(keep))
        self.assertEqual(got["delete"], set(nightly_keys) - set(keep))
        self.assertEqual(got["future"], set())
        return got

    def test_46_nightly_objects_keep_7_daily_and_4_weekly(self):
        extra = [
            "backups/pg/manual-pre-0006.dump.zst",
            "backups/pg/incoming/2026-10-26.4242.dump.zst",
            "recordings/1/room.mp4",
        ]
        keep = days_back(6) + [
            "backups/pg/2026-10-18.dump.zst",
            "backups/pg/2026-10-11.dump.zst",
            "backups/pg/2026-10-04.dump.zst",
            "backups/pg/2026-09-27.dump.zst",
        ]
        got = self.assertPlan(days_back(45), keep, extra)
        self.assertEqual(len(got["delete"]), 35)

    def test_missing_sunday_keeps_newest_copy_of_that_week(self):
        keys = [k for k in days_back(45) if k != "backups/pg/2026-10-18.dump.zst"]
        rc, got = plan(TODAY, keys)
        self.assertEqual(rc, 0)
        self.assertIn("backups/pg/2026-10-17.dump.zst", got["keep"])
        self.assertIn("backups/pg/2026-10-12.dump.zst", got["delete"])
        self.assertEqual(len(got["keep"]), 11)

    def test_weekly_copy_kept_up_to_30_days_only(self):
        daily = days_back(6)
        self.assertPlan(daily + ["backups/pg/2026-09-26.dump.zst"],
                        daily + ["backups/pg/2026-09-26.dump.zst"])
        self.assertPlan(daily + ["backups/pg/2026-09-25.dump.zst"], daily)

    def test_future_dated_key_is_flagged_not_kept_or_deleted(self):
        future = ["backups/pg/2099-12-31.dump.zst", "backups/pg/2026-10-27.dump.zst"]
        rc, base = plan(TODAY, days_back(45))
        rc_f, got = plan(TODAY, days_back(45) + future)
        self.assertEqual((rc, rc_f), (0, 0))
        self.assertEqual(got["future"], set(future))
        self.assertEqual(got["keep"], base["keep"])
        self.assertEqual(got["delete"], base["delete"])

    def test_future_dated_key_does_not_count_as_fresh_copy(self):
        keys = days_back(45, TODAY - datetime.timedelta(days=10)) + ["backups/pg/2099-12-31.dump.zst"]
        rc, got = plan(TODAY, keys)
        self.assertNotEqual(rc, 0)
        self.assertEqual(got["delete"], set())

    def test_no_fresh_copy_deletes_nothing(self):
        for keys in (days_back(45, TODAY - datetime.timedelta(days=7)), [],
                     ["backups/pg/manual-pre-0006.dump.zst"]):
            with self.subTest(n=len(keys)):
                rc, got = plan(TODAY, keys)
                self.assertNotEqual(rc, 0)
                self.assertEqual(got, {"keep": set(), "delete": set(), "future": set()})

    def test_manual_and_foreign_keys_are_never_touched(self):
        manual = [
            "backups/pg/manual-pre-0006.dump.zst",
            "backups/pg/2026-09-01-pre-0006.dump.zst",
            "backups/pg/2026-09-01.dump.zst.bak",
            "backups/pg/2026-02-30.dump.zst",
            "backups/pg/incoming/2026-09-01.77.dump.zst",
            "recordings/2026-09-01.dump.zst",
            "backups/pg/2026-09-01.dump",
        ]
        rc, got = plan(TODAY, days_back(45) + manual)
        self.assertEqual(rc, 0)
        listed = got["keep"] | got["delete"] | got["future"]
        self.assertEqual(listed & set(manual), set())
        self.assertEqual(len(listed), 46)

    def test_daily_runs_keep_7_daily_and_a_copy_of_every_week_for_30_days(self):
        start = datetime.date(2026, 8, 1)
        bucket = set()
        for n in range(70):
            day = start + datetime.timedelta(days=n)
            bucket.add(nightly(day))
            rc, got = plan(day, sorted(bucket))
            self.assertEqual(rc, 0)
            bucket -= got["delete"]
            dates = {datetime.date.fromisoformat(k[11:21]) for k in bucket}
            ages = sorted((day - d).days for d in dates)
            self.assertEqual(ages[:7], list(range(min(7, n + 1))))
            self.assertLessEqual(ages[-1], KEEP_DAYS)
            window = {day - datetime.timedelta(days=a) for a in range(KEEP_DAYS + 1)}
            weeks = {d.isocalendar()[:2] for d in window if d >= start}
            self.assertLessEqual(weeks, {d.isocalendar()[:2] for d in dates})
            if n >= KEEP_DAYS:
                self.assertIn(len(bucket), (10, 11))

if __name__ == "__main__":
    unittest.main()
