import { notFound, redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { LessonDetail, Material, User } from "../../../lib/api";
import { lessonPhase } from "../../../lib/lesson-phase";
import type { LessonPhase } from "../../../lib/lesson-phase";
import UserBar from "../../user-bar";
import LessonPlayer from "./lesson-player";
import LessonRoom from "./lesson-room";

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

// общий вид секций-заглушек будущих этапов (S1-5/S1-6/S2-4)
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
  const materials = lesson.materials.filter((m) => m.kind === "material");
  const homework = lesson.materials.filter((m) => m.kind === "homework");

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>
        {lesson.group_name} — {phaseTitles[phase]}
      </h1>
      <p>
        {dateFmt.format(new Date(lesson.starts_at))} (UTC) · преподаватель: {lesson.teacher_name}
      </p>

      {phase === "live" && (
        <section style={{ border: "2px solid #2a7", borderRadius: 8, padding: "1rem", margin: "1rem 0" }}>
          {/* Доска и словарь урока приедут на этапе 3 */}
          <LessonRoom lessonId={lesson.id} />
        </section>
      )}

      {phase === "processing" && (
        <section style={placeholderSectionStyle}>
          <p>Запись урока обрабатывается — плеер и транскрипт появятся здесь.</p>
        </section>
      )}

      {phase === "done" && (
        <>
          <section style={placeholderSectionStyle}>
            <LessonPlayer lessonId={lesson.id} />
          </section>
          <section style={placeholderSectionStyle}>
            {/* Транскрипт по спикерам оживёт в S2-4 */}
            <p>📝 Транскрипт по спикерам появится на этапе 2 (S2-4).</p>
          </section>
        </>
      )}

      {phase === "ended" && (
        <section style={placeholderSectionStyle}>
          <p>Урок завершён. Запись и транскрипт будут появляться здесь начиная с этапов 1–2.</p>
        </section>
      )}

      <MaterialsSection title="Материалы" items={materials} />
      <MaterialsSection title="Домашка" items={homework} />

      <p style={{ color: "#666" }}>
        Участники: {lesson.participants.map((p) => p.name).join(", ") || "—"}
      </p>
    </main>
  );
}

function MaterialsSection({ title, items }: { title: string; items: Material[] }) {
  return (
    <section style={{ margin: "1.5rem 0" }}>
      <h2>{title}</h2>
      {items.length === 0 && <p style={{ color: "#666" }}>Пока пусто.</p>}
      {items.map((m) => (
        <article key={m.id} style={{ border: "1px solid #eee", borderRadius: 8, padding: "0.75rem 1rem", marginBottom: "0.75rem" }}>
          <h3 style={{ marginTop: 0 }}>{m.title}</h3>
          {/* body_md рендерится как текст; настоящий markdown-рендер придёт
              вместе с <ClickableText> (S3-1) — единственным рендером текста */}
          <pre style={{ whiteSpace: "pre-wrap", fontFamily: "inherit", margin: 0 }}>{m.body_md}</pre>
        </article>
      ))}
    </section>
  );
}
