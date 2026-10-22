import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { Group, Lesson, User } from "../../lib/api";
import { lessonTitle } from "../../lib/lesson-title";
import UserBar from "../user-bar";
import NewEvent from "./new-event";

const dateFmt = new Intl.DateTimeFormat("ru-RU", {
  dateStyle: "short",
  timeStyle: "short",
  timeZone: "UTC",
});

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

  // Группы нужны только форме создания — студенту их не показываем
  // и не запрашиваем (ему api ответит 403).
  let groups: Group[] = [];
  if (me.role !== "student") {
    groups = await apiFetch<Group[]>("/groups").catch(() => []);
  }

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>Уроки</h1>
      {me.role === "teacher" && <NewEvent groups={groups} />}
      {lessons.length === 0 && <p>Пока нет уроков.</p>}
      <ul style={{ listStyle: "none", padding: 0, display: "grid", gap: "0.75rem" }}>
        {lessons.map((l) => (
          <li key={l.id} style={{ border: "1px solid #ddd", borderRadius: 8, padding: "0.75rem 1rem" }}>
            <Link href={`/lesson/${l.id}`} style={{ textDecoration: "none", color: "inherit" }}>
              <strong>{lessonTitle(l)}</strong> ·{" "}
              {dateFmt.format(new Date(l.starts_at))} (UTC) ·{" "}
              <span>{l.status}</span>
            </Link>
          </li>
        ))}
      </ul>
    </main>
  );
}
