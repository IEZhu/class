"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import type { TermStatus } from "../../lib/term-status";
import TermStatusControl, { removeTerm } from "../term-status-control";
import { Icon } from "../ui/icons";

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

// DictionaryRemove — «Убрать» в конце строки словаря: приглушённая кнопка
// со значком, вдали от статусов (DESIGN §4.4), и с подтверждением, как
// удаление в «Людях и группах»: вместе со словом уходит его график
// повторений, вернуть его нечем. После удаления список перечитывается.
export function DictionaryRemove({ studentId, termId, lemma }: { studentId: number; termId: number; lemma: string }) {
  const router = useRouter();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function remove() {
    if (busy || !window.confirm(`Убрать «${lemma}» из словаря? Статус и график повторений слова удалятся.`)) return;
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
      {error && (
        <p className="field-error" role="alert">
          <Icon name="error" />
          <span>{error}</span>
        </p>
      )}
    </>
  );
}
