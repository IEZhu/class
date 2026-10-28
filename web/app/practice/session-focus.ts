import { useEffect } from "react";
import type { RefObject } from "react";

// Щелчок по ним — их собственный: фокус ставит (или нарочно не ставит)
// сам элемент. Слова текста и сессия — с tabindex, попап — role=dialog
const OWN_CLICK_TARGETS = "a, button, input, select, textarea, label, summary, [contenteditable], [tabindex], [role=dialog]";

// focusLost — фокус ни на чём: на <body> — нажатая кнопка исчезла или на
// время запроса стала disabled, щелчок пришёлся на пустое место
function focusLost(): boolean {
  return document.activeElement === null || document.activeElement === document.body;
}

// useSessionFocus — фокус в сессии практики: горячие клавиши карточек и
// cloze действуют, только пока он внутри неё (lib/practice-keys, WCAG
// 2.1.4). Фокус переходит на саму сессию (tabIndex=-1), только когда он
// потерян:
// - с начала сессии и после каждого шага (step: переворот, оценка, ответ,
//   «Дальше», конец запроса, и неудачного тоже);
// - после щелчка по пустому месту страницы — поле страницы, боковая
//   панель, мимо попапа слова: браузер снимает фокус на <body> после
//   pointerdown, поэтому проверка — по click.
// Фокус, поставленный человеком, — поле ввода, кнопка, ссылка меню, — не
// трогается, даже если шаг закончился, пока он был там.
export function useSessionFocus(ref: RefObject<HTMLElement | null>, step: unknown) {
  useEffect(() => {
    if (focusLost()) ref.current?.focus({ preventScroll: true });
  }, [ref, step]);

  useEffect(() => {
    function onClick(e: MouseEvent) {
      if (e.target instanceof Element && e.target.closest(OWN_CLICK_TARGETS)) return;
      if (focusLost()) ref.current?.focus({ preventScroll: true });
    }
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, [ref]);
}
