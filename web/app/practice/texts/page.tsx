import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { PracticeText, PracticeTextsPage, User } from "../../../lib/api";
import ClickableText, { ClickableTextScope, ExampleAuthors } from "../../clickable-text";
import UserBar from "../../user-bar";
import { GeneratedParagraphs, genreLabel } from "../text/generated-text";

const pageStyle = { fontFamily: "system-ui, sans-serif", maxWidth: 720, margin: "2rem auto", padding: "0 1rem" } as const;
const cardStyle = { border: "1px solid #ddd", borderRadius: 8, padding: "0.75rem 1rem" } as const;
const mutedStyle = { color: "#666" } as const;
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
      <main style={pageStyle}>
        <UserBar name={me.name} role={me.role} />
        <h1>Тексты для практики</h1>
        <p>Подборка текстов доступна студентам и преподавателям.</p>
      </main>
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
    <main style={pageStyle}>
      <UserBar name={me.name} role={me.role} />
      <h1>Тексты для практики</h1>
      <p style={mutedStyle}>
        Ваши сгенерированные тексты и предложения Tatoeba с переводом, где встречаются слова, которые вы учите;
        сверху — где их больше.
      </p>
      <form method="get" action="/practice/texts" style={{ display: "flex", gap: "0.5rem", flexWrap: "wrap" }}>
        <input
          name="words"
          defaultValue={words}
          placeholder="слова через пробел или запятую; пусто — из моего словаря"
          style={{ flex: 1, minWidth: "16rem", padding: "0.5rem" }}
        />
        <button type="submit">Найти</button>
      </form>
      <p>
        <Link href="/practice/text">Сгенерировать новый текст по словам →</Link>
      </p>
      {error && <p style={{ color: "#c92a2a" }}>{error}</p>}
      {page && (
        <ClickableTextScope dictionary={me.role === "student"}>
          {page.words.length > 0 && <p style={mutedStyle}>Слова поиска: {page.words.join(", ")}</p>}
          {page.hint && (
            <p>
              {me.role === "student"
                ? "В словаре нет слов, которые вы учите: добавьте их кликом по тексту урока или впишите слова в поле поиска."
                : "Впишите слова в поле поиска."}
            </p>
          )}
          {page.words.length > 0 && page.items.length === 0 && (
            <p>
              Ничего не нашлось — <Link href="/practice/text">сгенерируйте текст</Link> с этими словами.
            </p>
          )}
          <div style={{ display: "grid", gap: "0.75rem" }}>
            {page.items.map((item) => (
              <PracticeItem key={`${item.source}-${item.id}`} item={item} />
            ))}
          </div>
          {/* CC BY 2.0 FR требует подписи: авторы — у каждого предложения (ADR-028) */}
          {license?.source === "tatoeba" && (
            <p style={{ ...mutedStyle, fontSize: "0.85em" }}>
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
          <nav style={{ display: "flex", gap: "1rem", margin: "1rem 0" }}>
            {cursor && <Link href={`/practice/texts${query(words)}`}>← В начало</Link>}
            {page.next_cursor && <Link href={`/practice/texts${query(words, page.next_cursor)}`}>Дальше →</Link>}
          </nav>
        </ClickableTextScope>
      )}
    </main>
  );
}

function PracticeItem({ item }: { item: PracticeText }) {
  const matched = (
    <p style={{ ...mutedStyle, fontSize: "0.9rem", margin: 0 }}>Ваши слова: {item.matched.join(", ")}</p>
  );
  if (item.source === "generated") {
    return (
      <article style={cardStyle}>
        <p style={{ ...mutedStyle, margin: 0 }}>
          Мой текст · {item.level} · {genreLabel(item.genre)} · {dateFmt.format(new Date(item.created_at))}
        </p>
        <GeneratedParagraphs body={item.text} />
        {matched}
      </article>
    );
  }
  return (
    <article style={cardStyle}>
      <p style={{ margin: 0, lineHeight: 1.6 }}>
        <ClickableText text={item.text} />
      </p>
      <p style={{ ...mutedStyle, margin: "0.25rem 0" }}>{item.translation}</p>
      <ExampleAuthors example={item} />
      {matched}
    </article>
  );
}
