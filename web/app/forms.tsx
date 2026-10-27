"use client";

import { useState } from "react";
import type { FormEvent, ReactNode } from "react";

import { Icon } from "./ui/icons";

// Общий каркас форм кабинетов (/account, /admin): вызов api, состояние
// отправки и текст ошибки. Держим в одном месте, чтобы формы не
// расползались собственными try/catch.

// request — вызовы api из браузера идут на относительный /api (через caddy),
// в отличие от Server Components (web/lib/api.ts). Текст ошибки берём из
// тела {error}, как его отдаёт api (04-api.md).
export async function request(path: string, method: string, body?: unknown): Promise<void> {
  await call(path, method, body);
}

// requestJson — когда ответ нужен целиком: например выпущенная ссылка
// приглашения, которую api отдаёт один раз (ADR-008).
export async function requestJson<T>(path: string, method: string, body?: unknown): Promise<T> {
  const res = await call(path, method, body);
  return (await res.json()) as T;
}

// RequestError — api ответил не 2xx. Код нужен, когда ответ — обычное
// состояние, а не сбой: например 404 у урока без транскрипта.
export class RequestError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function call(path: string, method: string, body?: unknown): Promise<Response> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new Error("сеть недоступна, попробуйте ещё раз");
  }
  if (!res.ok) {
    const parsed = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new RequestError(res.status, parsed?.error ?? `ошибка ${res.status}`);
  }
  return res;
}

export function useSubmit(action: () => Promise<void>) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setDone(false);
    try {
      await action();
      setDone(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "не удалось сохранить");
    } finally {
      setBusy(false);
    }
  }

  return { busy, error, done, submit };
}

// FormStatus — итог отправки рядом с кнопкой (DESIGN §3.15): ошибка —
// role="alert", успех — role="status", чтобы его прочла программа
// экранного доступа; иконка рядом, цвет — не единственный признак.
// <span>, а не <p>: стоит и внутри строк и абзацев форм.
export function FormStatus({
  error,
  done,
  doneText,
}: {
  error: string | null;
  done: boolean;
  doneText: string;
}): ReactNode {
  if (error)
    return (
      <span className="form-status form-status--error" role="alert">
        <Icon name="error" size="sm" />
        <span className="form-status-text">{error}</span>
      </span>
    );
  if (done)
    return (
      <span className="form-status form-status--done" role="status">
        <Icon name="check" size="sm" />
        <span className="form-status-text">{doneText}</span>
      </span>
    );
  return null;
}
