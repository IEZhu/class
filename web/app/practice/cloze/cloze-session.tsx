"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import type { CSSProperties, FormEvent } from "react";

import type { ClozeAttemptResult, ClozeItem, ClozeOptions, ClozePage } from "../../../lib/api";
import {
  clozeKeyAction,
  isAnswerOption,
  responseMs,
  scheduleText,
  sourceLabel,
  verdictText,
} from "../../../lib/cloze-session";
import type { ClozeMode } from "../../../lib/cloze-session";
import { reviewId } from "../../../lib/review-session";
import ClickableText from "../../clickable-text";
import { RequestError, requestJson } from "../../forms";
import { termStatuses } from "../../term-status-control";

const mutedStyle = { color: "#666" } as const;
const cardStyle: CSSProperties = { border: "1px solid #ddd", borderRadius: 8, padding: "1.25rem", lineHeight: 1.6 };
const buttonStyle: CSSProperties = { padding: "0.4rem 0.8rem", border: "1px solid #ccc", borderRadius: 4, background: "#fff" };
const blankStyle: CSSProperties = { display: "inline-block", minWidth: "4em", borderBottom: "2px solid #4c6ef5" };

function newAttemptId(): string {
  return reviewId(crypto.getRandomValues(new Uint8Array(16)));
}

type Options = Pick<ClozeItem, "options" | "distractor_source">;

// ClozeSession — проход выдачи: предложение с пропуском, ответ кнопкой
// (клавиши 1–4) или вводом, итог и следующий пункт по Enter. attempt_id
// заводится при первой попытке ответа и живёт до следующего пункта:
// повтор после сбоя сети api засчитает один раз. С LLM-дистракторами
// (llm_available) выдача просит их для пунктов после первого: первый уже
// на экране, менять ему варианты на глазах нельзя.
export default function ClozeSession({ page }: { page: ClozePage }) {
  const router = useRouter();
  const [index, setIndex] = useState(0);
  const [mode, setMode] = useState<ClozeMode>("choice");
  const [typed, setTyped] = useState("");
  const [chosen, setChosen] = useState<string | null>(null);
  const [result, setResult] = useState<ClozeAttemptResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tally, setTally] = useState({ correct: 0, wrong: 0 });
  const [llmOptions, setLlmOptions] = useState<Record<number, Options>>({});
  const [llmNote, setLlmNote] = useState<string | null>(null);
  const attemptRef = useRef<string | null>(null);
  const shownAt = useRef(0);
  const indexRef = useRef(0);
  const item: ClozeItem | undefined = page.items[index];
  const total = page.items.length;
  const options = item ? (llmOptions[item.id] ?? item).options : [];

  // Время ответа — от показа пункта
  useEffect(() => {
    indexRef.current = index;
    shownAt.current = performance.now();
  }, [index]);

  useEffect(() => {
    const ids = page.items.slice(1).filter((it) => it.distractor_source === "list").map((it) => it.id);
    if (!page.llm_available || ids.length === 0) return;
    let cancelled = false;
    requestJson<ClozeOptions>("/api/practice/cloze/distractors", "POST", { item_ids: ids })
      .then((res) => {
        if (cancelled) return;
        // Пункт, который уже на экране или пройден, вариантов не меняет
        const shown = new Set(page.items.slice(0, indexRef.current + 1).map((it) => it.id));
        setLlmOptions(Object.fromEntries(res.items.filter((it) => !shown.has(it.id)).map((it) => [it.id, it])));
      })
      .catch((e: unknown) => {
        // Лимит, сбой провайдера — пункты остаются с вариантами словаря
        if (!cancelled) setLlmNote(`варианты от LLM недоступны: ${e instanceof Error ? e.message : "сбой"}`);
      });
    return () => {
      cancelled = true;
    };
  }, [page]);

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
      termStatuses.setStatus(res.lemma, res.status);
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

  // Клавиши — на всё окно. Поля ввода и попап слова не перехватываются;
  // Enter и пробел на кнопке или ссылке — их собственное нажатие.
  // Обработчик переподписывается на каждый рендер: видит текущий пункт
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!item || e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;
      const target = e.target instanceof Element ? e.target : null;
      if (target?.closest("input, textarea, select, [contenteditable], [role=dialog]")) return;
      if ((e.key === " " || e.key === "Enter") && target?.closest("button, a")) return;
      const action = clozeKeyAction(e.key, result !== null, mode, options.length);
      if (!action) return;
      e.preventDefault();
      if (action.kind === "next") next(null);
      else void answer(options[action.index], "choice");
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  function submitTyped(e: FormEvent) {
    e.preventDefault();
    void answer(typed, "typing");
  }

  const done = Math.min(index, total);
  if (total === 0) {
    return (
      <div style={cardStyle}>
        <p style={{ marginTop: 0 }}>Пока не из чего составить упражнение.</p>
        <p style={mutedStyle}>
          Нужны слова в <Link href="/dictionary">словаре</Link> и предложения с ними: из уроков, ваших текстов или корпуса
          Tatoeba.
        </p>
      </div>
    );
  }
  return (
    <section>
      <div style={{ display: "flex", justifyContent: "space-between", gap: "1rem", flexWrap: "wrap" }}>
        <span>
          {done} из {total}
          {page.due_total > 0 && <span style={mutedStyle}> · к повторению {page.due_total}</span>}
        </span>
        <span style={mutedStyle}>
          верно {tally.correct} · неверно {tally.wrong}
        </span>
      </div>
      <div
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={done}
        style={{ height: 6, background: "#eee", borderRadius: 3, margin: "0.5rem 0 1rem" }}
      >
        <div style={{ width: `${(100 * done) / total}%`, height: "100%", background: "#4c6ef5", borderRadius: 3 }} />
      </div>
      <div role="group" aria-label="Как отвечать" style={{ display: "flex", gap: "0.5rem", marginBottom: "0.75rem" }}>
        {(["choice", "typing"] as const).map((m) => (
          <button
            key={m}
            type="button"
            aria-pressed={mode === m}
            disabled={busy}
            onClick={() => setMode(m)}
            style={{ ...buttonStyle, fontWeight: mode === m ? 600 : 400, borderColor: mode === m ? "#4c6ef5" : "#ccc" }}
          >
            {m === "choice" ? "Варианты" : "Ввод слова"}
          </button>
        ))}
      </div>
      {error && <p style={{ color: "crimson" }}>{error}</p>}
      {item ? (
        <div style={cardStyle} key={item.id}>
          <ItemSource item={item} />
          <p style={{ fontSize: "1.25rem", margin: "0.5rem 0 1rem" }}>
            {result ? (
              <ClickableText text={result.sentence} />
            ) : (
              <>
                <ClickableText text={item.before} />
                <span style={blankStyle} aria-label="пропуск">
                  &nbsp;
                </span>
                <ClickableText text={item.after} />
              </>
            )}
          </p>
          {mode === "choice" ? (
            <div style={{ display: "flex", gap: "0.5rem", flexWrap: "wrap" }}>
              {options.map((o, i) => (
                <button
                  key={o}
                  type="button"
                  disabled={busy || result !== null}
                  onClick={() => void answer(o, "choice")}
                  style={{ ...buttonStyle, ...optionColor(o, chosen, result) }}
                >
                  {o} <kbd style={mutedStyle}>{i + 1}</kbd>
                </button>
              ))}
            </div>
          ) : (
            <form onSubmit={submitTyped} style={{ display: "flex", gap: "0.5rem" }}>
              <input
                key={item.id}
                autoFocus
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                disabled={busy || result !== null}
                maxLength={64}
                aria-label="Пропущенное слово"
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                style={{ flex: 1, padding: "0.4rem", fontSize: "1rem" }}
              />
              <button type="submit" disabled={busy || result !== null || !typed.trim()} style={buttonStyle}>
                Проверить
              </button>
            </form>
          )}
          {result && <Feedback result={result} attribution={item.attribution} onNext={() => next(null)} />}
        </div>
      ) : (
        <div style={cardStyle}>
          <p style={{ marginTop: 0 }}>
            Выдача пройдена: верно {tally.correct} из {tally.correct + tally.wrong}.
          </p>
          <button type="button" onClick={() => router.refresh()} style={buttonStyle}>
            Ещё предложения
          </button>
        </div>
      )}
      {llmNote && <p style={{ ...mutedStyle, fontSize: "0.85em" }}>{llmNote}</p>}
    </section>
  );
}

