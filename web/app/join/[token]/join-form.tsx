"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import { FormStatus, requestJson, useSubmit } from "../../forms";

type Joined = { lesson_id: number };

// Гость называет имя и сразу оказывается на странице события: сессию
// выдаёт api тем же ответом, отдельного входа у гостя нет.
export default function JoinForm({ token }: { token: string }) {
  const router = useRouter();
  const [name, setName] = useState("");

  const { busy, error, done, submit } = useSubmit(async () => {
    const joined = await requestJson<Joined>(`/api/join/${encodeURIComponent(token)}`, "POST", { name });
    router.push(`/lesson/${joined.lesson_id}`);
    router.refresh();
  });

  return (
    <form onSubmit={submit} style={{ display: "grid", gap: "0.75rem" }}>
      <label>
        Как вас представить
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          required
          maxLength={60}
          autoComplete="name"
          placeholder="Имя и фамилия"
          style={{ width: "100%", padding: "0.5rem" }}
        />
      </label>
      <button type="submit" disabled={busy} style={{ padding: "0.6rem" }}>
        {busy ? "Заходим…" : "Войти на событие"}
      </button>
      <FormStatus error={error} done={done} doneText="Готово, открываем событие…" />
    </form>
  );
}
