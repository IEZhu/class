import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { User, UserTermPage } from "../../lib/api";
import { lessonTitle } from "../../lib/lesson-title";
import { STATUS_LABELS, TERM_STATUSES } from "../../lib/term-status";
import UserBar from "../user-bar";
import DictionaryStatus from "./dictionary-status";

export const metadata: Metadata = { title: "Мой словарь" };

const dateFmt = new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeZone: "UTC" });

const pageStyle = { fontFamily: "system-ui, sans-serif", maxWidth: 760, margin: "2rem auto", padding: "0 1rem" } as const;
const cellStyle = { padding: "0.4rem 0.5rem", borderBottom: "1px solid #eee", verticalAlign: "top" } as const;

// Фильтры по статусу (S3-5): «учу» — всё, кроме «знаю»
const filters = [
  { label: "Все", status: "" },
  { label: "Учу (1–4)", status: "1,2,3,4" },
  ...TERM_STATUSES.map((s) => ({ label: `${s} · ${STATUS_LABELS[s]}`, status: String(s) })),
];

type Search = Promise<Record<string, string | string[] | undefined>>;

function first(v: string | string[] | undefined): string {
  return (Array.isArray(v) ? v[0] : v) ?? "";
}

// query — одни и те же status и cursor у ссылок страницы и у запроса к api
function query(status: string, cursor = ""): string {
  const qs = new URLSearchParams();
  if (status) qs.set("status", status);
  if (cursor) qs.set("cursor", cursor);
  const s = qs.toString();
  return s ? `?${s}` : "";
}

function dictionaryHref(status: string, cursor = ""): string {
  return `/dictionary${query(status, cursor)}`;
}

// Личный словарь студента (S3-5): слова со статусами, переводом из кэша
// и уроком, на котором слово добавлено; новые сверху, по страницам.
export default async function DictionaryPage({ searchParams }: { searchParams: Search }) {
  const params = await searchParams;
  const status = first(params.status);
  const cursor = first(params.cursor);

  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  if (me.role !== "student") {
    return (
      <main style={pageStyle}>
        <UserBar name={me.name} role={me.role} />
        <h1>Словарь</h1>
        <p>Личный словарь есть только у студентов.</p>
      </main>
    );
  }

  let page: UserTermPage;
  try {
    page = await apiFetch<UserTermPage>(`/user-terms${query(status, cursor)}`);
  } catch (e) {
    // Ручная правка адреса: api не принял фильтр или курсор
    if (e instanceof ApiError && e.status === 400) redirect("/dictionary");
    throw e;
  }

  return (
    <main style={pageStyle}>
      <UserBar name={me.name} role={me.role} />
      <h1>Мой словарь</h1>
      <nav style={{ display: "flex", gap: "0.75rem", flexWrap: "wrap", marginBottom: "1rem" }}>
        {filters.map((f) =>
          f.status === status ? (
            <strong key={f.label}>{f.label}</strong>
          ) : (
            <Link key={f.label} href={dictionaryHref(f.status)}>
              {f.label}
            </Link>
          ),
        )}
      </nav>
      {page.items.length === 0 ? (
        <p style={{ color: "#666" }}>
          {status ? "С таким статусом слов нет." : "Пока пусто: нажмите на слово в тексте урока и выберите «в мой словарь»."}
        </p>
      ) : (
        <table style={{ width: "100%", borderCollapse: "collapse" }}>
          <thead>
            <tr style={{ textAlign: "left" }}>
              <th style={cellStyle}>Слово</th>
              <th style={cellStyle}>Перевод</th>
              <th style={cellStyle}>Статус</th>
              <th style={cellStyle}>Урок</th>
            </tr>
          </thead>
          <tbody>
            {page.items.map((it) => (
              <tr key={it.term_id}>
                <td style={cellStyle}>
                  <strong>{it.lemma}</strong>
                  {it.surface.toLowerCase() !== it.lemma && <div style={{ color: "#666" }}>{it.surface}</div>}
                </td>
                <td style={cellStyle}>{it.translation ?? "—"}</td>
                <td style={cellStyle}>
                  <DictionaryStatus studentId={me.id} termId={it.term_id} lemma={it.lemma} status={it.status} />
                </td>
                <td style={cellStyle}>
                  {it.source_lesson ? (
                    <Link href={`/lesson/${it.source_lesson.id}`}>
                      {lessonTitle(it.source_lesson)}, {dateFmt.format(new Date(it.source_lesson.starts_at))}
                    </Link>
                  ) : (
                    "—"
                  )}
                  <div style={{ color: "#666", fontSize: "0.85em" }}>добавлено {dateFmt.format(new Date(it.added_at))}</div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p style={{ display: "flex", gap: "1rem" }}>
        {cursor && <Link href={dictionaryHref(status)}>← к началу</Link>}
        {page.next_cursor && <Link href={dictionaryHref(status, page.next_cursor)}>дальше →</Link>}
      </p>
    </main>
  );
}
