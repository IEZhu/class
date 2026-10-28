import type { Metadata } from "next";
import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import type { ReactNode } from "react";

import { apiFetch, ApiError } from "../../../lib/api";
import type { LessonDetail, LessonTerm, Material, User } from "../../../lib/api";
import { initials } from "../../../lib/initials";
import { lessonTitle } from "../../../lib/lesson-title";
import { lessonPhase } from "../../../lib/lesson-phase";
import type { LessonPhase } from "../../../lib/lesson-phase";
import ClickableText from "../../clickable-text";
import { Alert } from "../../ui/alert";
import { buttonClass } from "../../ui/button";
import { cx } from "../../ui/cx";
import { EmptyState } from "../../ui/empty";
import { Icon } from "../../ui/icons";
import { LocalTime } from "../../ui/local-time";
import { PageHeader } from "../../ui/page-header";
import { PhasePill } from "../../ui/pill";
import UserBar from "../../user-bar";
import GuestLink from "./guest-link";
import LessonDictionary from "./lesson-dictionary";
import LessonReplay from "./lesson-replay";
import LessonRoom from "./lesson-room";
import { DeleteMaterial, FileDownload, MaterialUpload } from "./material-files";
import LiveTabs from "./live-tabs";
import styles from "./page.module.css";
import TermPicker from "./term-picker";
import Whiteboard from "./whiteboard";

// Название урока во вкладке потребовало бы второго запроса к api
// (generateMetadata не делит fetch страницы) — заголовок общий
export const metadata: Metadata = { title: "Урок" };

// Дата и время урока в шапке: «суббота, 24 октября, 21:00–22:00»
const headerTime: Intl.DateTimeFormatOptions = {
  weekday: "long",
  day: "numeric",
  month: "long",
  hour: "2-digit",
  minute: "2-digit",
};

// Начало запланированного урока: «29 октября в 21:00»
const startTime: Intl.DateTimeFormatOptions = { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" };

type Anchor = { id: string; label: string };

export default async function LessonPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;

  let me: User;
  let lesson: LessonDetail;
  try {
    [me, lesson] = await Promise.all([
      apiFetch<User>("/auth/me"),
      apiFetch<LessonDetail>(`/lessons/${encodeURIComponent(id)}`),
    ]);
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    if (e instanceof ApiError && (e.status === 404 || e.status === 400)) notFound();
    if (e instanceof ApiError && e.status === 403) {
      // Как 404: карточка без каркаса и путь назад
      return (
        <main className="auth-page">
          <div className="card card-pad">
            <EmptyState
              icon="lessons"
              title="Нет доступа"
              titleAs="h1"
              action={
                <Link href="/lessons" className={buttonClass({ variant: "primary" })}>
                  К урокам
                </Link>
              }
            >
              Этот урок ведёт другой преподаватель или вы не в его группе.
            </EmptyState>
          </div>
        </main>
      );
    }
    throw e;
  }

  const phase = lessonPhase(lesson.status, lesson.starts_at, lesson.ends_at);
  // Материалы и файлы добавляет и удаляет только преподаватель урока
  const isTeacher = me.id === lesson.teacher_id;
  const materials = lesson.materials.filter((m) => m.kind === "material");
  const homework = lesson.materials.filter((m) => m.kind === "homework");
  // Словарь урока (S3-8): преподаватель собирает его в чеклисте, когда урок
  // начался; остальным участникам — подтверждённые слова. Сбой запроса
  // (например, миграция 0019 не применена) — страница без словаря
  const lessonTerms = isTeacher
    ? []
    : await apiFetch<{ items: LessonTerm[] }>(`/lessons/${lesson.id}/terms`)
        .then((r) => r.items)
        .catch(() => []);
  const showPicker = isTeacher && phase !== "scheduled";
  // Гостевой ссылкой распоряжается организатор, и только у открытого
  // события: в урок с группой посторонних не зовут (ADR-010). После
  // ends_at api ссылку уже не пускает (410) — и выпускать её незачем
  const showGuestLink =
    lesson.group_id === null &&
    (me.role === "admin" || me.id === lesson.teacher_id) &&
    new Date(lesson.ends_at).getTime() > Date.now();

  // Раскраска слов по статусу — только у студента: ему и легенда
  const dictionary = <LessonDictionary items={lessonTerms} legend={me.role === "student"} />;

  const anchors: Anchor[] = [
    ...mediaAnchor(phase),
    ...(lessonTerms.length > 0 ? [{ id: "terms", label: `Словарь урока · ${lessonTerms.length}` }] : []),
    { id: "materials", label: `Материалы · ${materials.length}` },
    { id: "homework", label: `Домашка · ${homework.length}` },
    { id: "people", label: `Участники · ${lesson.participants.length}` },
    ...(showPicker ? [{ id: "picker", label: "Подбор слов" }] : []),
  ];

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className={cx("page page--wide", styles.page)}>
        <div className={styles.head}>
          <PageHeader
            crumb={{ href: "/lessons", label: "Уроки" }}
            title={lessonTitle(lesson)}
            pill={<PhasePill phase={phase} />}
            meta={
              <>
                <span>
                  <Icon name="lessons" size="sm" />
                  <LocalTime value={lesson.starts_at} end={lesson.ends_at} options={headerTime} />
                </span>
                <span>
                  <Icon name="user" size="sm" />
                  <span className="sr-only">Преподаватель:</span>
                  {lesson.teacher_name}
                </span>
              </>
            }
            actions={
              showGuestLink ? <GuestLink lessonId={lesson.id} hasLink={lesson.has_guest_link ?? false} /> : undefined
            }
          />
        </div>

        <nav className={cx("chip-row", styles.sectionNav)} aria-label="Разделы урока">
          {anchors.map((a) => (
            <a key={a.id} className="chip" href={`#${a.id}`}>
              {a.label}
            </a>
          ))}
        </nav>

        {phase === "scheduled" && (
          <div className={styles.phase}>
            <Alert tone="info" live={false}>
              Урок начнётся <LocalTime value={lesson.starts_at} options={startTime} />
            </Alert>
          </div>
        )}

        {phase === "live" && (
          <section id="live" className={cx("section", styles.media)} aria-label="Урок онлайн">
            {/* Доска — только на живом уроке (S3-6); словарь урока — ниже (S3-8) */}
            <LiveTabs
              room={<LessonRoom lessonId={lesson.id} />}
              board={<Whiteboard lessonId={lesson.id} userName={me.name} />}
            />
          </section>
        )}

        {phase === "processing" && (
          <section id="rec" className={cx("section", styles.phase)} aria-label="Запись урока">
            <Alert tone="warning" live={false}>
              Запись урока обрабатывается — плеер и транскрипт появятся здесь.
            </Alert>
            <div className={cx("skeleton skeleton--player", styles.pendingPlayer)} aria-hidden="true" />
          </section>
        )}

        {phase === "done" && (
          <section id="rec" className={cx("section", styles.media)} aria-label="Запись урока">
            {/* Запись и транскрипт по спикерам (S2-4). Правит реплики и
                сопоставляет спикеров с людьми урока только его преподаватель.
                Словарь урока — под плеером, рядом с транскриптом */}
            <LessonReplay
              lessonId={lesson.id}
              canEdit={isTeacher}
              people={[
                { user_id: lesson.teacher_id, name: lesson.teacher_name },
                ...lesson.participants.map((p) => ({ user_id: p.user_id, name: p.name })),
              ]}
              below={lessonTerms.length > 0 ? dictionary : undefined}
            />
          </section>
        )}

        {phase === "ended" && (
          <div className={styles.phase}>
            <Alert tone="neutral" live={false}>
              Урок завершён. Запись и транскрипт будут появляться здесь начиная с этапов 1–2.
            </Alert>
          </div>
        )}

        <div className={styles.grid}>
          <div className={styles.mainCol}>
            {phase !== "done" && dictionary}
            <MaterialsSection
              id="materials"
              title="Материалы"
              items={materials}
              isTeacher={isTeacher}
              upload={isTeacher ? <MaterialUpload lessonId={lesson.id} /> : null}
            />
            <MaterialsSection id="homework" title="Домашка" items={homework} isTeacher={isTeacher} />
          </div>
          <div className={styles.sideCol}>
            <Participants people={lesson.participants} />
          </div>
        </div>

        {showPicker && (
          <div className={styles.picker}>
            <TermPicker
              lessonId={lesson.id}
              students={lesson.participants
                .filter((p) => p.role === "student" && !p.archived)
                .map((p) => ({ user_id: p.user_id, name: p.name }))}
            />
          </div>
        )}
      </main>
    </>
  );
}

