"use client";

import { useState } from "react";
import type { FormEvent, ReactNode } from "react";

// Общий каркас форм кабинетов (/account, /admin): вызов api, состояние
// отправки и текст ошибки. Держим в одном месте, чтобы формы не
// расползались собственными try/catch.

// request — вызовы api из браузера идут на относительный /api (через caddy),
// в отличие от Server Components (web/lib/api.ts). Текст ошибки берём из
// тела {error}, как его отдаёт api (04-api.md).
export async function request(path: string, method: string, body?: unknown): Promise<void> {
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
    throw new Error(parsed?.error ?? `ошибка ${res.status}`);
  }
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

export function FormStatus({
  error,
  done,
  doneText,
}: {
  error: string | null;
  done: boolean;
  doneText: string;
}): ReactNode {
  if (error) return <span style={{ color: "crimson" }}>{error}</span>;
  if (done) return <span style={{ color: "#2a7" }}>{doneText}</span>;
  return null;
}
