import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { PracticeVideo, PracticeVideoDetail, PracticeVideosPage, User, VideoTargets } from "../../../lib/api";
import { clock } from "../../../lib/timeline";
import ClickableText, { ClickableTextScope } from "../../clickable-text";
import { Alert } from "../../ui/alert";
import { buttonClass } from "../../ui/button";
import { cx } from "../../ui/cx";
import { EmptyState } from "../../ui/empty";
import { Icon } from "../../ui/icons";
import { PageHeader } from "../../ui/page-header";
import { Pill } from "../../ui/pill";
import UserBar from "../../user-bar";
import VideoPlayer, { TargetTags } from "./video-player";
import v from "./video.module.css";

export const metadata: Metadata = { title: "Видео по словам" };

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

  // 400 списка — неверные слова или курсор: причина — у поля слов
  const listError = typeof page === "string" ? page : null;
  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page">
        <PageHeader title="Видео по словам" />
        {!allowed ? (
          <Alert tone="info" live={false}>
            Видео-практика доступна студентам и преподавателям.
          </Alert>
        ) : (
          // Раскраска по словарю и «в мой словарь» — у студента (ADR-026)
          <ClickableTextScope studentId={me.role === "student" ? me.id : null}>
            <form method="get" action="/practice/video" className={v.search}>
              <label className="label" htmlFor="words">
                Слова
              </label>
              <div className={v.searchRow}>
                <input
                  id="words"
                  className="input"
                  name="words"
                  defaultValue={words}
                  aria-describedby={listError ? "words-hint words-error" : "words-hint"}
                  aria-invalid={listError ? true : undefined}
                />
                <button type="submit" className={buttonClass({ variant: "primary" })}>
                  Подобрать
                </button>
              </div>
              <p id="words-hint" className="hint">
                Через запятую; пусто — из словаря
              </p>
              {listError && (
                <p id="words-error" className="field-error">
                  <Icon name="error" size="sm" />
                  <span>{listError}</span>
                </p>
              )}
            </form>
            <div className={v.content}>
              {typeof detail === "string" && (
                <Alert tone="danger" className={v.alert}>
                  {detail}
                </Alert>
              )}
              {detail && typeof detail !== "string" && (
                <section className={cx("card card-pad", v.playerCard)} aria-labelledby="video-title">
                  <h2 id="video-title" className={v.playerTitle}>
                    {detail.video.title}
                  </h2>
                  <VideoPlayer key={`${detail.video.id}:${startMs}`} video={detail.video} startMs={startMs} />
                </section>
              )}
              {page && typeof page !== "string" && <Picks page={page} words={words} cursor={cursor} />}
            </div>
          </ClickableTextScope>
        )}
      </main>
    </>
  );
}

function Picks({ page, words, cursor }: { page: PracticeVideosPage; words: string; cursor: string }) {
  if (page.words.length === 0) {
    return (
      <div className="card">
        <EmptyState icon="dict" title="В словаре нет невыученных слов">
          Введите слова в форму или добавьте их в словарь кликом по слову в тексте.
        </EmptyState>
      </div>
    );
  }
  return (
    <>
      <div className={v.targets}>
        <span className="small muted">Цели ({weightingTexts[page.weighting] ?? page.weighting}):</span>
        <ul className={v.targetList}>
          {page.words.map((w) => (
            <li key={w.lemma}>
              <Pill tone="accent">{w.lemma}</Pill>
            </li>
          ))}
        </ul>
      </div>
      {page.items.length === 0 ? (
        <div className="card">
          <EmptyState icon="video" title="В собранных субтитрах этих слов пока нет" />
        </div>
      ) : (
        <div className={v.picks}>
          {page.items.map((item) => (
            <Pick key={item.id} video={item} words={words} cursor={cursor} />
          ))}
        </div>
      )}
      {page.next_cursor && (
        <nav className="pager" aria-label="Страницы подбора">
          <Link href={href({ words, cursor: page.next_cursor })} className={buttonClass({ variant: "secondary" })}>
            Ещё видео
          </Link>
        </nav>
      )}
    </>
  );
}

// Pick — ролик подбора: покрытие слов и лучшие места со ссылкой на плеер
// с этого момента; страница подбора остаётся той же.
function Pick({ video: p, words, cursor }: { video: PracticeVideo; words: string; cursor: string }) {
  return (
    <article className={cx("card card-pad", v.pick)}>
      <h2 className={v.pickTitle}>
        <Link href={href({ words, cursor, v: p.id })}>{p.title}</Link>
      </h2>
      <p className="small muted">
        {p.channel && `${p.channel} · `}
        {clock(p.duration_ms)} · слова: {p.matched_words.join(", ")} ({percent.format(p.coverage)}) ·{" "}
        {perMinute.format(p.per_minute)} в минуту
      </p>
      <ul className={v.segments}>
        {p.segments.map((s) => (
          <li key={s.idx} className={v.segment}>
            <Link href={href({ words, cursor, v: p.id, t: s.start_ms })} title="Смотреть с этого места" className="ts">
              {clock(s.start_ms)}
            </Link>
            <div>
              <ClickableText text={s.text} />
              {s.words.length > 0 && <TargetTags words={s.words} />}
            </div>
          </li>
        ))}
      </ul>
    </article>
  );
}
