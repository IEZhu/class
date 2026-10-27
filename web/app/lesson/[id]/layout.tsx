import type { ReactNode } from "react";

import { apiFetch } from "../../../lib/api";
import type { User } from "../../../lib/api";
import { ClickableTextScope } from "../../clickable-text";

// Контекст текстов урока для <ClickableText> (S3-1, S3-5): клик уходит
// с lesson_id, раскраска и «в мой словарь» — у студента. Обёртка в
// layout, а не в странице: под неё попадает всё, что страница урока
// показывает (материалы, транскрипт). Вход и доступ проверяет страница;
// здесь сбой /auth/me — просто текст без словаря.
export default async function LessonLayout({ children, params }: { children: ReactNode; params: Promise<{ id: string }> }) {
  const { id } = await params;
  const me = await apiFetch<User>("/auth/me").catch(() => null);
  const lessonId = Number(id);
  return (
    <ClickableTextScope
      lessonId={Number.isSafeInteger(lessonId) && lessonId > 0 ? lessonId : null}
      studentId={me?.role === "student" ? me.id : null}
    >
      {children}
    </ClickableTextScope>
  );
}
