"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import type { CSSProperties, FormEvent } from "react";

import type { ClozeAttemptResult, ClozeItem, ClozeOptions, ClozePage } from "../../../lib/api";
import {
  clozeCredit,
  clozeKeyAction,
  distractorRequestIds,
  itemOptions,
  llmErrorNote,
  optionState,
  responseMs,
  restartClock,
  scheduleText,
  sourceLabel,
  verdictParts,
} from "../../../lib/cloze-session";
import type { ClozeClock, ClozeCredit, ClozeMode, ClozeVerdict } from "../../../lib/cloze-session";
import { isSessionKey, SESSION_PROPS } from "../../../lib/practice-keys";
import { reviewId } from "../../../lib/review-session";
import ClickableText from "../../clickable-text";
import { RequestError, requestJson } from "../../forms";
import { termStatuses } from "../../term-status-control";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import { cx } from "../../ui/cx";
import { EmptyState } from "../../ui/empty";
import { Icon } from "../../ui/icons";
import { Kbd } from "../../ui/kbd";
import { Pill } from "../../ui/pill";
import s from "../practice.module.css";
import { useSessionFocus } from "../session-focus";

function newAttemptId(): string {
  return reviewId(crypto.getRandomValues(new Uint8Array(16)));
}

type Options = ClozeOptions["items"][number];

const modes: { mode: ClozeMode; label: string }[] = [
  { mode: "choice", label: "Варианты" },
  { mode: "typing", label: "Ввод слова" },
];

// Надпись клавиши Enter — английское слово на клавише (WCAG 3.1.2)
const enterKey = <span lang="en">Enter</span>;