// Первый якорь — то, что у урока сейчас главное: класс или запись
function mediaAnchor(phase: LessonPhase): Anchor[] {
  if (phase === "live") return [{ id: "live", label: "Класс" }];
  if (phase === "done" || phase === "processing") return [{ id: "rec", label: "Запись" }];
  return [];
}

function MaterialsSection({
  id,
  title,
  items,
  isTeacher,
  upload,
}: {
  id: string;
  title: string;
  items: Material[];
  isTeacher: boolean;
  upload?: ReactNode;
}) {
  return (
    <section id={id} className="section" aria-labelledby={`${id}-title`}>
      <div className="section-title">
        <h2 id={`${id}-title`}>{title}</h2>
      </div>
      {upload}
      {items.length === 0 && <p className={cx("card muted", styles.empty)}>Пока пусто.</p>}
      {items.map((m) => (
        <article key={m.id} className={cx("card", styles.material)} aria-labelledby={`material-${m.id}`}>
          <div className={styles.materialHead}>
            <h3 id={`material-${m.id}`}>{m.title}</h3>
            {isTeacher && <DeleteMaterial materialId={m.id} title={m.title} />}
          </div>
          {/* body_md — текстом с переносами строк: разметка markdown пока
              не рендерится (долг D-17) */}
          {m.body_md && (
            <div className={cx("reading", styles.reading)}>
              <ClickableText text={m.body_md} />
            </div>
          )}
          {m.file && <FileDownload materialId={m.id} file={m.file} />}
        </article>
      ))}
    </section>
  );
}

function Participants({ people }: { people: LessonDetail["participants"] }) {
  return (
    <section id="people" className={cx("card section", styles.flush)} aria-labelledby="people-title">
      <div className="card-head">
        <h2 id="people-title">Участники</h2>
      </div>
      {people.length === 0 ? (
        <p className={cx("muted small", styles.peopleEmpty)}>Участников нет.</p>
      ) : (
        <ul className={styles.people}>
          {people.map((p) => (
            <li key={p.user_id} className={cx(styles.person, p.archived && "is-archived")}>
              <span className="avatar avatar--sm" aria-hidden="true">
                {initials(p.name)}
              </span>
              <span>
                {p.name}
                {p.archived && " · в архиве"}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
