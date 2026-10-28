"use client";

import { useRouter } from "next/navigation";
import { useId, useState } from "react";

import { FormStatus, request, useSubmit } from "../forms";
import { Button } from "../ui/button";
import { cx } from "../ui/cx";
import { Field } from "../ui/field";
import styles from "./account.module.css";

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
  const titleId = useId();
  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/auth/me", "PATCH", { name });
    onSaved();
  });

  return (
    <form onSubmit={submit} className="card" aria-labelledby={titleId}>
      <div className="card-head">
        <h2 id={titleId}>Имя</h2>
      </div>
      <div className="card-pad">
        <Field label="Как вас показывать">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} required autoComplete="name" />
        </Field>
      </div>
      <div className={cx("card-foot", styles.foot)}>
        <FormStatus error={error} done={done} doneText="Имя обновлено." />
        <Button type="submit" variant="primary" blockOnPhone disabled={busy} busy={busy} className={styles.submit}>
          {busy ? "Сохраняем…" : "Сохранить"}
        </Button>
      </div>
    </form>
  );
}

function PasswordForm() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const titleId = useId();
  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/auth/password", "POST", {
      current_password: current,
      new_password: next,
    });
    setCurrent("");
    setNext("");
  });

  return (
    <form onSubmit={submit} className="card" aria-labelledby={titleId}>
      <div className="card-head">
        <h2 id={titleId}>Пароль</h2>
      </div>
      <div className={cx("card-pad", styles.pair)}>
        <Field label="Текущий пароль">
          <input
            className="input"
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            required
            autoComplete="current-password"
          />
        </Field>
        <Field label="Новый пароль" hint="Не короче 8 символов.">
          <input
            className="input"
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            required
            minLength={8}
            autoComplete="new-password"
          />
        </Field>
      </div>
      <div className={cx("card-foot", styles.foot)}>
        <FormStatus error={error} done={done} doneText="Пароль изменён. Другие устройства разлогинены." />
        <Button type="submit" variant="primary" blockOnPhone disabled={busy} busy={busy} className={styles.submit}>
          {busy ? "Меняем…" : "Сменить пароль"}
        </Button>
      </div>
    </form>
  );
}
