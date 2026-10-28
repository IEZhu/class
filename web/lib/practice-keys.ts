// Горячие клавиши практики — карточки (/practice/cards) и «вставь слово»
// (/practice/cloze) слушают всё окно. ownsKey решает, чьё нажатие:
// элемента в фокусе или страницы. Чистый TS без React и DOM — его
// проверяют тесты lib/practice-keys.test.ts (npm test).

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
