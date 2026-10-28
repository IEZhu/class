import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { PracticeText, PracticeTextsPage, User } from "../../../lib/api";
import ClickableText, { ClickableTextScope, ExampleAuthors } from "../../clickable-text";
import { Alert } from "../../ui/alert";
import { buttonClass } from "../../ui/button";
import { cx } from "../../ui/cx";
import { EmptyState } from "../../ui/empty";
import { Icon } from "../../ui/icons";
import { PageHeader } from "../../ui/page-header";
import { Pill } from "../../ui/pill";
import UserBar from "../../user-bar";
import { GeneratedParagraphs, genreLabel } from "../text/generated-text";
import t from "../text/text.module.css";

export const metadata: Metadata = { title: "Тексты для практики" };

const dateFmt = new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeZone: "UTC" });

type Search = Promise<Record<string, string | string[] | undefined>>;

function first(v: string | string[] | undefined): string {
  return (Array.isArray(v) ? v[0] : v) ?? "";
}

// query — одни и те же words и cursor у ссылок страницы и у запроса к api
function query(words: string, cursor = ""): string {
  const qs = new URLSearchParams();
  if (words) qs.set("words", words);
  if (cursor) qs.set("cursor", cursor);
  const s = qs.toString();
  return s ? `?${s}` : "";
}

// S5-7: тексты для практики своих слов — свои сгенерированные тексты и
// предложения Tatoeba с переводом, где больше всего слов из словаря
// (или из поля поиска). Каждый текст — через <ClickableText>: клик
// открывает попап слова, у студента слова раскрашены по словарю.
export default async function PracticeTextsPage({ searchParams }: { searchParams: Search }) {
  const params = await searchParams;
  const words = first(params.words).trim();
  const cursor = first(params.cursor);

  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  if (me.role !== "student" && me.role !== "teacher") {
    return (
      <>
        <UserBar name={me.name} role={me.role} />
        <main className="page">
          <PageHeader title="Тексты для практики" />
          <Alert tone="info" live={false}>
            Подборка текстов доступна студентам и преподавателям.
          </Alert>
        </main>
      </>
    );
  }

  let page: PracticeTextsPage | null = null;
  let error: string | null = null;
  try {
    page = await apiFetch<PracticeTextsPage>(`/practice/texts/search${query(words, cursor)}`);
  } catch (e) {
    // 400 — слово не из букв или их больше 50: показываем причину у поля
    if (!(e instanceof ApiError && e.status === 400)) throw e;
    error = e.message;
  }
  const license = page?.items.find((it) => it.source === "tatoeba");

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page">
        <PageHeader
          title="Тексты для практики"
          actions={
            <Link href="/practice/text" className={buttonClass({ variant: "primary", blockOnPhone: true })}>
              <Icon name="spark" />
              Сгенерировать текст
            </Link>
          }
        >
          <p className="muted">
            Ваши сгенерированные тексты и предложения Tatoeba с переводом, где встречаются слова, которые вы учите;
            сверху — где их больше.
          </p>
        </PageHeader>
        <form method="get" action="/practice/texts" className={t.search}>
          <label className="label" htmlFor="words">
            Слова
          </label>
          <div className={t.searchRow}>
            <input
              id="words"
              className="input"
              name="words"
              defaultValue={words}
              aria-describedby={error ? "words-hint words-error" : "words-hint"}
              aria-invalid={error ? true : undefined}
            />
            <button type="submit" className={buttonClass({ variant: "secondary" })}>
              <Icon name="search" />
              Найти
            </button>
          </div>
          <p id="words-hint" className="hint">
            Через пробел или запятую; пусто — из моего словаря
          </p>
          {error && (
            <p id="words-error" className="field-error">
              <Icon name="error" size="sm" />
              <span>{error}</span>
            </p>
          )}
        </form>
        {page && (
          <ClickableTextScope studentId={me.role === "student" ? me.id : null}>
            <div className={t.results}>
              {page.words.length > 0 && (
                <div className={t.words}>
                  <span className="small muted">Слова поиска:</span>
                  <ul className={t.tags}>
                    {page.words.map((w) => (
                      <li key={w}>
                        <Pill>{w}</Pill>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {page.hint && (
                <div className="card">
                  {me.role === "student" ? (
                    <EmptyState icon="dict" title="В словаре нет слов, которые вы учите">
                      Добавьте их кликом по тексту урока или впишите слова в поле поиска.
                    </EmptyState>
                  ) : (
                    <EmptyState icon="search" title="Впишите слова в поле поиска" />
                  )}
                </div>
              )}
              {page.words.length > 0 && page.items.length === 0 && (
                <div className="card">
                  <EmptyState icon="search" title="Ничего не нашлось">
                    <Link href="/practice/text">Сгенерируйте текст</Link> с этими словами.
                  </EmptyState>
                </div>
              )}
              {page.items.length > 0 && (
                <div className={t.list}>
                  {page.items.map((item) => (
                    <PracticeItem key={`${item.source}-${item.id}`} item={item} />
                  ))}
                </div>
              )}
              {/* CC BY 2.0 FR требует подписи: авторы — у каждого предложения (ADR-028) */}
              {license?.source === "tatoeba" && (
                <p className="attr">
                  Предложения —{" "}
                  <a href="https://tatoeba.org" target="_blank" rel="noreferrer">
                    Tatoeba
                  </a>
                  ,{" "}
                  <a href={license.attribution.license_url} target="_blank" rel="noreferrer">
                    {license.attribution.license}
                  </a>
                </p>
              )}
            </div>
            {(cursor || page.next_cursor) && (
              <nav className="pager" aria-label="Страницы подборки">
                {cursor && (
                  <Link href={`/practice/texts${query(words)}`} className={buttonClass({ variant: "secondary" })}>
                    ← В начало
                  </Link>
                )}
                {page.next_cursor && (
                  <Link
                    href={`/practice/texts${query(words, page.next_cursor)}`}
                    className={buttonClass({ variant: "secondary", className: t.next })}
                  >
                    Дальше →
                  </Link>
                )}
              </nav>
            )}
          </ClickableTextScope>
        )}
      </main>
    </>
  );
}

function PracticeItem({ item }: { item: PracticeText }) {
  const matched = <p className="small muted">Ваши слова: {item.matched.join(", ")}</p>;
  if (item.source === "generated") {
    return (
      <article className={cx("card card-pad", t.item)}>
        <div className={t.itemHead}>
          <Pill tone="accent">Мой текст</Pill>
          <span className="small muted">
            {item.level} · {genreLabel(item.genre)} · {dateFmt.format(new Date(item.created_at))}
          </span>
        </div>
        <GeneratedParagraphs body={item.text} />
        {matched}
      </article>
    );
  }
  return (
    <article className={cx("card card-pad", t.item)}>
      <div>
        <p className={t.sentence}>
          <ClickableText text={item.text} />
        </p>
        <p className={t.translation}>{item.translation}</p>
        <ExampleAuthors example={item} />
      </div>
      {matched}
    </article>
  );
}
