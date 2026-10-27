import type { ReactNode } from "react";

import { cx } from "./cx";

// Свой набор иконок (DESIGN §3.27): контур 24×24, штрих 1.8, цвет —
// currentColor. Иконка всегда декоративна (aria-hidden): смысл несёт текст
// рядом или aria-label кнопки. Без пакетов иконок и CDN (ADR-040). Файл —
// icons.tsx, не icon.tsx: icon.tsx в app/ Next считает иконкой сайта.
const glyphs = {
  lessons: (
    <>
      <rect x="3" y="4.5" width="18" height="16" rx="3" />
      <path d="M8 2.5v4M16 2.5v4M3 9.5h18" />
    </>
  ),
  dict: (
    <>
      <path d="M5 4h11a3 3 0 0 1 3 3v13H8a3 3 0 0 1-3-3z" />
      <path d="M5 17a3 3 0 0 1 3-3h11" />
    </>
  ),
  cards: (
    <>
      <rect x="7" y="3" width="13" height="15" rx="2.5" />
      <path d="M4 7v11.5A2.5 2.5 0 0 0 6.5 21H15" />
    </>
  ),
  cloze: (
    <>
      <rect x="3" y="6" width="18" height="12" rx="3" />
      <path d="M8 12h8" strokeDasharray="2 2.2" />
    </>
  ),
  text: (
    <>
      <path d="M7 3h7l5 5v13H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z" />
      <path d="M14 3v5h5M9 13h7M9 17h5" />
    </>
  ),
  video: (
    <>
      <rect x="3" y="5" width="18" height="14" rx="3" />
      <path d="M10 9.5v5l4.5-2.5z" />
    </>
  ),
  people: (
    <>
      <circle cx="9" cy="8" r="3.5" />
      <path d="M2.5 20a6.5 6.5 0 0 1 13 0" />
      <path d="M15.5 4.6a3.5 3.5 0 0 1 0 6.8M18 14.2a6.5 6.5 0 0 1 3.5 5.8" />
    </>
  ),
  user: (
    <>
      <circle cx="12" cy="8" r="4" />
      <path d="M4 21a8 8 0 0 1 16 0" />
    </>
  ),
  logout: (
    <>
      <path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3" />
      <path d="M10 8l-4 4 4 4M6 12h10" />
    </>
  ),
  more: (
    <>
      <circle cx="5" cy="12" r="1.4" />
      <circle cx="12" cy="12" r="1.4" />
      <circle cx="19" cy="12" r="1.4" />
    </>
  ),
  chev: <path d="M9 6l6 6-6 6" />,
  back: <path d="M15 6l-6 6 6 6" />,
  down: <path d="M6 9l6 6 6-6" />,
  play: <path d="M7 4.5v15l13-7.5z" />,
  x: <path d="M6 6l12 12M18 6L6 18" />,
  check: <path d="M5 12.5l4.5 4.5L19 7.5" />,
  spark: <path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z" />,
  edit: <path d="M4 20h4L19 9l-4-4L4 16z" />,
  download: <path d="M12 4v11M7 10l5 5 5-5M5 20h14" />,
  clock: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </>
  ),
  plus: <path d="M12 5v14M5 12h14" />,
  cam: (
    <>
      <rect x="3" y="6" width="13" height="12" rx="2.5" />
      <path d="M16 10.5l5-3v9l-5-3" />
    </>
  ),
  volume: (
    <>
      <path d="M4 9h4l5-4v14l-5-4H4z" />
      <path d="M17 9a4 4 0 0 1 0 6" />
    </>
  ),
  full: <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />,
  file: (
    <>
      <path d="M7 3h7l5 5v13H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z" />
      <path d="M14 3v5h5" />
    </>
  ),
  copy: (
    <>
      <rect x="8" y="8" width="12" height="12" rx="2.5" />
      <path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" />
    </>
  ),
  search: (
    <>
      <circle cx="11" cy="11" r="6.5" />
      <path d="M16 16l4.5 4.5" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11v5.5M12 7.6v.1" />
    </>
  ),
  warning: (
    <>
      <path d="M12 3.5l9.5 16.5h-19z" />
      <path d="M12 10v4.5M12 17.4v.1" />
    </>
  ),
  error: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7.5V13M12 16.4v.1" />
    </>
  ),
} satisfies Record<string, ReactNode>;

export type IconName = keyof typeof glyphs;

export function Icon({ name, size = "md", className }: { name: IconName; size?: "sm" | "md" | "lg"; className?: string }) {
  return (
    <svg className={cx("i", size !== "md" && `i--${size}`, className)} viewBox="0 0 24 24" aria-hidden="true">
      {glyphs[name]}
    </svg>
  );
}