// optionColor — после ответа: верный вариант зелёный, выбранный неверный —
// красный
function optionColor(option: string, chosen: string | null, result: ClozeAttemptResult | null): CSSProperties {
  if (!result) return option === chosen ? { borderColor: "#4c6ef5" } : {};
  if (isAnswerOption(option, result)) return { background: "#d3f9d8", borderColor: "#2f9e44" };
  return option === chosen ? { background: "#ffe3e3", borderColor: "#e03131" } : {};
}

// ItemSource — откуда предложение; у Tatoeba — подпись автора (CC BY)
function ItemSource({ item }: { item: ClozeItem }) {
  const small = { ...mutedStyle, fontSize: "0.85em" };
  const a = item.attribution;
  return (
    <div style={small}>
      {item.lesson_id !== null ? <Link href={`/lesson/${item.lesson_id}`}>{sourceLabel(item.source)}</Link> : sourceLabel(item.source)}
      {a && (
        <>
          {" · "}
          <a href={a.url} target="_blank" rel="noreferrer">
            {a.author || "автор не указан"}
          </a>
          {", "}
          <a href={a.license_url} target="_blank" rel="noreferrer">
            {a.license}
          </a>
        </>
      )}
      {!item.due && " · повторять слово ещё рано: засчитается только ошибка"}
    </div>
  );
}

// Feedback — итог ответа: верно ли, что было в пропуске, что стало
// с графиком, перевод предложения Tatoeba с подписью его автора (CC BY)
function Feedback({
  result,
  attribution: a,
  onNext,
}: {
  result: ClozeAttemptResult;
  attribution: ClozeItem["attribution"];
  onNext: () => void;
}) {
  return (
    <div style={{ borderTop: "1px solid #eee", marginTop: "1rem", paddingTop: "0.75rem" }}>
      <p style={{ margin: "0 0 0.25rem", fontWeight: 600, color: result.correct ? "#2f9e44" : "#e03131" }}>
        {verdictText(result)}
      </p>
      <p style={{ ...mutedStyle, margin: "0 0 0.5rem" }}>{scheduleText(result)}</p>
      {result.translation && (
        <p style={{ margin: "0 0 0.5rem" }}>
          <ClickableText text={result.translation} />
          {a && (
            <span style={{ ...mutedStyle, fontSize: "0.8em" }}>
              {" · перевод: "}
              <a href={a.translation_url} target="_blank" rel="noreferrer">
                {a.translation_author || "автор не указан"}
              </a>
            </span>
          )}
        </p>
      )}
      <button type="button" autoFocus onClick={onNext} style={buttonStyle}>
        Дальше <kbd style={mutedStyle}>Enter</kbd>
      </button>
    </div>
  );
}
