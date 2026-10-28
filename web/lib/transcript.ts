// Транскрипт на странице урока (S2-4, S2-5): подписи числа реплик и правки
// преподавателя поверх перечитанного транскрипта. Чистые функции без React
// и DOM — их проверяют тесты lib/transcript.test.ts (npm test).

import type { Transcript, Utterance } from "./api";

// Метка спикера, сопоставленная с человеком урока (ответ PUT
// /lessons/{id}/transcript/speakers/{label})
export type SpeakerLink = Pick<Utterance, "speaker_label" | "user_id" | "speaker_name">;

// Форма слова при числе n: forms — для 1, 2 и 5 («реплика, реплики, реплик»)
function plural(n: number, forms: readonly [string, string, string]): string {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return forms[0];
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return forms[1];
  return forms[2];
}

// «23 реплики» — число реплик в шапке транскрипта
export function replicas(n: number): string {
  return `${n} ${plural(n, ["реплика", "реплики", "реплик"])}`;
}

// «40 из 70 реплик» — шапка, пока показано только начало транскрипта
export function replicasOf(shown: number, total: number): string {
  return `${shown} из ${total} ${plural(total, ["реплики", "реплик", "реплик"])}`;
}

// «ещё 30 реплик» — сколько реплик панель ещё дочитывает
export function moreReplicas(n: number): string {
  return `ещё ${n} ${plural(n, ["реплику", "реплики", "реплик"])}`;
}

// Что преподаватель сохранил с открытия страницы: реплики по idx (ответ
// PATCH правки текста) и сопоставленные метки спикеров
export interface LocalChanges {
  utterances: Map<number, Utterance>;
  speakers: Map<string, SpeakerLink>;
}

// withLocalChanges — перечитанный транскрипт t с сохранёнными правками
// поверх: перечитывание могло уйти из api раньше правки и вернуть старый
// текст или метку. Сохранённое — ответ api на саму правку, оно не старше.
// Метка применяется после текста: правку текста могли сохранить до неё.
export function withLocalChanges(t: Transcript, local: LocalChanges): Transcript {
  if (local.utterances.size === 0 && local.speakers.size === 0) return t;
  return {
    ...t,
    utterances: t.utterances.map((u) => {
      const it = local.utterances.get(u.idx) ?? u;
      const s = local.speakers.get(it.speaker_label);
      return s ? { ...it, user_id: s.user_id, speaker_name: s.speaker_name } : it;
    }),
  };
}
