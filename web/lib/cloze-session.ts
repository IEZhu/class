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

// Что на экране для часов ответа: номер пункта, режим и пришли ли его
// варианты (у pending — после POST /practice/cloze/distractors)
export interface ClozeClock {
  index: number;
  mode: ClozeMode;
  ready: boolean;
}

// restartClock — начать ли отсчёт времени ответа заново: показан другой
// пункт, в режиме вариантов у пункта только что появились кнопки или
// студент ушёл с экрана ожидания вариантов во ввод слова. Ожидание
// вариантов LLM — не время ответа: иначе быстрый верный ответ после
// ожидания получил бы good вместо easy (ADR-037). В режиме ввода ответить
// можно и без вариантов — их приход отсчёт не трогает; смена режима, когда
// варианты уже на экране, — не новый показ.
export function restartClock(prev: ClozeClock | null, cur: ClozeClock): boolean {
  if (!prev || prev.index !== cur.index) return true;
  const waited = prev.mode === "choice" && !prev.ready;
  return (cur.mode === "choice" && cur.ready && !prev.ready) || (waited && cur.mode === "typing");
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

// Пункт для набора вариантов — что из него нужно этим помощникам
interface OptionsOf {
  id: number;
  options: string[];
  distractor_source: "list" | "llm" | "pending";
}

// distractorRequestIds — пункты, чьи варианты страница просит у POST
// /practice/cloze/distractors: только pending и только с ключом LLM. Без
// ключа выдача сразу отдаёт варианты всем пунктам, и запроса нет.
export function distractorRequestIds(page: { items: OptionsOf[]; llm_available: boolean }): number[] {
  if (!page.llm_available) return [];
  return page.items.filter((it) => it.distractor_source === "pending").map((it) => it.id);
}

// itemOptions — варианты пункта на экране: из выдачи, а у pending — из
// ответа POST /practice/cloze/distractors; null — ещё не пришли. Набор у
// пункта один (ADR-037): пришедший позже не заменяет уже отданный
// выдачей.
export function itemOptions(item: OptionsOf, fetched: Record<number, OptionsOf>): string[] | null {
  if (item.distractor_source !== "pending") return item.options;
  const got = fetched[item.id];
  return got && got.distractor_source !== "pending" ? got.options : null;
}

const LLM_ERRORS: Record<string, string> = {
  rate_limited: "лимит подсказок LLM на сегодня исчерпан",
  llm_unavailable: "LLM временно недоступен",
  llm_failed: "LLM не справился с запросом",
};

// llmErrorNote — подпись, почему у пунктов варианты словаря, а не LLM.
export function llmErrorNote(code: string | null): string | null {
  if (!code) return null;
  return `варианты из словаря: ${LLM_ERRORS[code] ?? "LLM недоступен"}`;
}

// Подпись предложения Tatoeba (CC BY 2.0 FR): автор и лицензия всегда;
// ссылки на страницу предложения и перевод — только после ответа, их
// присылает попытка: на странице предложение целиком, с ответом.
export interface ClozeCredit {
  author: string;
  url: string | null;
  license: string;
  licenseUrl: string;
  translation: { author: string; url: string } | null;
}

export function clozeCredit(
  before: { author: string; license: string; license_url: string } | null,
  after: {
    author: string;
    url: string;
    translation_author: string;
    translation_url: string;
    license: string;
    license_url: string;
  } | null,
): ClozeCredit | null {
  if (after) {
    return {
      author: after.author,
      url: after.url,
      license: after.license,
      licenseUrl: after.license_url,
      translation: { author: after.translation_author, url: after.translation_url },
    };
  }
  if (!before) return null;
  return { author: before.author, url: null, license: before.license, licenseUrl: before.license_url, translation: null };
}
