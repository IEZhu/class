import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { Lesson, User } from "../../lib/api";
import UserBar from "../user-bar";

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

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>Уроки</h1>
      {lessons.length === 0 && <p>Пока нет уроков.</p>}
      <ul style={{ listStyle: "none", padding: 0, display: "grid", gap: "0.75rem" }}>
        {lessons.map((l) => (
          <li key={l.id} style={{ border: "1px solid #ddd", borderRadius: 8, padding: "0.75rem 1rem" }}>
            <Link href={`/lesson/${l.id}`} style={{ textDecoration: "none", color: "inherit" }}>
              <strong>{l.group_name}</strong> · {dateFmt.format(new Date(l.starts_at))} (UTC) ·{" "}
              <span>{l.status}</span>
            </Link>
          </li>
        ))}
      </ul>
    </main>
  );
}
