"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import type { Group, Lesson } from "../../lib/api";
import { FormStatus, requestJson, useSubmit } from "../forms";

const fieldStyle = { padding: "0.5rem" };
const rowStyle = { display: "flex", gap: "0.5rem", flexWrap: "wrap", alignItems: "center" } as const;

// Создание урока или открытого события (ADR-010). Без группы — событие,
// туда приходят по гостевой ссылке; с группой — обычный урок, участники
// снапшотятся из её состава.
export default function NewEvent({ groups }: { groups: Group[] }) {
  const router = useRouter();
  const [title, setTitle] = useState("");
  const [groupId, setGroupId] = useState("");
  const [startsAt, setStartsAt] = useState("");
  const [minutes, setMinutes] = useState("60");

  const { busy, error, done, submit } = useSubmit(async () => {
    // Поле datetime-local отдаёт время без зоны — читаем его как местное
    // время браузера и переводим в UTC, как ждёт api (RFC3339).
    const start = new Date(startsAt);
    const end = new Date(start.getTime() + Number(minutes) * 60_000);
    const created = await requestJson<Lesson>("/api/lessons", "POST", {
      group_id: groupId ? Number(groupId) : null,
      title,
      starts_at: start.toISOString(),
      ends_at: end.toISOString(),
    });
    setTitle("");
    setStartsAt("");
    router.push(`/lesson/${created.id}`);
  });

  return (
    <form onSubmit={submit} style={{ border: "1px solid #ddd", borderRadius: 8, padding: "1rem", margin: "1rem 0" }}>
      <h2 style={{ marginTop: 0 }}>Новое событие</h2>
      <div style={rowStyle}>
        <input
          placeholder="Название"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          required={!groupId}
          style={fieldStyle}
        />
        <select value={groupId} onChange={(e) => setGroupId(e.target.value)} style={fieldStyle}>
          <option value="">без группы — открытое событие</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name}
            </option>
          ))}
        </select>
        <input
          type="datetime-local"
          value={startsAt}
          onChange={(e) => setStartsAt(e.target.value)}
          required
          style={fieldStyle}
        />
        <label>
          мин.
          <input
            type="number"
            min={5}
            max={480}
            value={minutes}
            onChange={(e) => setMinutes(e.target.value)}
            required
            style={{ ...fieldStyle, width: "5rem", marginLeft: "0.35rem" }}
          />
        </label>
        <button type="submit" disabled={busy}>
          {busy ? "Создаём…" : "Создать"}
        </button>
      </div>
      <p style={{ color: "#666", margin: "0.5rem 0 0" }}>
        Без группы получится открытое событие: на него зовут гостевой ссылкой со страницы события.
      </p>
      <FormStatus error={error} done={done} doneText="Создано." />
    </form>
  );
}
