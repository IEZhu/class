import type { Metadata, Viewport } from "next";
import Link from "next/link";
import type { ReactNode } from "react";

import "./globals.css";

// Заголовок вкладки — «Страница · Lingua Class» (WCAG 2.4.2): каждая
// страница экспортирует свой title.
export const metadata: Metadata = {
  title: { default: "Lingua Class", template: "%s · Lingua Class" },
  description: "Платформа занятий английским: уроки, записи, словарь, практика",
};

// Цвет панели браузера — фон верхней панели каркаса в каждой теме
export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  colorScheme: "light dark",
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#ffffff" },
    { media: "(prefers-color-scheme: dark)", color: "#121726" },
  ],
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="ru">
      <body>
        {/* Ссылка ведёт на конец навигации каркаса; без каркаса её нет */}
        <a className="skip-link" href="#content">
          К содержимому
        </a>
        {/* Логотип страниц без каркаса (вход, приглашение, гостевой вход,
            404): CSS прячет его, когда на странице есть каркас */}
        <header className="auth-top">
          <Link href="/lessons" className="brand">
            <span className="brand-mark" aria-hidden="true">
              L
            </span>
            <span>Lingua Class</span>
          </Link>
        </header>
        {children}
      </body>
    </html>
  );
}
