"use client";

import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

import { STATUS_LABELS, TERM_STATUSES, TermStatuses } from "../lib/term-status";
import type { LookupResult, TermStatus } from "../lib/term-status";
import { request, requestJson } from "./forms";
import styles from "./term-status-control.module.css";
import { Button } from "./ui/button";
import { cx } from "./ui/cx";
import { Icon } from "./ui/icons";

// termStatuses — статусы слов зрителя-студента, один кэш на вкладку: его
// читают все <ClickableText> страницы, а сохранение статуса — здесь —
// сразу перекрашивает формы слова и на других страницах без перезагрузки.
// Вход другого студента в той же вкладке кэш сбрасывает (lib/term-status).
export const termStatuses = new TermStatuses((words) =>
  requestJson<LookupResult>("/api/user-terms/lookup", "POST", { words }),
);

// removeTerm — убрать слово из словаря студента: DELETE /user-terms/{id}
// (вместе со словом уходит его график повторений) и кэш раскраски
export async function removeTerm(studentId: number, termId: number, lemma: string) {
  await request(`/api/user-terms/${termId}`, "DELETE");
  termStatuses.setStatus(studentId, lemma, null);
}

// confirmRemove — подтверждение «Убрать» (DESIGN §4.4), одно для попапа
// слова и строки словаря: вместе со словом уходит его график повторений,
// вернуть его нечем
export function confirmRemove(lemma: string): boolean {
  return window.confirm(`Убрать «${lemma}» из словаря? Статус и график повторений слова удалятся.`);
}

// TermStatusControl — «в мой словарь», статус 1..5 и «убрать» (S3-5):
// в попапе слова и на странице словаря. studentId — зритель, чей это
// словарь (ключ кэша termStatuses); lessonId — урок, с которого слово
// пришло (source_lesson_id); api запоминает первый.
//
// Вид (DESIGN §4.2, §6.5): в попапе — сегменты с номером и словом статуса
// и строка действий (actions — слева, «Убрать» — справа, отдельно от
// основного действия); compact — строка словаря: номера 32px и слово
// статуса рядом, у кнопок оно в доступном имени. «Убрать» — с
// подтверждением (confirmRemove); в строке словаря — в конце строки
// (dictionary-status.tsx), а не рядом со статусами.
export default function TermStatusControl({
  studentId,
  termId,
  lemma,
  status,
  lessonId,
  onSaved,
  compact = false,
  actions,
}: {
  studentId: number;
  termId: number;
  lemma: string;
  status: TermStatus | null;
  lessonId?: number | null;
  onSaved?: () => void;
  compact?: boolean;
  actions?: ReactNode;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const box = useRef<HTMLDivElement>(null);
  // Фокус был внутри, когда статус сохраняли: «Убрать» и «В мой словарь»
  // заменяют кнопки, и фокус переходит на новые, а не теряется
  const refocus = useRef(false);

  useEffect(() => {
    if (!refocus.current) return;
    refocus.current = false;
    box.current?.querySelector<HTMLElement>('[aria-pressed="true"], [data-add]')?.focus();
  }, [status]);

  async function save(next: TermStatus | null) {
    // Пока идёт сохранение, кнопки помечены aria-disabled, а не disabled:
    // disabled снял бы фокус с нажатой кнопки
    if (busy) return;
    setBusy(true);
    setError(null);
    refocus.current = box.current?.contains(document.activeElement) ?? false;
    try {
      if (next === null) {
        await removeTerm(studentId, termId, lemma);
      } else {
        await request("/api/user-terms", "POST", { term_id: termId, status: next, source_lesson_id: lessonId ?? undefined });
        termStatuses.setStatus(studentId, lemma, next);
      }
      onSaved?.();
    } catch (e) {
      refocus.current = false;
      setError(e instanceof Error ? e.message : "не удалось сохранить");
    } finally {
      setBusy(false);
    }
  }

  function remove() {
    if (!busy && confirmRemove(lemma)) void save(null);
  }

  const errorText = error && (
    <p className="field-error" role="alert">
      <Icon name="error" />
      <span>{error}</span>
    </p>
  );

  if (status === null) {
    return (
      <div ref={box} className={styles.control} aria-busy={busy || undefined}>
        <div className="row">
          <Button type="button" variant="primary" size="sm" data-add aria-disabled={busy || undefined} onClick={() => void save(1)}>
            <Icon name="plus" />В мой словарь
          </Button>
          {actions}
        </div>
        {errorText}
      </div>
    );
  }

  return (
    <div ref={box} className={cx(styles.control, compact && styles.compact)} aria-busy={busy || undefined}>
      <div className={compact ? styles.compactRow : undefined}>
        <div className={styles.seg} role="group" aria-label={`Статус слова ${lemma}`}>
          {TERM_STATUSES.map((s) => (
            <button
              key={s}
              type="button"
              aria-pressed={s === status}
              aria-disabled={busy || undefined}
              title={compact ? STATUS_LABELS[s] : undefined}
              onClick={() => s !== status && void save(s)}
            >
              <span>{s}</span>
              <small className={compact ? "sr-only" : undefined}>{STATUS_LABELS[s]}</small>
            </button>
          ))}
        </div>
        {compact && <span className={styles.current}>{STATUS_LABELS[status]}</span>}
      </div>
      {!compact && (
        <div className="row">
          {actions}
          <span className="spacer" />
          <Button
            type="button"
            variant="danger-ghost"
            size="sm"
            aria-label={`Убрать ${lemma} из словаря`}
            aria-disabled={busy || undefined}
            onClick={remove}
          >
            Убрать
          </Button>
        </div>
      )}
      {errorText}
    </div>
  );
}
