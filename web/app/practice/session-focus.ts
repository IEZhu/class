import { useEffect } from "react";
import type { RefObject } from "react";

// useSessionFocus — фокус в сессии практики: горячие клавиши карточек и
// cloze действуют, только пока он внутри неё (lib/practice-keys, WCAG
// 2.1.4). С начала сессии и после каждого шага (step: переворот, оценка,
// ответ, «Дальше») фокус вне сессии — на <body>, когда нажатая кнопка
// исчезла, или там, где он был до клика мышью, — переходит на саму
// сессию (tabIndex=-1). Фокус в поле ввода или на кнопке внутри сессии
// остаётся на месте.
export function useSessionFocus(ref: RefObject<HTMLElement | null>, step: unknown) {
  useEffect(() => {
    const session = ref.current;
    if (session && !session.contains(document.activeElement)) session.focus({ preventScroll: true });
  }, [ref, step]);
}
