import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { User, UserTermPage } from "../../lib/api";
import { lessonTitle } from "../../lib/lesson-title";
import { STATUS_LABELS, TERM_STATUSES } from "../../lib/term-status";
import RoleRestricted from "../role-restricted";
import { buttonClass } from "../ui/button";
import { cx } from "../ui/cx";
import { EmptyState } from "../ui/empty";
import { PageHeader } from "../ui/page-header";
import UserBar from "../user-bar";
import DictionaryStatus, { DictionaryRow } from "./dictionary-status";
import styles from "./page.module.css";

export const metadata: Metadata = { title: "Мой словарь" };

const dateFmt = new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeZone: "UTC" });

// Фильтры по статусу (S3-5): «учу» — всё, кроме «знаю»
const filters = [
  { label: "Все", status: "" },
  { label: "Учу 1–4", status: "1,2,3,4" },
  ...TERM_STATUSES.map((s) => ({ label: `${s} ${STATUS_LABELS[s]}`, status: String(s) })),
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
// Вид (DESIGN §6.5): таблица от 768px, на телефоне строки — карточки.
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
      <RoleRestricted me={me} title="Словарь" icon="dict">
        Личный словарь есть только у студентов.
      </RoleRestricted>
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
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page">
        <PageHeader title="Мой словарь" />
        <div className="stack">
          <nav aria-label="Статус слов" className={styles.filterNav}>
            <ul className={cx("chip-row", styles.filters)}>
              {filters.map((f) => (
                <li key={f.label}>
                  {f.status === status ? (
                    <span className="chip" aria-current="true">
                      {f.label}
                    </span>
                  ) : (
                    <Link className="chip" href={dictionaryHref(f.status)}>
                      {f.label}
                    </Link>
                  )}
                </li>
              ))}
            </ul>
          </nav>
          {/* Как статус выглядит в текстах: фон и узор подчёркивания */}
          <p className="legend">
            <span>В текстах:</span>
            {TERM_STATUSES.slice(0, 4).map((s) => (
              <span key={s} className={`w w${s}`}>
                {s} {STATUS_LABELS[s]}
              </span>
            ))}
            <span>5 {STATUS_LABELS[5]} — без выделения</span>
          </p>
          {page.items.length === 0 ? (
            <div className="card">
              {status ? (
                <EmptyState icon="dict" title="С таким статусом слов нет." />
              ) : (
                <EmptyState icon="dict" title="Пока пусто">
                  Нажмите на слово в тексте урока и выберите «В мой словарь».
                </EmptyState>
              )}
            </div>
          ) : (
            <div className={cx("card", styles.card)}>
              <table className={cx("table", styles.table)}>
                <thead>
                  <tr>
                    <th scope="col">Слово</th>
                    <th scope="col">Перевод</th>
                    <th scope="col">Статус</th>
                    <th scope="col">Урок</th>
                    <th scope="col">
                      <span className="sr-only">Действия</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {page.items.map((it) => (
                    <DictionaryRow key={it.term_id} studentId={me.id} termId={it.term_id} lemma={it.lemma}>
                      <td className={styles.word}>
                        <strong lang="en">{it.lemma}</strong>
                        {it.surface.toLowerCase() !== it.lemma && (
                          <span lang="en" className="muted">
                            {it.surface}
                          </span>
                        )}
                      </td>
                      <td className={styles.tr}>{it.translation ?? <span className="muted">—</span>}</td>
                      <td className={styles.status}>
                        <DictionaryStatus studentId={me.id} termId={it.term_id} lemma={it.lemma} status={it.status} />
                      </td>
                      <td className={styles.lesson}>
                        {it.source_lesson ? (
                          <Link href={`/lesson/${it.source_lesson.id}`}>
                            {lessonTitle(it.source_lesson)}, {dateFmt.format(new Date(it.source_lesson.starts_at))}
                          </Link>
                        ) : (
                          <span className="muted">—</span>
                        )}
                        <span className={styles.added}>добавлено {dateFmt.format(new Date(it.added_at))}</span>
                      </td>
                    </DictionaryRow>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
        {(cursor || page.next_cursor) && (
          <nav className="pager" aria-label="Страницы словаря">
            {cursor && (
              <Link className={buttonClass({ variant: "secondary" })} href={dictionaryHref(status)}>
                ← К началу
              </Link>
            )}
            {page.next_cursor && (
              <Link className={buttonClass({ variant: "secondary", className: styles.next })} href={dictionaryHref(status, page.next_cursor)}>
                Дальше →
              </Link>
            )}
          </nav>
        )}
      </main>
    </>
  );
}
