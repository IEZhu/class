"use client";

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { Transcript, Utterance } from "../../../lib/api";
import { activeAt, clock } from "../../../lib/timeline";
import { FormStatus, RequestError, requestJson, useSubmit } from "../../forms";
import UtteranceText from "./utterance-text";

// Человек урока, с которым преподаватель сопоставляет метку спикера (S2-5):
// сам teacher урока или участник снапшота — других api не примет.
export type Person = { user_id: number; name: string };

type Speaker = { speaker_label: string; user_id: number | null; speaker_name: string | null };

const statusTexts: Record<Exclude<Transcript["status"], "completed">, string> = {
  queued: "Транскрипт ещё готовится.",
  submitted: "Транскрипт ещё готовится.",
  fetching: "Транскрипт ещё готовится.",
  error: "Транскрипт не получился — запись доступна без него.",
};

const mutedStyle = { color: "#666", margin: 0 } as const;

// Транскрипт по спикерам (S2-4) рядом с плеером: подряд идущие реплики
// одного спикера — один блок, клик по реплике мотает запись к её началу,
// звучащая реплика подсвечена и держится в видимой части списка.
// Преподаватель урока правит текст реплик и сопоставляет метки с людьми.
export default function TranscriptPanel({
  lessonId,
  timeMs,
  onSeek,
  canEdit,
  people,
}: {
  lessonId: number;
  timeMs: number;
  onSeek: (ms: number) => void;
  canEdit: boolean;
  people: Person[];
}) {
  const [transcript, setTranscript] = useState<Transcript | null>(null);
  // missing — транскрипта у урока нет (ASR не настроен или записи не было)
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<number | null>(null);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    requestJson<Transcript>(`/api/lessons/${lessonId}/transcript`, "GET")
      .then((t) => {
        if (!cancelled) setTranscript(t);
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        if (e instanceof RequestError && e.status === 404) setMissing(true);
        else setError(e instanceof Error ? e.message : "не удалось получить транскрипт");
      });
    return () => {
      cancelled = true;
    };
  }, [lessonId]);

  const utterances = useMemo(() => transcript?.utterances ?? [], [transcript]);
  const timeline = useMemo(() => [...utterances].sort((a, b) => a.start_ms - b.start_ms), [utterances]);
  const active = activeAt(timeline, timeMs);
  const groups = useMemo(() => groupBySpeaker(utterances), [utterances]);

  // Звучащая реплика остаётся в видимой части списка; прокручивается только
  // сам список, не страница. Пока преподаватель правит текст, список стоит.
  useEffect(() => {
    const box = listRef.current;
    if (active === null || editing !== null || !box) return;
    const row = box.querySelector<HTMLElement>(`[data-idx="${active}"]`);
    if (!row) return;
    const top = row.offsetTop;
    const bottom = top + row.offsetHeight;
    if (top < box.scrollTop || bottom > box.scrollTop + box.clientHeight) {
      box.scrollTo({ top: Math.max(0, top - box.clientHeight / 3), behavior: "smooth" });
    }
  }, [active, editing]);

  const saved = useCallback((u: Utterance) => {
    setTranscript((t) => t && { ...t, utterances: t.utterances.map((it) => (it.idx === u.idx ? u : it)) });
    setEditing(null);
  }, []);

  const mapped = useCallback((s: Speaker) => {
    setTranscript(
      (t) =>
        t && {
          ...t,
          utterances: t.utterances.map((it) =>
            it.speaker_label === s.speaker_label ? { ...it, user_id: s.user_id, speaker_name: s.speaker_name } : it,
          ),
        },
    );
  }, []);

  if (missing) return <p style={mutedStyle}>Транскрипта у этого урока нет.</p>;
  if (error) return <p style={{ color: "crimson", margin: 0 }}>Транскрипт недоступен: {error}</p>;
  if (!transcript) return <p style={mutedStyle}>Загружаем транскрипт…</p>;
  if (transcript.status !== "completed") return <p style={mutedStyle}>{statusTexts[transcript.status]}</p>;
  if (utterances.length === 0) return <p style={mutedStyle}>В записи не распознано речи.</p>;

  return (
    <div>
      <h2 style={{ margin: "0 0 0.5rem", fontSize: "1.1rem" }}>Транскрипт</h2>
      {canEdit && (
        <SpeakerMapping lessonId={lessonId} utterances={utterances} people={people} onSeek={onSeek} onMapped={mapped} />
      )}
      <div
        ref={listRef}
        // position: relative — offsetTop реплик отсчитывается от списка
        style={{ position: "relative", maxHeight: "70vh", overflowY: "auto", paddingRight: "0.25rem" }}
      >
        {groups.map((g) => (
          <div key={g[0].idx} style={{ marginBottom: "0.75rem" }}>
            <div style={{ fontWeight: 600, fontSize: "0.9rem", color: "#2a7" }}>{speakerTitle(g[0])}</div>
            {g.map((u) => (
              <UtteranceRow
                key={u.idx}
                lessonId={lessonId}
                utterance={u}
                active={u.idx === active}
                editing={u.idx === editing}
                canEdit={canEdit}
                onSeek={onSeek}
                onEdit={setEditing}
                onSaved={saved}
              />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

// Строка мемоизирована: время плеера меняется несколько раз в секунду,
// а перерисовать нужно только две реплики — погасшую и подсвеченную.
const UtteranceRow = memo(function UtteranceRow({
  lessonId,
  utterance: u,
  active,
  editing,
  canEdit,
  onSeek,
  onEdit,
  onSaved,
}: {
  lessonId: number;
  utterance: Utterance;
  active: boolean;
  editing: boolean;
  canEdit: boolean;
  onSeek: (ms: number) => void;
  onEdit: (idx: number | null) => void;
  onSaved: (u: Utterance) => void;
}) {
  return (
    <div
      data-idx={u.idx}
      style={{
        padding: "0.2rem 0.4rem",
        borderLeft: `3px solid ${active ? "#2a7" : "transparent"}`,
        background: active ? "#e8f6ef" : "transparent",
        borderRadius: 4,
      }}
    >
      {editing ? (
        <UtteranceEditor lessonId={lessonId} utterance={u} onSaved={onSaved} onCancel={() => onEdit(null)} />
      ) : (
        <>
          <button
            type="button"
            onClick={() => onSeek(u.start_ms)}
            title="Перейти к этому месту записи"
            style={{ border: "none", background: "none", padding: 0, color: "#888", cursor: "pointer", fontVariantNumeric: "tabular-nums" }}
          >
            {clock(u.start_ms)}
          </button>{" "}
          <span onClick={() => onSeek(u.start_ms)} style={{ cursor: "pointer" }}>
            <UtteranceText text={u.text} />
          </span>
          {u.edited_at && (
            <span title={`исправлено ${new Date(u.edited_at).toLocaleString("ru-RU")}`} style={{ color: "#999", fontSize: "0.8rem" }}>
              {" "}
              (испр.)
            </span>
          )}
          {canEdit && (
            <button
              type="button"
              onClick={() => onEdit(u.idx)}
              title="Исправить текст реплики"
              aria-label="Исправить текст реплики"
              style={{ border: "none", background: "none", padding: "0 0.25rem", color: "#888", cursor: "pointer" }}
            >
              ✎
            </button>
          )}
        </>
      )}
    </div>
  );
});

// Правка текста реплики (S2-4): компенсация ошибок ASR. Пробелы и переводы
// строк api схлопывает сам — реплика остаётся одной строкой речи.
function UtteranceEditor({
  lessonId,
  utterance: u,
  onSaved,
  onCancel,
}: {
  lessonId: number;
  utterance: Utterance;
  onSaved: (u: Utterance) => void;
  onCancel: () => void;
}) {
  const [text, setText] = useState(u.text);
  const save = useSubmit(async () => {
    onSaved(await requestJson<Utterance>(`/api/lessons/${lessonId}/transcript/utterances/${u.idx}`, "PATCH", { text }));
  });

  return (
    <form onSubmit={save.submit}>
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={3}
        autoFocus
        style={{ width: "100%", boxSizing: "border-box", padding: "0.4rem", font: "inherit" }}
      />
      <div style={{ display: "flex", gap: "0.5rem", alignItems: "center", flexWrap: "wrap" }}>
        <button type="submit" disabled={save.busy || text.trim() === ""}>
          {save.busy ? "Сохраняем…" : "Сохранить"}
        </button>
        <button type="button" onClick={onCancel} disabled={save.busy}>
          Отмена
        </button>
        <FormStatus error={save.error} done={false} doneText="" />
      </div>
    </form>
  );
}

// Маппинг спикеров (S2-5): метка ASR → человек урока. Возле метки — её первая
// реплика: по голосу и словам легче узнать, кто это. Выбор сохраняется сразу.
function SpeakerMapping({
  lessonId,
  utterances,
  people,
  onSeek,
  onMapped,
}: {
  lessonId: number;
  utterances: Utterance[];
  people: Person[];
  onSeek: (ms: number) => void;
  onMapped: (s: Speaker) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const firstByLabel = useMemo(() => {
    const first = new Map<string, Utterance>();
    for (const u of utterances) if (!first.has(u.speaker_label)) first.set(u.speaker_label, u);
    return [...first.values()];
  }, [utterances]);

  async function map(label: string, userID: number | null) {
    setBusy(label);
    setError(null);
    try {
      onMapped(
        await requestJson<Speaker>(`/api/lessons/${lessonId}/transcript/speakers/${encodeURIComponent(label)}`, "PUT", {
          user_id: userID,
        }),
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : "не удалось сохранить");
    } finally {
      setBusy(null);
    }
  }

  return (
    <fieldset style={{ border: "1px solid #eee", borderRadius: 6, margin: "0 0 0.75rem", padding: "0.5rem 0.75rem" }}>
      <legend style={{ fontSize: "0.9rem", color: "#666" }}>Кто говорит</legend>
      {firstByLabel.map((u) => (
        <div key={u.speaker_label} style={{ display: "flex", gap: "0.5rem", alignItems: "center", flexWrap: "wrap", marginBottom: "0.25rem" }}>
          <label>
            Спикер {u.speaker_label}:{" "}
            <select
              value={u.user_id ?? ""}
              disabled={busy !== null}
              onChange={(e) => void map(u.speaker_label, e.target.value === "" ? null : Number(e.target.value))}
            >
              <option value="">— не указан —</option>
              {/* Сопоставленный раньше человек мог выпасть из списка — показываем и его */}
              {u.user_id !== null && !people.some((p) => p.user_id === u.user_id) && (
                <option value={u.user_id}>{u.speaker_name ?? `#${u.user_id}`}</option>
              )}
              {people.map((p) => (
                <option key={p.user_id} value={p.user_id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            onClick={() => onSeek(u.start_ms)}
            title="Послушать первую реплику"
            style={{ border: "none", background: "none", padding: 0, color: "#888", cursor: "pointer", textAlign: "left" }}
          >
            ▶ «{preview(u.text)}»
          </button>
        </div>
      ))}
      <FormStatus error={error} done={false} doneText="" />
    </fieldset>
  );
}

// Подряд идущие реплики одного спикера — один блок с его именем. Спикер —
// человек, если метка сопоставлена: ASR иногда делит одного на две метки.
function groupBySpeaker(utterances: Utterance[]): Utterance[][] {
  const speaker = (u: Utterance) => (u.user_id !== null ? `user:${u.user_id}` : `label:${u.speaker_label}`);
  const groups: Utterance[][] = [];
  for (const u of utterances) {
    const last = groups[groups.length - 1];
    if (last && speaker(last[0]) === speaker(u)) last.push(u);
    else groups.push([u]);
  }
  return groups;
}

function speakerTitle(u: Utterance): string {
  return u.speaker_name ?? `Спикер ${u.speaker_label}`;
}

function preview(text: string): string {
  return text.length > 60 ? `${text.slice(0, 60)}…` : text;
}
