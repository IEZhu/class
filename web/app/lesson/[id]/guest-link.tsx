"use client";

import { useState } from "react";
import type { FormEvent } from "react";

import { FormStatus, request, requestJson, useSubmit } from "../../forms";

const rowStyle = { display: "flex", gap: "0.5rem", flexWrap: "wrap", alignItems: "center" } as const;

// Гостевая ссылка на событие (ADR-010): многоразовая, по ней заходят все,
// представившись именем. Показывается только организатору события.
export default function GuestLink({ lessonId }: { lessonId: number }) {
  const [link, setLink] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const issue = useSubmit(async () => {
    const res = await requestJson<{ url: string }>(`/api/lessons/${lessonId}/guest-link`, "POST");
    setLink(res.url);
    setCopied(false);
  });

  const revoke = useSubmit(async () => {
    await request(`/api/lessons/${lessonId}/guest-link`, "DELETE");
    setLink(null);
  });

  function confirmRevoke(e: FormEvent) {
    e.preventDefault();
    // Отзыв ломает уже разосланную ссылку — спрашиваем
    if (window.confirm("Отозвать гостевую ссылку? Кто ещё не зашёл, зайти не сможет.")) void revoke.submit(e);
  }

  async function copy() {
    if (!link) return;
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
    } catch {
      // Буфер недоступен — ссылка и так на экране
      setCopied(false);
    }
  }

  return (
    <div style={{ margin: "1rem 0" }}>
      <div style={rowStyle}>
        <form onSubmit={issue.submit} style={{ display: "inline" }}>
          <button type="submit" disabled={issue.busy}>
            {issue.busy ? "Готовим…" : link ? "Выпустить новую ссылку" : "Гостевая ссылка"}
          </button>
        </form>
        {link && (
          <form onSubmit={confirmRevoke} style={{ display: "inline" }}>
            <button type="submit" disabled={revoke.busy}>
              Отозвать
            </button>
          </form>
        )}
        <FormStatus error={issue.error ?? revoke.error} done={false} doneText="" />
      </div>
      {link && (
        <div style={{ ...rowStyle, marginTop: "0.5rem" }}>
          <input readOnly value={link} onFocus={(e) => e.target.select()} style={{ padding: "0.5rem", minWidth: "22rem" }} />
          <button type="button" onClick={copy}>
            {copied ? "Скопировано" : "Скопировать"}
          </button>
        </div>
      )}
      {link && (
        <p style={{ color: "#666", margin: "0.5rem 0 0" }}>
          Ссылка многоразовая: по ней зайдут все, кому вы её отправите. Перевыпуск отключает предыдущую.
        </p>
      )}
    </div>
  );
}
