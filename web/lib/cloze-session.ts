// Упражнение «вставь слово» /practice/cloze (S4-4, S4-5, ADR-037):
// клавиши, время ответа и подписи итога. Чистый TS без React и fetch —
// его проверяют тесты lib/cloze-session.test.ts (npm test).

// Как отвечают: кнопкой варианта или вводом слова (mode у POST
// /practice/cloze/{id}/attempts; порог «быстро» у ввода выше).
export type ClozeMode = "choice" | "typing";

export type ClozeKeyAction = { kind: "choose"; index: number } | { kind: "next" } | null;

// clozeKeyAction — что делает клавиша вне поля ввода: до ответа в режиме
// вариантов 1…N выбирают вариант, после ответа Enter и пробел ведут
// к следующему пункту. Набор слова — в поле, его клавиши страница не
// перехватывает.
export function clozeKeyAction(key: string, answered: boolean, mode: ClozeMode, options: number): ClozeKeyAction {
  if (answered) return key === "Enter" || key === " " ? { kind: "next" } : null;
  if (mode !== "choice" || !/^[1-9]$/.test(key)) return null;
  const index = Number(key) - 1;
  return index < options ? { kind: "choose", index } : null;
}

// MAX_RESPONSE_MS — потолок response_ms у api: дольше — пункт бросили
// и вернулись
export const MAX_RESPONSE_MS = 600_000;

// responseMs — время ответа от показа пункта, целые миллисекунды
// в границах, которые принимает api.
export function responseMs(shownAt: number, now: number): number {
  return Math.min(MAX_RESPONSE_MS, Math.max(0, Math.round(now - shownAt)));
}

// Итог ответа — поля ClozeAttemptResult, нужные подписи
export interface ClozeVerdict {
  correct: boolean;
  exact: boolean;
  answer: string;
  rating: "again" | "good" | "easy";
  counted: boolean;
}

const RATING_LABELS: Record<ClozeVerdict["rating"], string> = { again: "снова", good: "хорошо", easy: "легко" };

// verdictText — строка итога: верно ли и какое слово стояло в предложении
// (засчитывается любая форма слова, точная форма — подсказкой).
export function verdictText(v: ClozeVerdict): string {
  if (!v.correct) return `Неверно — в предложении «${v.answer}»`;
  return v.exact ? "Верно!" : `Верно — в предложении «${v.answer}»`;
}

// scheduleText — что ответ сделал с графиком повторений: оценка FSRS или
// почему не засчитан (верный ответ раньше показа карточки, ADR-037).
export function scheduleText(v: ClozeVerdict): string {
  if (v.counted) return `в график повторений: ${RATING_LABELS[v.rating]}`;
  return "повторять слово ещё рано — верный ответ в график не засчитан";
}

// sourceLabel — откуда предложение пункта
export function sourceLabel(source: "lesson" | "material" | "generated" | "tatoeba"): string {
  switch (source) {
    case "lesson":
      return "из урока";
    case "material":
      return "из материалов урока";
    case "generated":
      return "из вашего текста";
    case "tatoeba":
      return "Tatoeba";
  }
}

// isAnswerOption — вариант, который был ответом: форма из предложения
// (варианты LLM) или лемма (варианты словаря).
export function isAnswerOption(option: string, result: { answer: string; lemma: string }): boolean {
  return option === result.answer.toLowerCase() || option === result.lemma;
}
