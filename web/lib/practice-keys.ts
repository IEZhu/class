// Горячие клавиши практики — карточки (/practice/cards) и «вставь слово»
// (/practice/cloze). isSessionKey решает, чьё нажатие: страницы или
// элемента в фокусе. Чистый TS без React и DOM — его проверяют тесты
// lib/practice-keys.test.ts (npm test).

// Сессия практики — контейнер <section {...SESSION_PROPS} tabIndex={-1}>:
// фокус стоит в нём с начала сессии и возвращается в него, когда потерян
// (app/practice/session-focus.ts). SESSION — его селектор: по нему же
// попап слова отдаёт фокус сессии (lib/word-nav closeFocusTarget)
export const SESSION_PROPS = { "data-practice-session": "" } as const;
export const SESSION = "[data-practice-session]";
// Поля ввода и диалоги (попап слова): все их клавиши — их собственные
const TYPING_TARGETS = "input, textarea, select, [contenteditable], [role=dialog]";
// Слово <ClickableText>: data-i у него всегда, с клавиатурным доступом
// (DESIGN §5.2) ещё класс .w и role=button — пробел и Enter открывают
// попап, стрелки ходят по словам; страница их не перехватывает
const WORD_TARGETS = "[data-i], .w[role=button]";
// Кнопки и ссылки: пробел и Enter на них — их собственное нажатие
const ACTIVATION_TARGETS = "button, a";

// Минимум Element, нужный проверке
export interface KeyTarget {
  closest(selectors: string): unknown;
}

// ownsKey — нажатие key принадлежит цели события, а не странице:
// обработчик практики его пропускает.
export function ownsKey(target: KeyTarget | null, key: string): boolean {
  if (!target) return false;
  if (target.closest(`${TYPING_TARGETS}, ${WORD_TARGETS}`)) return true;
  return (key === " " || key === "Enter") && target.closest(ACTIVATION_TARGETS) !== null;
}

// isSessionKey — нажатие key — горячая клавиша практики: фокус внутри
// сессии, и цель нажатие себе не забирает (ownsKey). Односимвольные
// клавиши (пробел, 1–4) действуют, только пока фокус в сессии (WCAG
// 2.1.4): цифра на ссылке меню или без фокуса ничего не оценивает.
export function isSessionKey(target: KeyTarget | null, key: string): boolean {
  if (!target || !target.closest(SESSION)) return false;
  return !ownsKey(target, key);
}
