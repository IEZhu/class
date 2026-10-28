"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { ReactNode } from "react";

import type { TermStatus } from "../../lib/term-status";
import TermStatusControl, { confirmRemove, removeTerm } from "../term-status-control";
import { cx } from "../ui/cx";
import { Icon } from "../ui/icons";
import styles from "./page.module.css";

// Статус слова в строке словаря — компактный (DESIGN §6.5): после
// сохранения страница перечитывает список — слово могло выпасть из
// фильтра или исчезнуть.
export default function DictionaryStatus({
  studentId,
  termId,
  lemma,
  status,
}: {
  studentId: number;
  termId: number;
  lemma: string;
  status: TermStatus;
}) {
  const router = useRouter();
  return <TermStatusControl studentId={studentId} termId={termId} lemma={lemma} status={status} onSaved={() => router.refresh()} compact />;
}

// DictionaryRow — строка словаря (cells — её колонки до «Убрать») и
// «Убрать» в её конце: приглушённая кнопка со значком, вдали от статусов
// (DESIGN §4.4), с подтверждением (confirmRemove): вместе со словом уходит
// его график повторений, вернуть его нечем. Сбой удаления — отдельной
// строкой под словом во всю ширину таблицы: в узкой колонке «Убрать»
// сообщение сжималось в столбик. После удаления список перечитывается.
export function DictionaryRow({
  studentId,
  termId,
  lemma,
  children: cells,
}: {
  studentId: number;
  termId: number;
  lemma: string;
  children: ReactNode;
}) {
  const router = useRouter();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function remove() {
    if (busy || !confirmRemove(lemma)) return;
    setBusy(true);
    setError(null);
    try {
      await removeTerm(studentId, termId, lemma);
      router.refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "не удалось убрать слово");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <tr className={cx(error && styles.failed)}>
        {cells}
        <td className={styles.remove}>
          <button
            type="button"
            className="icon-btn"
            aria-label={`Убрать ${lemma} из словаря`}
            title="Убрать из словаря"
            aria-disabled={busy || undefined}
            onClick={() => void remove()}
          >
            <Icon name="x" />
          </button>
        </td>
      </tr>
      {error && (
        <tr className={styles.removeError}>
          {/* Все пять колонок таблицы (page.tsx) */}
          <td colSpan={5}>
            <p className="field-error" role="alert">
              <Icon name="error" />
              <span>
                Не удалось убрать «<span lang="en">{lemma}</span>»: {error}
              </span>
            </p>
          </td>
        </tr>
      )}
    </>
  );
}
