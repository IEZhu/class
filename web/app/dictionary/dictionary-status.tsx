"use client";

import { useRouter } from "next/navigation";

import type { TermStatus } from "../../lib/term-status";
import TermStatusControl from "../term-status-control";

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