// ClozeSession — проход выдачи: предложение с пропуском, ответ кнопкой
// (клавиши 1–4) или вводом, итог и следующий пункт по Enter. attempt_id
// заводится при первой попытке ответа и живёт до следующего пункта:
// повтор после сбоя сети api засчитает один раз. С ключом LLM
// (llm_available) у пунктов после первого вариантов в выдаче нет
// (pending): страница сразу просит их одним POST
// /practice/cloze/distractors, и у каждого пункта набор вариантов один
// (ADR-037). Пока варианты не пришли, ответить можно вводом. studentId —
// чей словарь перекрашивает ответ (кэш статусов по студенту, ADR-026).
export default function ClozeSession({ page, studentId }: { page: ClozePage; studentId: number }) {
  const router = useRouter();
  const [index, setIndex] = useState(0);
  const [mode, setMode] = useState<ClozeMode>("choice");
  const [typed, setTyped] = useState("");
  const [chosen, setChosen] = useState<string | null>(null);
  const [result, setResult] = useState<ClozeAttemptResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tally, setTally] = useState({ correct: 0, wrong: 0 });
  const [fetched, setFetched] = useState<Record<number, Options>>({});
  const [fetchFailed, setFetchFailed] = useState(false);
  const [llmNote, setLlmNote] = useState<string | null>(null);
  const attemptRef = useRef<string | null>(null);
  const shownAt = useRef(0);
  const clock = useRef<ClozeClock | null>(null);
  const sessionRef = useRef<HTMLElement>(null);
  const item: ClozeItem | undefined = page.items[index];
  const total = page.items.length;
  // null — варианты пункта ещё не пришли (pending)
  const options = item ? itemOptions(item, fetched) : [];
  const ready = options !== null;

  // Время ответа — от показа пункта, а у pending, пока ждали вариантов, —
  // от прихода кнопок или от перехода к вводу слова (restartClock)
  useEffect(() => {
    const cur = { index, mode, ready };
    if (restartClock(clock.current, cur)) shownAt.current = performance.now();
    clock.current = cur;
  }, [index, mode, ready]);

  // fetchOptions — окончательные варианты pending-пунктов одним запросом.
  // Повтор безопасен: api отдаёт уже закреплённый набор
  function fetchOptions(ids: number[], isCancelled: () => boolean) {
    setFetchFailed(false);
    requestJson<ClozeOptions>("/api/practice/cloze/distractors", "POST", { item_ids: ids })
      .then((res) => {
        if (isCancelled()) return;
        setFetched((prev) => ({ ...prev, ...Object.fromEntries(res.items.map((it) => [it.id, it])) }));
        setLlmNote(llmErrorNote(res.llm_error));
      })
      .catch((e: unknown) => {
        if (isCancelled()) return;
        setFetchFailed(true);
        setLlmNote(`варианты не загрузились: ${e instanceof Error ? e.message : "сбой"}`);
      });
  }

  useEffect(() => {
    const ids = distractorRequestIds(page);
    if (ids.length === 0) return;
    let cancelled = false;
    fetchOptions(ids, () => cancelled);
    return () => {
      cancelled = true;
    };
  }, [page]);

  function retryOptions() {
    const ids = distractorRequestIds(page).filter((id) => !fetched[id]);
    if (ids.length > 0) fetchOptions(ids, () => false);
  }

  function next(note: string | null) {
    attemptRef.current = null;
    setIndex((i) => i + 1);
    setResult(null);
    setChosen(null);
    setTyped("");
    setError(note);
  }

  async function answer(value: string, how: ClozeMode) {
    if (!item || busy || result || !value.trim()) return;
    attemptRef.current ??= newAttemptId();
    const ms = responseMs(shownAt.current, performance.now());
    setBusy(true);
    setError(null);
    if (how === "choice") setChosen(value);
    try {
      const res = await requestJson<ClozeAttemptResult>(`/api/practice/cloze/${item.id}/attempts`, "POST", {
        attempt_id: attemptRef.current,
        answer: value.trim(),
        mode: how,
        response_ms: ms,
      });
      termStatuses.setStatus(studentId, res.lemma, res.status);
      setResult(res);
      setTally((t) => (res.correct ? { ...t, correct: t.correct + 1 } : { ...t, wrong: t.wrong + 1 }));
    } catch (e) {
      // 404 — слово убрали из словаря, 409 — ключ занят: дальше, а не
      // повтор; прочие сбои — повтор с тем же ключом
      if (e instanceof RequestError && (e.status === 404 || e.status === 409)) {
        next(`пункт пропущен: ${e.message}`);
      } else {
        setChosen(null);
        setError(e instanceof Error ? e.message : "не удалось сохранить ответ");
      }
    } finally {
      setBusy(false);
    }
  }

  // Клавиши — только с фокусом в сессии (WCAG 2.1.4). Поля ввода, попап
  // и слова текста не перехватываются; Enter и пробел на кнопке или
  // ссылке — их собственное нажатие (lib/practice-keys). Обработчик
  // переподписывается на каждый рендер: видит текущий пункт
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!item || e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;
      if (!isSessionKey(e.target instanceof Element ? e.target : null, e.key)) return;
      const action = clozeKeyAction(e.key, result !== null, mode, options?.length ?? 0);
      if (!action) return;
      e.preventDefault();
      if (action.kind === "next") next(null);
      else if (options) void answer(options[action.index], "choice");
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  // Фокус — в сессии с начала, после ответа, «Дальше» и после запроса
  // ответа: нажатый мышью вариант на время запроса disabled, фокус с него
  // падает на <body>, и после сбоя клавиши иначе молчали бы
  useSessionFocus(sessionRef, `${index}:${result !== null}:${busy}`);

  function submitTyped(e: FormEvent) {
    e.preventDefault();
    void answer(typed, "typing");
  }

  const done = Math.min(index, total);
  if (total === 0) {
    return (
      <div className="card">
        <EmptyState icon="cloze" title="Пока не из чего составить упражнение">
          Нужны слова в <Link href="/dictionary">словаре</Link> и предложения с ними: из уроков, ваших текстов или корпуса
          Tatoeba.
        </EmptyState>
      </div>
    );
  }
  const progress = { "--progress": `${(100 * done) / total}%` } as CSSProperties;
  return (
    <section ref={sessionRef} tabIndex={-1} {...SESSION_PROPS} aria-label="Вставь слово" className={s.session}>
      <div className={s.bar}>
        <div className={s.barRow}>
          <span>
            <span className={s.count}>
              {done} из {total}
            </span>
            {page.due_total > 0 && <span className="small muted"> · к повторению {page.due_total}</span>}
          </span>
          <span className={s.score}>
            <Pill tone="success">верно {tally.correct}</Pill>
            <Pill tone="danger">неверно {tally.wrong}</Pill>
          </span>
        </div>
        <div
          className="progress"
          role="progressbar"
          aria-label="Пройдено предложений"
          aria-valuemin={0}
          aria-valuemax={total}
          aria-valuenow={done}
          style={progress}
        >
          <span />
        </div>
      </div>
      <div className={cx("segmented", s.modes)} role="group" aria-label="Как отвечать">
        {modes.map((m) => (
          <button key={m.mode} type="button" aria-pressed={mode === m.mode} disabled={busy} onClick={() => setMode(m.mode)}>
            {m.label}
          </button>
        ))}
      </div>
      {error && (
        <Alert tone="danger" className={s.alert}>
          {error}
        </Alert>
      )}
      {item ? (
        <article className={cx("card", s.practiceCard)} key={item.id}>
          <ItemSource item={item} credit={clozeCredit(item.attribution, result?.attribution ?? null)} />
          <p className={s.sentence}>
            {result ? (
              <ClickableText text={result.sentence} />
            ) : (
              <>
                <ClickableText text={item.before} />
                <span className={s.blank} role="img" aria-label="пропуск" />
                <ClickableText text={item.after} />
              </>
            )}
          </p>
          {mode === "choice" && options === null ? (
            fetchFailed ? (
              <Alert
                tone="warning"
                action={
                  <Button type="button" variant="secondary" size="sm" className={s.touch} onClick={retryOptions}>
                    Повторить
                  </Button>
                }
              >
                Варианты не загрузились. Или впишите слово (режим «Ввод слова»).
              </Alert>
            ) : (
              <div className={s.pending}>
                <p className="small muted" role="status">
                  Подбираем варианты…
                </p>
                <div className={s.options} aria-hidden="true">
                  {[0, 1, 2, 3].map((i) => (
                    <span key={i} className={cx("skeleton", s.optionSkeleton)} />
                  ))}
                </div>
              </div>
            )
          ) : mode === "choice" && options !== null ? (
            <div className={s.options} role="group" aria-label="Варианты ответа">
              {options.map((o, i) => {
                const state = optionState(o, chosen, result);
                return (
                  <button
                    key={o}
                    type="button"
                    className={cx(s.option, state && s[state])}
                    lang="en"
                    disabled={busy || result !== null}
                    onClick={() => void answer(o, "choice")}
                  >
                    <span className={s.optionLabel}>
                      {state === "correct" && <Icon name="check" />}
                      {state === "wrong" && <Icon name="x" />}
                      {o}
                    </span>
                    <Kbd>{i + 1}</Kbd>
                  </button>
                );
              })}
            </div>
          ) : (
            <form onSubmit={submitTyped} className={s.typing}>
              <input
                key={item.id}
                className="input input--lg"
                autoFocus
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                disabled={busy || result !== null}
                maxLength={64}
                aria-label="Пропущенное слово"
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
              />
              <Button type="submit" variant="primary" size="lg" blockOnPhone disabled={busy || result !== null || !typed.trim()}>
                Проверить
              </Button>
            </form>
          )}
          {/* Итог для чтения с экрана: регион есть с показа пункта, поэтому
              появившийся ответ объявляется */}
          <p className="sr-only" role="status">
            {result && (
              <>
                <Verdict result={result} /> — {scheduleText(result)}
              </>
            )}
          </p>
          {result && (
            <Feedback
              result={result}
              credit={clozeCredit(item.attribution, result.attribution)}
              onNext={() => next(null)}
            />
          )}
        </article>
      ) : (
        <div className="card">
          <EmptyState
            icon="check"
            title="Выдача пройдена"
            action={
              <Button type="button" variant="primary" blockOnPhone onClick={() => router.refresh()}>
                Ещё предложения
              </Button>
            }
          >
            Верно {tally.correct} из {tally.correct + tally.wrong}.
          </EmptyState>
        </div>
      )}
      {llmNote && <p className={cx("xs muted", s.note)}>{llmNote}</p>}
      {item && (
        <p className={s.shortcuts}>
          {mode === "choice" ? (
            <span>
              <Kbd>1</Kbd>–<Kbd>4</Kbd> выбрать вариант
            </span>
          ) : (
            <span>
              <Kbd>{enterKey}</Kbd> проверить
            </span>
          )}
          <span>
            <Kbd>{enterKey}</Kbd> дальше
          </span>
        </p>
      )}
    </section>
  );
}

