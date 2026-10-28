"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { FormEvent } from "react";

import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Field } from "../ui/field";

// Вход (DESIGN §6.1): карточка под логотипом, который рисует layout.
// Ошибка — плашка над кнопкой с role="alert".
export default function LoginPage() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password }),
      });
      if (res.ok) {
        router.push("/lessons");
        router.refresh();
        return;
      }
      const body = (await res.json().catch(() => null)) as { error?: string } | null;
      setError(body?.error ?? "не удалось войти");
    } catch {
      setError("сеть недоступна, попробуйте ещё раз");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="auth-page">
      <div className="card card-pad">
        <h1>Вход</h1>
        <form onSubmit={submit} className="stack">
          <Field label="Email">
            <input
              className="input"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              autoComplete="email"
            />
          </Field>
          <Field label="Пароль">
            <input
              className="input"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              autoComplete="current-password"
            />
          </Field>
          {error && (
            <Alert tone="danger">
              {/* Текст api начинается со строчной — заглавную даёт CSS FormStatus */}
              <span className="form-status-text">{error}</span>
            </Alert>
          )}
          <Button type="submit" variant="primary" size="lg" block disabled={busy} busy={busy}>
            {busy ? "Входим…" : "Войти"}
          </Button>
        </form>
      </div>
    </main>
  );
}
