import { cache } from "react";

import { apiFetch } from "../../../lib/api";
import type { LessonDetail, User } from "../../../lib/api";

// Запросы страницы урока через cache(): заголовок вкладки
// (generateMetadata), страница и её not-found за один рендер получают один
// ответ api, а не делают каждый свой запрос.
export const getMe = cache(() => apiFetch<User>("/auth/me"));
export const getLesson = cache((id: string) => apiFetch<LessonDetail>(`/lessons/${encodeURIComponent(id)}`));
