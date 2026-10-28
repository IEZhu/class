"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import { FormStatus, requestJson, useSubmit } from "../../forms";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import { Field } from "../../ui/field";

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
    <form onSubmit={submit} className="stack">
      <Field label="Как вас представить">
        <input
          className="input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          required
          maxLength={60}
          autoComplete="name"
          placeholder="Имя и фамилия"
        />
      </Field>
      {error && (
        <Alert tone="danger">
          <span className="form-status-text">{error}</span>
        </Alert>
      )}
      <Button type="submit" variant="primary" size="lg" block disabled={busy} busy={busy}>
        {busy ? "Заходим…" : "Войти на событие"}
      </Button>
      <FormStatus error={null} done={done} doneText="Готово, открываем событие…" />
    </form>
  );
}
