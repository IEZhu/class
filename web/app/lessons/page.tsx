import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";
import type { ReactNode } from "react";

import { apiFetch, ApiError } from "../../lib/api";
import type { Group, Lesson, User } from "../../lib/api";
import { lessonPhase } from "../../lib/lesson-phase";
import type { LessonPhase } from "../../lib/lesson-phase";
import { lessonTitle } from "../../lib/lesson-title";
import type { Role } from "../../lib/roles";
import { buttonClass } from "../ui/button";
import { cx } from "../ui/cx";
import { EmptyState } from "../ui/empty";
import { Icon } from "../ui/icons";
import { LocalTime } from "../ui/local-time";
import { PageHeader } from "../ui/page-header";
import { PhasePill, Pill } from "../ui/pill";
import UserBar from "../user-bar";
import styles from "./lessons.module.css";
import NewEvent from "./new-event";

export const metadata: Metadata = { title: "Уроки" };

const TITLE = "Уроки";

// Время урока — в поясе зрителя (ui/local-time); без JS — по UTC с
// подписью «UTC» в конце интервала. День и месяц в блоке даты — без подписи.
const timeOpts: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit" };
const dayOpts: Intl.DateTimeFormatOptions = { day: "numeric" };

type Row = { lesson: Lesson; phase: LessonPhase };

// Группы списка (DESIGN §6.2) по фазе — той же, что на странице урока:
// «Сейчас» — идущие, «Скоро» — ближайший первым, «Прошедшие» — свежий первым.
function groupByPhase(lessons: Lesson[], now: Date) {
  const rows: Row[] = lessons.map((lesson) => ({
    lesson,
    phase: lessonPhase(lesson.status, lesson.starts_at, lesson.ends_at, now),
  }));
  const byStart = (a: Row, b: Row) => Date.parse(a.lesson.starts_at) - Date.parse(b.lesson.starts_at);
  return {
    live: rows.filter((r) => r.phase === "live").sort(byStart),
    soon: rows.filter((r) => r.phase === "scheduled").sort(byStart),
    past: rows.filter((r) => r.phase !== "live" && r.phase !== "scheduled").sort((a, b) => byStart(b, a)),
  };
}

// Подсказка пустого списка по роли (DESIGN §4.3): админ уроков не ведёт —
// ему ссылка на «Люди и группы»; у гостя всегда есть его событие.
const emptyHints: Partial<Record<Role, string>> = {
  admin: "Админ управляет людьми и группами, уроки ведут преподаватели.",
  teacher: "Создайте урок для группы или открытое событие кнопкой «Новое событие».",
  student: "Уроки появятся здесь, когда преподаватель их назначит.",
};

export default async function LessonsPage() {
  let me: User;
  let lessons: Lesson[];
  try {
    [me, lessons] = await Promise.all([
      apiFetch<User>("/auth/me"),
      apiFetch<Lesson[]>("/lessons"),
    ]);
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }

  // Группы нужны только форме создания, а она есть лишь у преподавателя:
  // студенту и гостю api ответит 403, админу форма не показывается.
  let groups: Group[] = [];
  if (me.role === "teacher") {
    groups = await apiFetch<Group[]>("/groups").catch(() => []);
  }

  const now = new Date();
  const { live, soon, past } = groupByPhase(lessons, now);

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page page--narrow">
        {me.role === "teacher" ? <NewEvent title={TITLE} groups={groups} /> : <PageHeader title={TITLE} />}
        {lessons.length === 0 ? (
          <div className="card">
            <EmptyState
              icon="lessons"
              title={me.role === "admin" ? "Уроков нет" : "Пока нет уроков"}
              action={
                me.role === "admin" && (
                  <Link href="/admin" className={buttonClass({ variant: "secondary" })}>
                    <Icon name="people" />
                    Люди и группы
                  </Link>
                )
              }
            >
              {emptyHints[me.role]}
            </EmptyState>
          </div>
        ) : (
          <div className={styles.groups}>
            {live.length > 0 && (
              <LessonGroup id="lessons-now" title="Сейчас">
                {live.map((r) => (
                  <LiveLesson key={r.lesson.id} lesson={r.lesson} />
                ))}
              </LessonGroup>
            )}
            {soon.length > 0 && (
              <LessonGroup id="lessons-soon" title="Скоро">
                {soon.map((r) => (
                  <LessonRow key={r.lesson.id} row={r} now={now} />
                ))}
              </LessonGroup>
            )}
            {past.length > 0 && (
              <LessonGroup id="lessons-past" title="Прошедшие">
                {past.map((r) => (
                  <LessonRow key={r.lesson.id} row={r} now={now} />
                ))}
              </LessonGroup>
            )}
          </div>
        )}
      </main>
    </>
  );
}

function LessonGroup({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section className={styles.group} aria-labelledby={id}>
      <h2 id={id} className={styles.groupTitle}>
        {title}
      </h2>
      {children}
    </section>
  );
}

// «21:00–22:00»: начало и конец отдельно, а не formatRange — тот у урока
// через полночь печатает обе даты целиком, а день и так виден в блоке даты
function TimeRange({ lesson }: { lesson: Lesson }) {
  return (
    <span className={styles.time}>
      <LocalTime value={lesson.starts_at} options={timeOpts} utcLabel={false} />–
      <LocalTime value={lesson.ends_at} options={timeOpts} />
    </span>
  );
}

// Открытое событие (без группы) помечено бейджем: на него зовут гостей
function EventPill({ lesson }: { lesson: Lesson }) {
  return lesson.group_id === null ? <Pill tone="accent">Открытое событие</Pill> : null;
}

// Идущий урок — крупная карточка с кнопкой входа в класс
function LiveLesson({ lesson }: { lesson: Lesson }) {
  return (
    <article className={cx("card", styles.live)}>
      <div className={styles.liveInfo}>
        <div className="meta">
          <PhasePill phase="live" />
          <span>
            <Icon name="clock" size="sm" />
            <TimeRange lesson={lesson} />
          </span>
          <EventPill lesson={lesson} />
        </div>
        <h3 className={styles.liveTitle}>{lessonTitle(lesson)}</h3>
      </div>
      <Link href={`/lesson/${lesson.id}`} className={buttonClass({ variant: "primary", size: "lg", blockOnPhone: true })}>
        <Icon name="cam" />
        Открыть урок
      </Link>
    </article>
  );
}

// Строка урока — вся карточка ссылка: блок даты, название, время и статус
function LessonRow({ row: { lesson, phase }, now }: { row: Row; now: Date }) {
  // Год — только у уроков не текущего года, иначе «окт. чт» без него
  const otherYear = new Date(lesson.starts_at).getUTCFullYear() !== now.getUTCFullYear();
  const monthOpts: Intl.DateTimeFormatOptions = otherYear
    ? { month: "short", year: "numeric" }
    : { month: "short", weekday: "short" };
  return (
    <Link href={`/lesson/${lesson.id}`} className={cx("card", styles.row)}>
      <span className={styles.date}>
        <span className={styles.day}>
          <LocalTime value={lesson.starts_at} options={dayOpts} utcLabel={false} />
        </span>
        <span className={styles.month}>
          <LocalTime value={lesson.starts_at} options={monthOpts} utcLabel={false} />
        </span>
      </span>
      <div className={styles.rowBody}>
        <h3 className={styles.rowTitle}>{lessonTitle(lesson)}</h3>
        <div className="meta">
          <TimeRange lesson={lesson} />
          <PhasePill phase={phase} />
          <EventPill lesson={lesson} />
        </div>
      </div>
      <Icon name="chev" className={styles.chev} />
    </Link>
  );
}
