"use client";

import { useRouter } from "next/navigation";

import type { TermStatus } from "../../lib/term-status";
import TermStatusControl from "../term-status-control";

// Статус слова в строке словаря: после сохранения страница перечитывает
// список — слово могло выпасть из фильтра или исчезнуть.
export default function DictionaryStatus({ termId, lemma, status }: { termId: number; lemma: string; status: TermStatus }) {
  const router = useRouter();
  return <TermStatusControl termId={termId} lemma={lemma} status={status} onSaved={() => router.refresh()} />;
}
