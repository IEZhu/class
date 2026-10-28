"use client";

import { useRouter } from "next/navigation";
import { useId, useState } from "react";

import type { Group, Lesson } from "../../lib/api";
import { FormStatus, requestJson, useSubmit } from "../forms";
import { Button } from "../ui/button";
import { cx } from "../ui/cx";
import { Field } from "../ui/field";
import { Icon } from "../ui/icons";
import { PageHeader } from "../ui/page-header";
import styles from "./lessons.module.css";

// Шапка списка уроков преподавателя с кнопкой «Новое событие», которая
// раскрывает форму карточкой под шапкой (DESIGN §6.2, раскрывашка с
// aria-expanded). Кнопка и форма делят состояние, поэтому шапку рисует
// этот клиентский компонент, а не страница.
//
// Создание урока или открытого события (ADR-010). Без группы — событие,
// туда приходят по гостевой ссылке; с группой — обычный урок, участники
// снапшотятся из её состава.
export default function NewEvent({ title, groups }: { title: string; groups: Group[] }) {
  const router = useRouter();
  const panelId = useId();
  const headingId = useId();
  const [open, setOpen] = useState(false);
  const [eventTitle, setEventTitle] = useState("");
  const [groupId, setGroupId] = useState("");
  const [startsAt, setStartsAt] = useState("");
  const [minutes, setMinutes] = useState("60");

  const { busy, error, done, submit } = useSubmit(async () => {
    // Поле datetime-local отдаёт время без зоны — читаем его как местное
    // время браузера и переводим в UTC, как ждёт api (RFC3339).
    const start = new Date(startsAt);
    const end = new Date(start.getTime() + Number(minutes) * 60_000);
    const times = { starts_at: start.toISOString(), ends_at: end.toISOString() };
    // Название — только у события: урок подписывается именем группы,
    // и api отклоняет title вместе с group_id (ADR-010).
    const body = groupId ? { group_id: Number(groupId), ...times } : { group_id: null, title: eventTitle, ...times };
    const created = await requestJson<Lesson>("/api/lessons", "POST", body);
    setEventTitle("");
    setStartsAt("");
    router.push(`/lesson/${created.id}`);
  });

  // С группой название не нужно: стираем введённое, чтобы оно не ушло
  // в api и не вернулось, если группу снова уберут
  function chooseGroup(value: string) {
    setGroupId(value);
    if (value) setEventTitle("");
  }

  return (
    <>
      <PageHeader
        title={title}
        actions={
          <Button
            type="button"
            variant="primary"
            aria-expanded={open}
            aria-controls={panelId}
            onClick={() => setOpen((v) => !v)}
          >
            <Icon name="plus" />
            Новое событие
          </Button>
        }
      />
      <section id={panelId} className={cx("card", styles.newEvent)} aria-labelledby={headingId} hidden={!open}>
        <div className="card-head">
          <h2 id={headingId}>Новое событие</h2>
        </div>
        <form onSubmit={submit} className={cx("card-pad", styles.form)}>
          <div className={styles.fields}>
            <Field label="Название">
              <input
                className="input"
                placeholder={groupId ? "Название — из группы" : undefined}
                value={eventTitle}
                onChange={(e) => setEventTitle(e.target.value)}
                required={!groupId}
                disabled={!!groupId}
              />
            </Field>
            <Field
              label="Группа"
              hint="Без группы получится открытое событие: на него зовут гостевой ссылкой со страницы события."
            >
              <select className="select" value={groupId} onChange={(e) => chooseGroup(e.target.value)}>
                <option value="">без группы — открытое событие</option>
                {groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Начало">
              <input
                className="input"
                type="datetime-local"
                value={startsAt}
                onChange={(e) => setStartsAt(e.target.value)}
                required
              />
            </Field>
            <Field label="Длительность, мин" className={styles.minutes}>
              <input
                className="input"
                type="number"
                min={5}
                max={480}
                value={minutes}
                onChange={(e) => setMinutes(e.target.value)}
                required
              />
            </Field>
          </div>
          <div className="row">
            <Button type="submit" variant="primary" disabled={busy} busy={busy}>
              {busy ? "Создаём…" : "Создать"}
            </Button>
            <FormStatus error={error} done={done} doneText="Создано." />
          </div>
        </form>
      </section>
    </>
  );
}
