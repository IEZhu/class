"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { FormEvent } from "react";

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
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 360, margin: "6rem auto", padding: "0 1rem" }}>
      <h1>Lingua Class</h1>
      <form onSubmit={submit} style={{ display: "grid", gap: "0.75rem" }}>
        <label>
          Email
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoComplete="email"
            style={{ width: "100%", padding: "0.5rem" }}
          />
        </label>
        <label>
          Пароль
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete="current-password"
            style={{ width: "100%", padding: "0.5rem" }}
          />
        </label>
        <button type="submit" disabled={busy} style={{ padding: "0.6rem" }}>
          {busy ? "Входим…" : "Войти"}
        </button>
        {error && <p style={{ color: "crimson" }}>{error}</p>}
      </form>
    </main>
  );
}
