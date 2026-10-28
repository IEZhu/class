import type { ReactNode } from "react";

// Kbd — подсказка клавиши рядом с кнопкой (DESIGN §3.22). На сенсорных
// экранах без клавиатуры CSS её прячет; горячие клавиши от этого не меняются.
export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="kbd">{children}</kbd>;
}
