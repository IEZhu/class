"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import { FormStatus, request, useSubmit } from "../forms";

const fieldStyle = { width: "100%", padding: "0.5rem" };
const sectionStyle = {
  border: "1px solid #ddd",
  borderRadius: 8,
  padding: "1rem",
  margin: "1rem 0",
  display: "grid",
  gap: "0.75rem",
} as const;

export default function AccountForms({ name: initialName }: { name: string }) {
  const router = useRouter();

  return (
    <>
      <NameForm initialName={initialName} onSaved={() => router.refresh()} />
      <PasswordForm />
    </>
  );
}

function NameForm({ initialName, onSaved }: { initialName: string; onSaved: () => void }) {
  const [name, setName] = useState(initialName);
  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/auth/me", "PATCH", { name });
    onSaved();
  });

  return (
    <form onSubmit={submit} style={sectionStyle}>
      <h2 style={{ margin: 0 }}>Имя</h2>
      <label>
        Как вас показывать
        <input value={name} onChange={(e) => setName(e.target.value)} required style={fieldStyle} />
      </label>
      <button type="submit" disabled={busy} style={{ padding: "0.6rem" }}>
        {busy ? "Сохраняем…" : "Сохранить"}
      </button>
      <FormStatus error={error} done={done} doneText="Имя обновлено." />
    </form>
  );
}

function PasswordForm() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/auth/password", "POST", {
      current_password: current,
      new_password: next,
    });
    setCurrent("");
    setNext("");
  });

  return (
    <form onSubmit={submit} style={sectionStyle}>
      <h2 style={{ margin: 0 }}>Пароль</h2>
      <label>
        Текущий пароль
        <input
          type="password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          required
          autoComplete="current-password"
          style={fieldStyle}
        />
      </label>
      <label>
        Новый пароль
        <input
          type="password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          required
          minLength={8}
          autoComplete="new-password"
          style={fieldStyle}
        />
      </label>
      <button type="submit" disabled={busy} style={{ padding: "0.6rem" }}>
        {busy ? "Меняем…" : "Сменить пароль"}
      </button>
      <FormStatus error={error} done={done} doneText="Пароль изменён. Другие устройства разлогинены." />
    </form>
  );
}
