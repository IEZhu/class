import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { PracticeVideo, PracticeVideoDetail, PracticeVideosPage, User, VideoTargets } from "../../../lib/api";
import { clock } from "../../../lib/timeline";
import ClickableText, { ClickableTextScope } from "../../clickable-text";
import UserBar from "../../user-bar";
import VideoPlayer from "./video-player";

// S5-5: видео-практика (ADR-038). Подбор роликов корпуса субтитров под
// слова — свои из словаря (новые и «забытые») или введённые в форме —
// и плеер выбранного ролика с панелью субтитров. Состояние — в адресе:
// ?words= — слова, ?v= — ролик, ?t= — с какого места (мс), ?cursor= —
// страница подбора.

type Search = Record<string, string | string[] | undefined>;

const percent = new Intl.NumberFormat("ru-RU", { style: "percent", maximumFractionDigits: 0 });
const perMinute = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 1 });

const weightingTexts: Record<VideoTargets["weighting"], string> = {
  words: "слова из формы",
  srs: "слова словаря, сначала те, что пора повторить",
  status: "слова словаря, сначала новые",
};

function param(sp: Search, name: string): string {
  const v = sp[name];
  return typeof v === "string" ? v : "";
}

function positiveInt(s: string): number | null {
  const n = Number(s);
  return /^\d+$/.test(s) && Number.isSafeInteger(n) ? n : null;
}

// href — адрес страницы с параметрами; пустые не пишутся
function href(params: Record<string, string | number | undefined>): string {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") qs.set(k, String(v));
  const s = qs.toString();
  return s ? `/practice/video?${s}` : "/practice/video";
}

// load — ответ api или текст ошибки для страницы: 400 — неверные слова
// или курсор, 404 — ролика нет или он без субтитров
async function load<T>(path: string): Promise<T | string> {
  try {
    return await apiFetch<T>(path);
  } catch (e) {
    if (e instanceof ApiError && (e.status === 400 || e.status === 404)) return e.message;
    throw e;
  }
}

export default async function PracticeVideoPage({ searchParams }: { searchParams: Promise<Search> }) {
  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  const allowed = me.role === "student" || me.role === "teacher";
  const sp = await searchParams;
  const words = param(sp, "words").trim();
  const videoId = positiveInt(param(sp, "v"));
  const startMs = positiveInt(param(sp, "t")) ?? 0;
  const cursor = param(sp, "cursor");
  const wordsQuery = words ? `words=${encodeURIComponent(words)}` : "";

  let detail: PracticeVideoDetail | string | null = null;
  let page: PracticeVideosPage | string | null = null;
  if (allowed) {
    const listQuery = [wordsQuery, cursor && `cursor=${encodeURIComponent(cursor)}`].filter(Boolean).join("&");
    [detail, page] = await Promise.all([
      videoId ? load<PracticeVideoDetail>(`/practice/videos/${videoId}${wordsQuery && `?${wordsQuery}`}`) : null,
      load<PracticeVideosPage>(`/practice/videos${listQuery && `?${listQuery}`}`),
    ]);
  }

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 820, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>Видео по словам</h1>
      {!allowed ? (
        <p>Видео-практика доступна студентам и преподавателям.</p>
      ) : (
        // Раскраска по словарю и «в мой словарь» — у студента (ADR-026)
        <ClickableTextScope dictionary={me.role === "student"}>
          <form method="get" action="/practice/video" style={{ display: "flex", gap: "0.5rem", flexWrap: "wrap" }}>
            <input
              name="words"
              defaultValue={words}
              placeholder="слова через запятую; пусто — из словаря"
              style={{ flex: "1 1 18rem" }}
            />
            <button type="submit">Подобрать</button>
          </form>
          {typeof detail === "string" && <p style={{ color: "crimson" }}>{detail}</p>}
          {detail && typeof detail !== "string" && (
            <div style={{ margin: "1rem 0" }}>
              <h2 style={{ margin: "0 0 0.5rem", fontSize: "1.1rem" }}>{detail.video.title}</h2>
              <VideoPlayer key={`${detail.video.id}:${startMs}`} video={detail.video} startMs={startMs} />
            </div>
          )}
          {typeof page === "string" ? (
            <p style={{ color: "crimson" }}>{page}</p>
          ) : (
            page && <Picks page={page} words={words} cursor={cursor} />
          )}
        </ClickableTextScope>
      )}
    </main>
  );
}

function Picks({ page, words, cursor }: { page: PracticeVideosPage; words: string; cursor: string }) {
  if (page.words.length === 0) {
    return (
      <p>
        В словаре нет невыученных слов — введите слова в форму или добавьте их в словарь кликом по слову в тексте.
      </p>
    );
  }
  return (
    <>
      <p style={{ color: "#666" }}>
        Цели ({weightingTexts[page.weighting] ?? page.weighting}): {page.words.map((w) => w.lemma).join(", ")}
      </p>
      {page.items.length === 0 && <p>В собранных субтитрах этих слов пока нет.</p>}
      <div style={{ display: "grid", gap: "0.75rem" }}>
        {page.items.map((v) => (
          <Pick key={v.id} video={v} words={words} cursor={cursor} />
        ))}
      </div>
      {page.next_cursor && (
        <p>
          <Link href={href({ words, cursor: page.next_cursor })}>Ещё видео</Link>
        </p>
      )}
    </>
  );
}

// Pick — ролик подбора: покрытие слов и лучшие места со ссылкой на плеер
// с этого момента; страница подбора остаётся той же.
function Pick({ video: v, words, cursor }: { video: PracticeVideo; words: string; cursor: string }) {
  return (
    <article style={{ border: "1px solid #ddd", borderRadius: 8, padding: "0.75rem 1rem" }}>
      <Link href={href({ words, cursor, v: v.id })} style={{ fontWeight: 600 }}>
        {v.title}
      </Link>
      <p style={{ color: "#666", margin: "0.25rem 0", fontSize: "0.9rem" }}>
        {v.channel && `${v.channel} · `}
        {clock(v.duration_ms)} · слова: {v.matched_words.join(", ")} ({percent.format(v.coverage)}) ·{" "}
        {perMinute.format(v.per_minute)} в минуту
      </p>
      {v.segments.map((s) => (
        <div key={s.idx} style={{ padding: "0.15rem 0" }}>
          <Link href={href({ words, cursor, v: v.id, t: s.start_ms })} title="Смотреть с этого места" style={{ color: "#888" }}>
            {clock(s.start_ms)}
          </Link>{" "}
          <ClickableText text={s.text} />
          <span style={{ color: "#b07d00", fontSize: "0.8rem" }}> · {s.words.join(", ")}</span>
        </div>
      ))}
    </article>
  );
}
