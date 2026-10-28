"use client";

import { memo, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";

import type { Transcript, Utterance } from "../../../lib/api";
import { activeAt, clock } from "../../../lib/timeline";
import { FormStatus, RequestError, requestJson, useSubmit } from "../../forms";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import { cx } from "../../ui/cx";
import { Icon } from "../../ui/icons";
import { Skeleton } from "../../ui/skeleton";
import styles from "./media.module.css";
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
  const titleId = useId();

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

  // Все состояния — в одной карточке «Транскрипт»: место рядом с плеером
  // не прыгает, пока транскрипт грузится (DESIGN §4.3)
  function card(body: ReactNode, count?: number) {
    return (
      <section className={cx("card", styles.transcript)} aria-labelledby={titleId}>
        <div className="card-head">
          <h2 id={titleId}>Транскрипт</h2>
          {count !== undefined && <span className={cx("muted small", styles.count)}>{replicas(count)}</span>}
        </div>
        {body}
      </section>
    );
  }

  if (missing) return card(<p className={cx("muted", styles.note)}>Транскрипта у этого урока нет.</p>);
  if (error) {
    return card(
      <div className={styles.note}>
        <Alert tone="danger">Транскрипт недоступен: {error}</Alert>
      </div>,
    );
  }
  if (!transcript) {
    return card(
      <div className={styles.note}>
        <Skeleton lines={6} label="Загружаем транскрипт…" />
      </div>,
    );
  }
  if (transcript.status !== "completed") {
    return card(
      <div className={styles.note}>
        <Alert tone={transcript.status === "error" ? "warning" : "info"}>{statusTexts[transcript.status]}</Alert>
      </div>,
    );
  }
  if (utterances.length === 0) return card(<p className={cx("muted", styles.note)}>В записи не распознано речи.</p>);

  return card(
    <>
      {canEdit && (
        <SpeakerMapping lessonId={lessonId} utterances={utterances} people={people} onSeek={onSeek} onMapped={mapped} />
      )}
      {/* position: relative (в CSS) — offsetTop реплик отсчитывается от списка */}
      <div ref={listRef} className={styles.list}>
        {groups.map((g) => (
          <div key={g[0].idx} className={styles.group}>
            <div className={styles.speaker}>
              <span className="avatar avatar--sm" aria-hidden="true">
                {speakerBadge(g[0])}
              </span>
              {speakerTitle(g[0])}
            </div>
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
    </>,
    utterances.length,
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
  const at = clock(u.start_ms);
  return (
    <div
      data-idx={u.idx}
      className={cx(styles.utt, active && styles.active, editing && styles.editing)}
      aria-current={active || undefined}
    >
      {editing ? (
        <UtteranceEditor lessonId={lessonId} utterance={u} onSaved={onSaved} onCancel={() => onEdit(null)} />
      ) : (
        <>
          <button
            type="button"
            className="ts"
            onClick={() => onSeek(u.start_ms)}
            title="Перейти к этому месту записи"
            aria-label={`Перейти к ${at}`}
          >
            {at}
          </button>
          <div className={styles.uttBody}>
            <span className={styles.uttText} onClick={() => onSeek(u.start_ms)}>
              <UtteranceText text={u.text} />
            </span>
            {u.edited_at && (
              <>
                {" "}
                <span
                  className={cx("pill pill--plain", styles.edited)}
                  title={`исправлено ${new Date(u.edited_at).toLocaleString("ru-RU")}`}
                >
                  испр.
                </span>
              </>
            )}
          </div>
          {canEdit && (
            <button
              type="button"
              className="icon-btn icon-btn--sm"
              onClick={() => onEdit(u.idx)}
              title="Исправить текст реплики"
              aria-label="Исправить текст реплики"
            >
              <Icon name="edit" size="sm" />
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
    <form onSubmit={save.submit} className={styles.editor}>
      <textarea
        className="textarea"
        aria-label="Текст реплики"
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={3}
        autoFocus
      />
      <div className="row">
        <Button type="submit" variant="primary" size="sm" disabled={save.busy || text.trim() === ""} busy={save.busy}>
          {save.busy ? "Сохраняем…" : "Сохранить"}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={save.busy}>
          Отмена
        </Button>
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
  const baseId = useId();
  const firstByLabel = useMemo(() => {
    const first = new Map<string, Utterance>();
    for (const u of utterances) if (!first.has(u.speaker_label)) first.set(u.speaker_label, u);
    return [...first.values()];
  }, [utterances]);
  // Раскрыто, пока есть несопоставленные метки; дальше открывает и
  // закрывает сам преподаватель (open задаётся только при монтировании)
  const [openAtStart] = useState(() => firstByLabel.some((u) => u.user_id === null));

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
    <details className={cx("disclosure", styles.speakers)} open={openAtStart}>
      <summary>
        Кто говорит
        <span className="muted small">· {firstByLabel.length}</span>
      </summary>
      <div className={styles.speakerRows}>
        {firstByLabel.map((u) => (
          <div key={u.speaker_label} className={styles.speakerRow}>
            <label className="label" htmlFor={`${baseId}-${u.speaker_label}`}>
              Спикер {u.speaker_label}
            </label>
            <select
              id={`${baseId}-${u.speaker_label}`}
              className="select"
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
            <button
              type="button"
              className={cx("btn btn--ghost btn--sm", styles.preview)}
              onClick={() => onSeek(u.start_ms)}
              title="Послушать первую реплику"
            >
              <Icon name="play" size="sm" />
              <span>«{preview(u.text)}»</span>
            </button>
          </div>
        ))}
        <FormStatus error={error} done={false} doneText="" />
      </div>
    </details>
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

// Аватар спикера: инициалы человека или метка ASR («C»). Инициалы — то же
// правило, что у аватара каркаса (app-shell.tsx)
function speakerBadge(u: Utterance): string {
  if (!u.speaker_name) return u.speaker_label;
  const letters = u.speaker_name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((w) => Array.from(w)[0] ?? "")
    .join("");
  return letters.toUpperCase() || u.speaker_label;
}

// «23 реплики»: число реплик в шапке транскрипта
function replicas(n: number): string {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return `${n} реплика`;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return `${n} реплики`;
  return `${n} реплик`;
}

function preview(text: string): string {
  return text.length > 60 ? `${text.slice(0, 60)}…` : text;
}
