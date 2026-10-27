// Карточки повторения /practice/cards (S4-3, ADR-036): оценки, клавиши,
// ключ оценки и подписи интервалов. Чистый TS без React и fetch — его
// проверяют тесты lib/review-session.test.ts (npm test).

// Оценка карточки — как её принимает POST /reviews
export type Rating = "again" | "hard" | "good" | "easy";

export interface RatingOption {
  rating: Rating;
  // key — клавиша оценки, она же подсказка на кнопке
  key: string;
  label: string;
}

export const RATINGS: readonly RatingOption[] = [
  { rating: "again", key: "1", label: "Снова" },
  { rating: "hard", key: "2", label: "Трудно" },
  { rating: "good", key: "3", label: "Хорошо" },
  { rating: "easy", key: "4", label: "Легко" },
];

export type KeyAction = { kind: "flip" } | { kind: "rate"; rating: Rating } | null;

// keyAction — что делает клавиша: пробел и Enter переворачивают карточку,
// 1–4 оценивают перевёрнутую; оценить, не посмотрев оборот, нельзя.
export function keyAction(key: string, flipped: boolean): KeyAction {
  if (!flipped) return key === " " || key === "Enter" ? { kind: "flip" } : null;
  const option = RATINGS.find((o) => o.key === key);
  return option ? { kind: "rate", rating: option.rating } : null;
}

// reviewId — uuid v4 из 16 случайных байт: review_id оценки. Ключ
// выдумывается на показ карточки и переживает повтор запроса после
// сбоя сети — api засчитает оценку один раз. crypto.randomUUID есть
// только в защищённом контексте, getRandomValues — везде.
export function reviewId(bytes: Uint8Array): string {
  if (bytes.length !== 16) throw new Error("reviewId: нужно 16 байт");
  const b = Uint8Array.from(bytes);
  b[6] = (b[6] & 0x0f) | 0x40; // версия 4
  b[8] = (b[8] & 0x3f) | 0x80; // вариант RFC 9562
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

// delayLabel — через сколько следующий показ: минуты на шагах обучения,
// часы, дни; сокращения вместо склонений («5 дн.» и «21 дн.» читаются
// одинаково).
export function delayLabel(ms: number): string {
  if (ms < MINUTE) return "меньше чем через минуту";
  if (ms < HOUR) return `через ${Math.round(ms / MINUTE)} мин`;
  if (ms < DAY) return `через ${Math.round(ms / HOUR)} ч`;
  return `через ${Math.round(ms / DAY)} дн.`;
}
