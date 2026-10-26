// Серверный клиент к api: страницы (Server Components) ходят на api напрямую
// внутри compose-сети (INTERNAL_API_URL), пробрасывая cookie запроса.
// Клиентские компоненты используют относительный /api (через caddy).
import { cookies } from "next/headers";

import type { Role } from "./roles";
import type { TermStatus } from "./term-status";

const API_BASE = process.env.INTERNAL_API_URL ?? "http://api:8080";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

export interface User {
  id: number;
  email: string;
  // admin заводит людей и тасует группы, teacher владеет уроками (ADR-007)
  role: Role;
  name: string;
}

// Строка списка людей в админ-кабинете: учётка + названия групп.
export interface UserWithGroups extends User {
  groups: string[];
  // null у действующей учётки, момент архивации — у архивной (ADR-024)
  deactivated_at: string | null;
}

export interface Group {
  id: number;
  name: string;
  level: string;
  // archived — учётка в архиве: из группы не убрана, в новые уроки не попадает
  members: { user_id: number; email: string; name: string; role: Role; archived: boolean }[];
}

// Приглашение (ADR-008). url приходит только в ответе на создание —
// в БД лежит лишь хэш токена, показать ссылку повторно нельзя.
export interface Invite {
  id: number;
  email: string;
  name: string;
  role: Role;
  group_name: string;
  inviter_name: string;
  expires_at: string;
  url?: string;
}

export interface Lesson {
  id: number;
  // null у открытого события — группы у него нет (ADR-010)
  group_id: number | null;
  // Название события; у обычного урока пусто, подпись берётся из группы
  title: string;
  teacher_id: number;
  starts_at: string;
  ends_at: string;
  status: "scheduled" | "live" | "processing" | "done";
  // в GET /lessons и GET /lessons/{id} заполнен всегда (NOT NULL join)
  group_name: string;
}

// Файл материала в хранилище (ADR-024). ready=false — загрузка не
// подтверждена; такой файл api показывает только преподавателю урока.
export interface MaterialFile {
  name: string;
  content_type: string;
  size: number;
  ready: boolean;
}

export interface Material {
  id: number;
  lesson_id: number;
  kind: "material" | "homework";
  title: string;
  body_md: string;
  created_at: string;
  // null у текстового материала
  file: MaterialFile | null;
}

export interface LessonDetail extends Lesson {
  teacher_name: string;
  materials: Material[];
  participants: { user_id: number; email: string; name: string; role: string }[];
}

// Google Calendar преподавателя (S1-1, ADR-023): available — ключи Google
// на стенде заданы, connected — токен сохранён и читается текущим ключом.
export interface GoogleCalendarStatus {
  available: boolean;
  connected: boolean;
}

// Ответ POST /terms/click (S3-2) — содержимое попапа <ClickableText>.
export interface TermClick {
  term_id: number;
  lemma: string;
  surface: string;
  // null — перевода нет в кэше, а LibreTranslate не настроен или не ответил
  translation: { text: string; source: string } | null;
  definitions: { word: string; pos: string; glosses: string[]; translations_ru: string[] }[];
  // Примеры Tatoeba с переводом (S5-4, долг D-1); до них — пустой список
  examples: { text: string; translation: string | null }[];
  // Статус в личном словаре студента (S3-5); null — слова там нет
  status: TermStatus | null;
}

// Слово личного словаря (GET/POST /user-terms, S3-5, ADR-027).
export interface UserTerm {
  term_id: number;
  lemma: string;
  surface: string;
  translation: string | null;
  status: TermStatus;
  // Урок, на котором слово добавили; null — вне урока или урок отменён
  source_lesson: (Pick<Lesson, "id" | "title" | "group_id" | "group_name" | "starts_at">) | null;
  added_at: string;
  updated_at: string;
}

export interface UserTermPage {
  items: UserTerm[];
  // null — страница последняя
  next_cursor: string | null;
}

const API_TIMEOUT_MS = 5000;

export async function apiFetch<T>(path: string): Promise<T> {
  const cookieStore = await cookies();
  // во внутренний api уходит только сессионный cookie (ADR-006),
  // остальные cookie запроса ему не нужны
  const sid = cookieStore.get("sid");
  const cookieHeader = sid ? `sid=${sid.value}` : "";

  // таймаут: зависший api не должен держать рендер страницы бесконечно
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), API_TIMEOUT_MS);
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      headers: cookieHeader ? { cookie: cookieHeader } : undefined,
      cache: "no-store",
      signal: controller.signal,
    });
  } finally {
    clearTimeout(timeoutId);
  }
  if (!res.ok) {
    let message = `api: ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // тело не JSON — оставляем статусное сообщение
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}
