"use client";

import { useCallback, useEffect, useState } from "react";
import type { CSSProperties } from "react";

import type { AssignResult, CandidateSource, LessonTerm, ManualCandidatesResult, TermCandidate } from "../../../lib/api";
import ClickableText from "../../clickable-text";
import { FormStatus, requestJson, useSubmit } from "../../forms";

// Post-lesson word picker (S3-8, ADR-034): преподаватель урока отмечает
// кандидатов (клики учеников, доска, свой ввод — S3-7, ADR-033),
// сохраняет словарь урока и назначает его всем или выбранным студентам.
// Слова — через <ClickableText>: попап с переводом и дефиницией помогает
// решить, брать ли слово.

export type Student = { user_id: number; name: string };

const sources: CandidateSource[] = ["click", "whiteboard", "manual"];

const sourceTitles: Record<CandidateSource, string> = {
  click: "Клики учеников",
  whiteboard: "С доски",
  manual: "Добавлены вручную",
};

const rejectReasons: Record<ManualCandidatesResult["rejected"][number]["reason"], string> = {
  not_a_word: "не одно слово",
  unknown: "незнакомое слово",
};

const sectionStyle: CSSProperties = { border: "1px solid #ddd", borderRadius: 8, padding: "1rem", margin: "1rem 0" };
const cellStyle: CSSProperties = { padding: "0.25rem 0.5rem", borderBottom: "1px solid #eee", verticalAlign: "top" };
const mutedStyle: CSSProperties = { color: "#666" };

const candidatesPath = (lessonId: number) => `/api/lessons/${lessonId}/term-candidates`;

function sameSet(a: Set<number>, b: Set<number>): boolean {
  return a.size === b.size && [...a].every((id) => b.has(id));
}

