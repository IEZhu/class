"use client";

import { useCallback, useEffect, useId, useState } from "react";

import type { AssignResult, CandidateSource, LessonTerm, ManualCandidatesResult, TermCandidate } from "../../../lib/api";
import ClickableText from "../../clickable-text";
import { FormStatus, requestJson, useSubmit } from "../../forms";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import { cx } from "../../ui/cx";
import { EmptyState } from "../../ui/empty";
import { Field } from "../../ui/field";
import { Icon } from "../../ui/icons";
import { Skeleton } from "../../ui/skeleton";
import styles from "./picker.module.css";

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
    <section id="picker" className={cx("card section", styles.picker)} aria-labelledby="picker-title">
      <div className="card-head">
        <h2 id="picker-title">Подбор слов</h2>
        <span className="spacer" />
        {candidates !== null && <span className="pill pill--accent">Отмечено {checked.size}</span>}
      </div>
      <p className={cx("hint", styles.intro)}>
        Отметьте слова, которые войдут в словарь урока, сохраните и назначьте студентам — слова появятся в их
        личных словарях со статусом «новое». Слова, которые студент уже учит, сохранят его статус.
      </p>
      {loadError && (
        <div className={styles.pad}>
          <Alert tone="danger">
            <span className={styles.cap}>{loadError}</span>
          </Alert>
        </div>
      )}
      {candidates === null && !loadError && (
        <div className={styles.pad}>
          <Skeleton lines={5} label="Загружаем кандидатов…" showLabel />
        </div>
      )}
      {candidates !== null && (
        // Обёртка ограничивает липкое сохранение списком кандидатов: оно не
        // наезжает на шапку карточки и не тянется к инструментам ниже
        <div className={styles.candidates}>
          {candidates.length === 0 && (
            <EmptyState icon="search" title="Кандидатов пока нет" titleAs="h3">
              Соберите слова с доски или добавьте свои.
            </EmptyState>
          )}
          {sources.map((source) => (
            <CandidateGroup
              key={source}
              source={source}
              items={candidates.filter((c) => c.source === source)}
              checked={checked}
              onToggle={toggle}
            />
          ))}
          {/* Сохранение липнет к низу экрана, пока видны кандидаты: длинный
              список не надо проматывать до кнопки (DESIGN §6.3) */}
          <form onSubmit={save.submit} className={cx("card-foot", styles.saveBar)}>
            <Button type="submit" variant="primary" disabled={save.busy || !dirty} busy={save.busy}>
              Сохранить словарь урока ({checked.size})
            </Button>
            <FormStatus error={save.error} done={save.done && !dirty} doneText="сохранено" />
            {dirty && !save.error && <span className="small muted">есть несохранённые отметки</span>}
          </form>
        </div>
      )}
      <div className={styles.tools}>
        <div className={styles.toolsCol}>
          <ManualInput lessonId={lessonId} onAdded={(ids) => reload(ids)} />
          <BoardCollect lessonId={lessonId} onCollected={() => reload()} />
        </div>
        <div className={styles.toolsCol}>
          <Assign lessonId={lessonId} students={students} disabledReason={assignBlocker(saved.size, dirty)} />
        </div>
      </div>
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
  // ×N — сколько раз слово кликали или встретили на доске; у своих слов нет
  const count = (c: TermCandidate) => (source !== "manual" ? `×${c.count}` : "");
  return (
    <div className={styles.group}>
      <h3 className={styles.groupTitle}>
        {sourceTitles[source]} <span className="muted">({items.length})</span>
      </h3>
      <div className="table-wrap">
        <table className={cx("table table--dense", styles.table)}>
          {/* Одна сетка колонок у всех групп: слова и переводы стоят ровно */}
          <colgroup>
            <col className={styles.colCheck} />
            <col className={styles.colLemma} />
            <col />
            <col className={styles.colCount} />
          </colgroup>
          <tbody>
            {items.map((c) => (
              <tr key={c.term_id} className={cx(checked.has(c.term_id) && styles.checked)}>
                <td className={styles.checkCell}>
                  {/* Чекбокс отдельно от слова: клик по слову открывает попап,
                      а не переключает отметку. Подпись-ячейка — цель 32px */}
                  <label className={styles.checkLabel}>
                    <input
                      type="checkbox"
                      aria-label={`в словарь урока: ${c.lemma}`}
                      checked={checked.has(c.term_id)}
                      onChange={() => onToggle(c.term_id)}
                    />
                  </label>
                </td>
                <td>
                  <strong className={styles.lemma}>
                    <ClickableText text={c.lemma} />
                  </strong>
                  {c.surface.toLowerCase() !== c.lemma && (
                    <span className="muted">
                      {" "}
                      (<ClickableText text={c.surface} />)
                    </span>
                  )}
                  {/* На телефоне ×N — под словом, без своей колонки */}
                  {count(c) && <span className={cx("muted small", styles.countBelow)}>{count(c)}</span>}
                </td>
                <td className={styles.translation}>{c.translation ?? "—"}</td>
                <td className={cx("muted", styles.countCell)}>{count(c)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
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
    <form onSubmit={submit} className={styles.tool}>
      <Field label="Добавить свои слова" hint="Через запятую или с новой строки">
        <textarea className="textarea" value={text} onChange={(e) => setText(e.target.value)} rows={2} />
      </Field>
      <div className="row">
        <Button type="submit" variant="secondary" disabled={busy} busy={busy}>
          <Icon name="plus" size="sm" />
          Добавить
        </Button>
        <FormStatus error={error} done={done} doneText="добавлено" />
      </div>
      {rejected.length > 0 && (
        <p className="small muted">
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
    <form onSubmit={submit} className={cx("row", styles.tool)}>
      <Button type="submit" variant="secondary" disabled={busy} busy={busy}>
        <Icon name="edit" size="sm" />
        Собрать слова с доски
      </Button>
      <FormStatus error={error} done={false} doneText="" />
      {!error && found !== null && <span className="small muted">знакомых слов на доске: {found}</span>}
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
  const id = useId();
  const { busy, error, submit } = useSubmit(async () => {
    setResult(null);
    const body = all ? { all: true } : { student_ids: [...picked] };
    setResult(await requestJson<AssignResult>(`/api/lessons/${lessonId}/terms/assignments`, "POST", body));
  });

  if (students.length === 0) {
    return <p className="small muted">У урока нет студентов — назначать словарь некому.</p>;
  }
  const blocked = disabledReason ?? (!all && picked.size === 0 ? "выберите студентов" : null);
  return (
    <form onSubmit={submit} className={styles.tool}>
      <fieldset className="fieldset">
        <legend>Назначить словарь урока</legend>
        <label className="check">
          <input type="radio" name={`${id}-whom`} checked={all} onChange={() => setAll(true)} />
          всем студентам урока ({students.length})
        </label>
        <label className="check">
          <input type="radio" name={`${id}-whom`} checked={!all} onChange={() => setAll(false)} />
          выбранным
        </label>
        {!all && (
          <fieldset className={cx("fieldset", styles.students)}>
            <legend className="sr-only">Кому назначить</legend>
            {students.map((s) => (
              <label key={s.user_id} className="check">
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
                />
                {s.name}
              </label>
            ))}
          </fieldset>
        )}
      </fieldset>
      <div className="row">
        <Button type="submit" variant="primary" disabled={busy || blocked !== null} busy={busy}>
          Назначить
        </Button>
        {blocked && <span className="small muted">{blocked}</span>}
        <FormStatus
          error={error}
          done={result !== null}
          doneText={
            result
              ? `назначено студентам: ${result.students}; новых слов в словарях: ${result.added}, уже были: ${result.kept}`
              : ""
          }
        />
      </div>
    </form>
  );
}
