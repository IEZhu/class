"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import { FormStatus, request, useSubmit } from "../../forms";

const fieldStyle = { width: "100%", padding: "0.5rem" };

// Пароль придумывает сам приглашённый — пригласивший его не узнаёт (ADR-008).
export default function AcceptForm({ token }: { token: string }) {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");

  const { busy, error, done, submit } = useSubmit(async () => {
    if (password !== repeat) throw new Error("пароли не совпадают");
    await request(`/api/signup/${encodeURIComponent(token)}`, "POST", { password });
    // accept сразу выдаёт cookie сессии — человек попадает в кабинет залогиненным
    router.push("/lessons");
    router.refresh();
  });

  return (
    <form onSubmit={submit} style={{ display: "grid", gap: "0.75rem" }}>
      <label>
        Придумайте пароль
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
          minLength={8}
          autoComplete="new-password"
          style={fieldStyle}
        />
      </label>
      <label>
        Ещё раз
        <input
          type="password"
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
          required
          minLength={8}
          autoComplete="new-password"
          style={fieldStyle}
        />
      </label>
      <button type="submit" disabled={busy} style={{ padding: "0.6rem" }}>
        {busy ? "Заходим…" : "Войти"}
      </button>
      <FormStatus error={error} done={done} doneText="Готово, открываем кабинет…" />
    </form>
  );
}
