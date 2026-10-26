import { notFound, redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { LessonDetail, Material, User } from "../../../lib/api";
import { lessonTitle } from "../../../lib/lesson-title";
import { lessonPhase } from "../../../lib/lesson-phase";
import type { LessonPhase } from "../../../lib/lesson-phase";
import UserBar from "../../user-bar";
import GuestLink from "./guest-link";
import LessonReplay from "./lesson-replay";
import LessonRoom from "./lesson-room";
import { DeleteMaterial, FileDownload, MaterialUpload } from "./material-files";
import LiveTabs from "./live-tabs";
import Whiteboard from "./whiteboard";

const dateFmt = new Intl.DateTimeFormat("ru-RU", {
  dateStyle: "full",
  timeStyle: "short",
  timeZone: "UTC",
});

const phaseTitles: Record<LessonPhase, string> = {
  scheduled: "Запланирован",
  live: "Идёт сейчас",
  ended: "Завершён",
  processing: "Запись обрабатывается",
  done: "Прошёл",
};

// Живой урок и запись с транскриптом шире колонки страницы (640px): доске,
// видео и транскрипту рядом с ним нужно место. Секция выходит за колонку
// симметрично — до 1200px или почти всего экрана.
const wideSectionStyle = {
  borderRadius: 8,
  padding: "1rem",
  boxSizing: "border-box",
  width: "min(96vw, 1200px)",
  margin: "1rem 0 1rem calc((100% - min(96vw, 1200px)) / 2)",
} as const;

const liveSectionStyle = { ...wideSectionStyle, border: "2px solid #2a7" } as const;
const replaySectionStyle = { ...wideSectionStyle, border: "1px solid #ddd" } as const;

// общий вид секций-заглушек будущих этапов
const placeholderSectionStyle = {
  border: "1px solid #ddd",
  borderRadius: 8,
  padding: "1rem",
  margin: "1rem 0",
} as const;

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
      return (
        <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "4rem auto", padding: "0 1rem" }}>
          <h1>Нет доступа</h1>
          <p>Этот урок ведёт другой преподаватель или вы не в его группе.</p>
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

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>
        {lessonTitle(lesson)} — {phaseTitles[phase]}
      </h1>
      <p>
        {dateFmt.format(new Date(lesson.starts_at))} (UTC) · преподаватель: {lesson.teacher_name}
      </p>
      {/* Гостевой ссылкой распоряжается организатор, и только у открытого
          события: в урок с группой посторонних не зовут (ADR-010) */}
      {lesson.group_id === null && (me.role === "admin" || me.id === lesson.teacher_id) && phase !== "done" && (
        <GuestLink lessonId={lesson.id} />
      )}

      {phase === "live" && (
        <section style={liveSectionStyle}>
          {/* Доска — только на живом уроке (S3-6); словарь урока приедет в S3-7 */}
          <LiveTabs
            room={<LessonRoom lessonId={lesson.id} />}
            board={<Whiteboard lessonId={lesson.id} userName={me.name} />}
          />
        </section>
      )}

      {phase === "processing" && (
        <section style={placeholderSectionStyle}>
          <p>Запись урока обрабатывается — плеер и транскрипт появятся здесь.</p>
        </section>
      )}

      {phase === "done" && (
        <section style={replaySectionStyle}>
          {/* Запись и транскрипт по спикерам (S2-4). Правит реплики только
              преподаватель урока */}
          <LessonReplay lessonId={lesson.id} canEdit={isTeacher} />
        </section>
      )}

      {phase === "ended" && (
        <section style={placeholderSectionStyle}>
          <p>Урок завершён. Запись и транскрипт будут появляться здесь начиная с этапов 1–2.</p>
        </section>
      )}

      {isTeacher && <MaterialUpload lessonId={lesson.id} />}
      <MaterialsSection title="Материалы" items={materials} isTeacher={isTeacher} />
      <MaterialsSection title="Домашка" items={homework} isTeacher={isTeacher} />

      <p style={{ color: "#666" }}>
        Участники: {lesson.participants.map((p) => p.name).join(", ") || "—"}
      </p>
    </main>
  );
}

function MaterialsSection({ title, items, isTeacher }: { title: string; items: Material[]; isTeacher: boolean }) {
  return (
    <section style={{ margin: "1.5rem 0" }}>
      <h2>{title}</h2>
      {items.length === 0 && <p style={{ color: "#666" }}>Пока пусто.</p>}
      {items.map((m) => (
        <article key={m.id} style={{ border: "1px solid #eee", borderRadius: 8, padding: "0.75rem 1rem", marginBottom: "0.75rem" }}>
          <div style={{ display: "flex", justifyContent: "space-between", gap: "0.5rem", alignItems: "baseline" }}>
            <h3 style={{ marginTop: 0 }}>{m.title}</h3>
            {isTeacher && <DeleteMaterial materialId={m.id} title={m.title} />}
          </div>
          {/* body_md рендерится как текст; настоящий markdown-рендер придёт
              вместе с <ClickableText> (S3-1) — единственным рендером текста */}
          {m.body_md && <pre style={{ whiteSpace: "pre-wrap", fontFamily: "inherit", margin: 0 }}>{m.body_md}</pre>}
          {m.file && <FileDownload materialId={m.id} file={m.file} />}
        </article>
      ))}
    </section>
  );
}
