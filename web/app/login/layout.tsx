import type { Metadata } from "next";
import type { ReactNode } from "react";

// Страница входа — клиентский компонент и metadata экспортировать не
// может; заголовок вкладки задаёт её сегмент
export const metadata: Metadata = { title: "Вход" };

export default function LoginLayout({ children }: { children: ReactNode }) {
  return children;
}
