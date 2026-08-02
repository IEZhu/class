// Серверный клиент к api: страницы (Server Components) ходят на api напрямую
// внутри compose-сети (INTERNAL_API_URL), пробрасывая cookie запроса.
// Клиентские компоненты используют относительный /api (через caddy).
import { cookies } from "next/headers";

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
  role: "teacher" | "student";
  name: string;
}

export interface Lesson {
  id: number;
  group_id: number;
  teacher_id: number;
  starts_at: string;
  ends_at: string;
  status: "scheduled" | "live" | "processing" | "done";
  group_name?: string;
}

export interface Material {
  id: number;
  lesson_id: number;
  kind: "material" | "homework";
  title: string;
  body_md: string;
  created_at: string;
}

export interface LessonDetail extends Lesson {
  teacher_name: string;
  materials: Material[];
  participants: { user_id: number; email: string; name: string; role: string }[];
}

export async function apiFetch<T>(path: string): Promise<T> {
  const cookieStore = await cookies();
  const cookieHeader = cookieStore
    .getAll()
    .map((c) => `${c.name}=${c.value}`)
    .join("; ");

  const res = await fetch(`${API_BASE}${path}`, {
    headers: cookieHeader ? { cookie: cookieHeader } : undefined,
    cache: "no-store",
  });
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
