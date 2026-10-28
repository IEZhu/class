"use client";

import { useRouter } from "next/navigation";
import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { flushSync } from "react-dom";

import { FormStatus, request, useSubmit } from "../../forms";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import { Field } from "../../ui/field";

// Пароль придумывает сам приглашённый — пригласивший его не узнаёт (ADR-008).
export default function AcceptForm({ token }: { token: string }) {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  // Несовпадение паролей — ошибка поля «Ещё раз», а не всей формы: фокус
  // уходит в поле, и программа экранного доступа читает его вместе с ошибкой.
  // Правка любого из двух полей снимает ошибку до следующей отправки
  const [mismatch, setMismatch] = useState(false);
  const repeatRef = useRef<HTMLInputElement>(null);

  const { busy, error, done, submit } = useSubmit(async () => {
    await request(`/api/signup/${encodeURIComponent(token)}`, "POST", { password });
    // accept сразу выдаёт cookie сессии — человек попадает в кабинет залогиненным
    router.push("/lessons");
    router.refresh();
  });

  function onSubmit(e: FormEvent) {
    const differ = password !== repeat;
    // Ошибка должна быть в разметке (aria-describedby) до переноса фокуса
    flushSync(() => setMismatch(differ));
    if (differ) {
      e.preventDefault();
      repeatRef.current?.focus();
      return;
    }
    void submit(e);
  }

  return (
    <form onSubmit={onSubmit} className="stack">
      <Field label="Придумайте пароль" hint="Не короче 8 символов.">
        <input
          className="input"
          type="password"
          value={password}
          onChange={(e) => {
            setPassword(e.target.value);
            setMismatch(false);
          }}
          required
          minLength={8}
          autoComplete="new-password"
        />
      </Field>
      <Field label="Ещё раз" error={mismatch ? "Пароли не совпадают" : null}>
        <input
          ref={repeatRef}
          className="input"
          type="password"
          value={repeat}
          onChange={(e) => {
            setRepeat(e.target.value);
            setMismatch(false);
          }}
          required
          minLength={8}
          autoComplete="new-password"
        />
      </Field>
      {error && (
        <Alert tone="danger">
          <span className="form-status-text">{error}</span>
        </Alert>
      )}
      <Button type="submit" variant="primary" size="lg" block disabled={busy} busy={busy}>
        {busy ? "Заходим…" : "Принять приглашение"}
      </Button>
      <FormStatus error={null} done={done} doneText="Готово, открываем кабинет…" />
    </form>
  );
}