export default function TermPicker({ lessonId, students }: { lessonId: number; students: Student[] }) {
  const [candidates, setCandidates] = useState<TermCandidate[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  // checked — отметки чеклиста, saved — словарь урока в api
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const [saved, setSaved] = useState<Set<number>>(new Set());

  // reload перечитывает кандидатов; отметки, которые преподаватель ещё
  // не сохранил, остаются, а check добавляет к ним только что введённые
  const reload = useCallback(
    async (check: number[] = []) => {
      const res = await requestJson<{ items: TermCandidate[] }>(candidatesPath(lessonId), "GET");
      setCandidates(res.items);
      if (check.length > 0) setChecked((prev) => new Set([...prev, ...check]));
    },
    [lessonId],
  );

  useEffect(() => {
    let cancelled = false;
    requestJson<{ items: TermCandidate[] }>(candidatesPath(lessonId), "GET")
      .then((res) => {
        if (cancelled) return;
        const confirmed = new Set(res.items.filter((c) => c.confirmed).map((c) => c.term_id));
        setCandidates(res.items);
        setChecked(confirmed);
        setSaved(confirmed);
      })
      .catch((e: unknown) => {
        if (!cancelled) setLoadError(e instanceof Error ? e.message : "не удалось получить кандидатов");
      });
    return () => {
      cancelled = true;
    };
  }, [lessonId]);

  const save = useSubmit(async () => {
    const res = await requestJson<{ items: LessonTerm[] }>(`/api/lessons/${lessonId}/terms`, "PUT", {
      term_ids: [...checked],
    });
    const ids = new Set(res.items.map((t) => t.term_id));
    setSaved(ids);
    setChecked(ids);
  });

  function toggle(termId: number) {
    setChecked((prev) => {
      const next = new Set(prev);
      if (next.has(termId)) next.delete(termId);
      else next.add(termId);
      return next;
    });
  }

  const dirty = !sameSet(checked, saved);

  return (
    <section style={sectionStyle}>
      <h2 style={{ marginTop: 0 }}>Словарь урока</h2>
      <p style={mutedStyle}>
        Отметьте слова, которые войдут в словарь урока, сохраните и назначьте студентам — слова появятся в их
        личных словарях со статусом «новое». Слова, которые студент уже учит, сохранят его статус.
      </p>
      {loadError && <p style={{ color: "crimson" }}>{loadError}</p>}
      {candidates === null && !loadError && <p style={mutedStyle}>Загружаем кандидатов…</p>}
      {candidates !== null && (
        <>
          {candidates.length === 0 && <p style={mutedStyle}>Кандидатов пока нет: соберите слова с доски или добавьте свои.</p>}
          {sources.map((source) => (
            <CandidateGroup
              key={source}
              source={source}
              items={candidates.filter((c) => c.source === source)}
              checked={checked}
              onToggle={toggle}
            />
          ))}
          <form onSubmit={save.submit} style={{ display: "flex", gap: "0.75rem", alignItems: "center", flexWrap: "wrap" }}>
            <button type="submit" disabled={save.busy || !dirty}>
              Сохранить словарь урока ({checked.size})
            </button>
            <FormStatus error={save.error} done={save.done && !dirty} doneText="сохранено" />
            {dirty && !save.error && <span style={mutedStyle}>есть несохранённые отметки</span>}
          </form>
        </>
      )}
      <ManualInput lessonId={lessonId} onAdded={(ids) => reload(ids)} />
      <BoardCollect lessonId={lessonId} onCollected={() => reload()} />
      <Assign lessonId={lessonId} students={students} disabledReason={assignBlocker(saved.size, dirty)} />
    </section>
  );
}

function assignBlocker(savedCount: number, dirty: boolean): string | null {
  if (dirty) return "сначала сохраните словарь урока";
  if (savedCount === 0) return "словарь урока пуст";
  return null;
}

function CandidateGroup({
  source,
  items,
  checked,
  onToggle,
}: {
  source: CandidateSource;
  items: TermCandidate[];
  checked: Set<number>;
  onToggle: (termId: number) => void;
}) {
  if (items.length === 0) return null;
  return (
    <div style={{ margin: "1rem 0" }}>
      <h3 style={{ margin: "0 0 0.5rem" }}>
        {sourceTitles[source]} ({items.length})
      </h3>
      <table style={{ borderCollapse: "collapse", width: "100%" }}>
        <tbody>
          {items.map((c) => (
            <tr key={c.term_id}>
              <td style={{ ...cellStyle, width: "2rem" }}>
                {/* Чекбокс отдельно от слова: клик по слову открывает попап,
                    а не переключает отметку */}
                <input
                  type="checkbox"
                  aria-label={`в словарь урока: ${c.lemma}`}
                  checked={checked.has(c.term_id)}
                  onChange={() => onToggle(c.term_id)}
                />
              </td>
              <td style={cellStyle}>
                <strong>
                  <ClickableText text={c.lemma} />
                </strong>
                {c.surface.toLowerCase() !== c.lemma && (
                  <span style={mutedStyle}>
                    {" "}
                    (<ClickableText text={c.surface} />)
                  </span>
                )}
              </td>
              <td style={cellStyle}>{c.translation ?? "—"}</td>
              <td style={{ ...cellStyle, ...mutedStyle, textAlign: "right", whiteSpace: "nowrap" }}>
                {source !== "manual" && `×${c.count}`}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ManualInput — свои слова: через запятую, точку с запятой или с новой
// строки, каждое — одно слово.
function ManualInput({ lessonId, onAdded }: { lessonId: number; onAdded: (termIds: number[]) => Promise<void> }) {
  const [text, setText] = useState("");
  const [rejected, setRejected] = useState<ManualCandidatesResult["rejected"]>([]);
  const { busy, error, done, submit } = useSubmit(async () => {
    const words = text
      .split(/[\n,;]+/)
      .map((w) => w.trim())
      .filter((w) => w !== "");
    if (words.length === 0) throw new Error("введите хотя бы одно слово");
    const res = await requestJson<ManualCandidatesResult>(candidatesPath(lessonId), "POST", { words });
    setRejected(res.rejected);
    setText("");
    await onAdded(res.added.map((a) => a.term_id));
  });
  return (
    <form onSubmit={submit} style={{ margin: "1rem 0" }}>
      <label style={{ display: "block", marginBottom: "0.25rem" }}>
        Добавить свои слова (через запятую или с новой строки)
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={2}
          style={{ display: "block", width: "100%", boxSizing: "border-box" }}
        />
      </label>
      <button type="submit" disabled={busy}>
        Добавить
      </button>{" "}
      <FormStatus error={error} done={done} doneText="добавлено" />
      {rejected.length > 0 && (
        <p style={mutedStyle}>
          Не добавлены: {rejected.map((r) => `${r.word || "(пусто)"} — ${rejectReasons[r.reason]}`).join("; ")}
        </p>
      )}
    </form>
  );
}

// BoardCollect — разбор доски сейчас: когда урок выходит из live, это
// делает worker сам; кнопка нужна уроку, который в live не переходил
// (без LiveKit), и чтобы подобрать слова, пока урок идёт.
function BoardCollect({ lessonId, onCollected }: { lessonId: number; onCollected: () => Promise<void> }) {
  const [found, setFound] = useState<number | null>(null);
  const { busy, error, submit } = useSubmit(async () => {
    const res = await requestJson<{ candidates: number }>(`${candidatesPath(lessonId)}/whiteboard`, "POST");
    setFound(res.candidates);
    await onCollected();
  });
  return (
    <form onSubmit={submit} style={{ margin: "1rem 0" }}>
      <button type="submit" disabled={busy}>
        Собрать слова с доски
      </button>{" "}
      {error && <span style={{ color: "crimson" }}>{error}</span>}
      {!error && found !== null && <span style={mutedStyle}>знакомых слов на доске: {found}</span>}
    </form>
  );
}

function Assign({
  lessonId,
  students,
  disabledReason,
}: {
  lessonId: number;
  students: Student[];
  disabledReason: string | null;
}) {
  const [all, setAll] = useState(true);
  const [picked, setPicked] = useState<Set<number>>(new Set());
  const [result, setResult] = useState<AssignResult | null>(null);
  const { busy, error, submit } = useSubmit(async () => {
    setResult(null);
    const body = all ? { all: true } : { student_ids: [...picked] };
    setResult(await requestJson<AssignResult>(`/api/lessons/${lessonId}/terms/assignments`, "POST", body));
  });

  if (students.length === 0) {
    return <p style={mutedStyle}>У урока нет студентов — назначать словарь некому.</p>;
  }
  const blocked = disabledReason ?? (!all && picked.size === 0 ? "выберите студентов" : null);
  return (
    <form onSubmit={submit} style={{ margin: "1rem 0", borderTop: "1px solid #eee", paddingTop: "0.75rem" }}>
      <h3 style={{ margin: "0 0 0.5rem" }}>Назначить словарь урока</h3>
      <label style={{ marginRight: "1rem" }}>
        <input type="radio" checked={all} onChange={() => setAll(true)} /> всем студентам урока ({students.length})
      </label>
      <label>
        <input type="radio" checked={!all} onChange={() => setAll(false)} /> выбранным
      </label>
      {!all && (
        <div style={{ margin: "0.5rem 0" }}>
          {students.map((s) => (
            <label key={s.user_id} style={{ display: "block" }}>
              <input
                type="checkbox"
                checked={picked.has(s.user_id)}
                onChange={() =>
                  setPicked((prev) => {
                    const next = new Set(prev);
                    if (next.has(s.user_id)) next.delete(s.user_id);
                    else next.add(s.user_id);
                    return next;
                  })
                }
              />{" "}
              {s.name}
            </label>
          ))}
        </div>
      )}
      <div style={{ marginTop: "0.5rem" }}>
        <button type="submit" disabled={busy || blocked !== null}>
          Назначить
        </button>{" "}
        {blocked && <span style={mutedStyle}>{blocked}</span>}
        {error && <span style={{ color: "crimson" }}>{error}</span>}
        {result && !error && (
          <span style={{ color: "#2a7" }}>
            назначено студентам: {result.students}; новых слов в словарях: {result.added}, уже были: {result.kept}
          </span>
        )}
      </div>
    </form>
  );
}