// ItemSource — откуда предложение; у Tatoeba — подпись автора и лицензия
// (CC BY), ссылка на страницу предложения — только после ответа
function ItemSource({ item, credit: c }: { item: ClozeItem; credit: ClozeCredit | null }) {
  const author = c ? c.author || "автор не указан" : "";
  return (
    <p className={s.source}>
      <Icon name={item.lesson_id !== null ? "lessons" : "text"} size="sm" />
      <span>
        {item.lesson_id !== null ? <Link href={`/lesson/${item.lesson_id}`}>{sourceLabel(item.source)}</Link> : sourceLabel(item.source)}
        {c && (
          <>
            {" · "}
            {c.url ? (
              <a href={c.url} target="_blank" rel="noreferrer">
                {author}
              </a>
            ) : (
              author
            )}
            {", "}
            <a href={c.licenseUrl} target="_blank" rel="noreferrer">
              {c.license}
            </a>
          </>
        )}
        {!item.due && " · повторять слово ещё рано: засчитается только ошибка"}
      </span>
    </p>
  );
}

// Verdict — итог ответа строкой; слово из предложения — английское,
// с lang="en" (WCAG 3.1.2)
function Verdict({ result }: { result: ClozeVerdict }) {
  const { label, word } = verdictParts(result);
  if (word === null) return label;
  return (
    <>
      {label} «<span lang="en">{word}</span>»
    </>
  );
}

// Feedback — итог ответа: верно ли, что было в пропуске, что стало
// с графиком, перевод предложения Tatoeba с подписью его автора (CC BY)
function Feedback({
  result,
  credit: c,
  onNext,
}: {
  result: ClozeAttemptResult;
  credit: ClozeCredit | null;
  onNext: () => void;
}) {
  return (
    <>
      <div className={cx(s.feedback, result.correct ? s.ok : s.bad)}>
        <p className={s.verdict}>
          <Icon name={result.correct ? "check" : "x"} />
          <span>
            <Verdict result={result} />
          </span>
        </p>
        <p className={s.schedule}>{scheduleText(result)}</p>
        {result.translation && (
          <p>
            <ClickableText text={result.translation} />
            {c?.translation && (
              <span className={s.credit}>
                {" · перевод: "}
                <a href={c.translation.url} target="_blank" rel="noreferrer">
                  {c.translation.author || "автор не указан"}
                </a>
              </span>
            )}
          </p>
        )}
      </div>
      <div className={s.next}>
        <Button type="button" variant="primary" size="lg" blockOnPhone className={s.onPrimary} autoFocus onClick={onNext}>
          Дальше <Kbd>{enterKey}</Kbd>
        </Button>
      </div>
    </>
  );
}
