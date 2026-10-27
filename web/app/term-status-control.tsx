"use client";

import { useState } from "react";

import { STATUS_LABELS, TERM_STATUSES, TermStatuses } from "../lib/term-status";
import type { LookupResult, TermStatus } from "../lib/term-status";
import { request, requestJson } from "./forms";

// termStatuses — статусы слов зрителя, один кэш на вкладку: его читают
// все <ClickableText> страницы, а сохранение статуса — здесь — сразу
// перекрашивает формы слова и на других страницах без перезагрузки.
export const termStatuses = new TermStatuses((words) =>
  requestJson<LookupResult>("/api/user-terms/lookup", "POST", { words }),
);

// buttonStyle — кнопки попапа слова: статусы и «объясни» (clickable-text)
export const buttonStyle = { padding: "0.2rem 0.5rem", border: "1px solid #ccc", borderRadius: 4, background: "#fff" };
const activeStyle = { ...buttonStyle, background: "#333", borderColor: "#333", color: "#fff" };

// TermStatusControl — «в мой словарь», статус 1..5 и «убрать» (S3-5):
// в попапе слова и на странице словаря. lessonId — урок, с которого
// слово пришло (source_lesson_id); api запоминает первый.
export default function TermStatusControl({
  termId,
  lemma,
  status,
  lessonId,
  onSaved,
}: {
  termId: number;
  lemma: string;
  status: TermStatus | null;
  lessonId?: number | null;
  onSaved?: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(next: TermStatus | null) {
    setBusy(true);
    setError(null);
    try {
      if (next === null) await request(`/api/user-terms/${termId}`, "DELETE");
      else await request("/api/user-terms", "POST", { term_id: termId, status: next, source_lesson_id: lessonId ?? undefined });
      termStatuses.setStatus(lemma, next);
      onSaved?.();
    } catch (e) {
      setError(e instanceof Error ? e.message : "не удалось сохранить");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ display: "flex", gap: "0.25rem", flexWrap: "wrap", alignItems: "center" }}>
      {status === null ? (
        <button type="button" disabled={busy} onClick={() => save(1)} style={buttonStyle}>
          + в мой словарь
        </button>
      ) : (
        <>
          {TERM_STATUSES.map((s) => (
            <button
              key={s}
              type="button"
              disabled={busy}
              aria-pressed={s === status}
              title={STATUS_LABELS[s]}
              onClick={() => s !== status && save(s)}
              style={s === status ? activeStyle : buttonStyle}
            >
              {s}
            </button>
          ))}
          <span style={{ color: "#666", fontSize: "0.85em" }}>{STATUS_LABELS[status]}</span>
          <button type="button" disabled={busy} onClick={() => save(null)} style={{ ...buttonStyle, marginLeft: "auto" }}>
            убрать
          </button>
        </>
      )}
      {error && <span style={{ color: "crimson", fontSize: "0.85em", width: "100%" }}>{error}</span>}
    </div>
  );
}
