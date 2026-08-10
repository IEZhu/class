import type { Lesson } from "./api";

// Подпись урока: у открытого события своё название, у обычного — имя
// группы (ADR-010). Фоллбэк по group_id остаётся защитой от пустой строки.
export function lessonTitle(l: Pick<Lesson, "title" | "group_name" | "group_id">): string {
  if (l.title) return l.title;
  if (l.group_name) return l.group_name;
  return l.group_id ? `Группа #${l.group_id}` : "Событие";
}
