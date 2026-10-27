"use client";

import { useState } from "react";
import type { FormEvent } from "react";

import { FormStatus, request, requestJson, useSubmit } from "../../forms";

const rowStyle = { display: "flex", gap: "0.5rem", flexWrap: "wrap", alignItems: "center" } as const;

// Гостевая ссылка на событие (ADR-010): многоразовая, по ней заходят все,
// представившись именем. Показывается только организатору и только
// у события без группы — api выдаёт ссылку лишь ему.
//
// Сам токен хранится лишь хэшем: после перезагрузки страницы ссылку
// не показать, известно только, что она выпущена (hasLink). Поэтому
// перевыпуск, ломающий уже разосланную ссылку, идёт только с подтверждения,
// а отозвать её можно и без нового выпуска (ADR-032).
export default function GuestLink({ lessonId, hasLink }: { lessonId: number; hasLink: boolean }) {
  const [exists, setExists] = useState(hasLink);
  // Открытый текст ссылки есть только сразу после выпуска в этой вкладке
  const [link, setLink] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const issue = useSubmit(async () => {
    const res = await requestJson<{ url: string }>(`/api/lessons/${lessonId}/guest-link`, "POST");
    setLink(res.url);
    setExists(true);
    setCopied(false);
  });

  const revoke = useSubmit(async () => {
    await request(`/api/lessons/${lessonId}/guest-link`, "DELETE");
    setLink(null);
    setExists(false);
  });

  function confirmIssue(e: FormEvent) {
    e.preventDefault();
    // Первый выпуск ничего не ломает; перевыпуск отключает разосланную ссылку
    if (
      !exists ||
      window.confirm("Выпустить новую ссылку? Старая перестанет работать у всех, кому вы её уже отправили.")
    ) {
      void issue.submit(e);
    }
  }

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
        <form onSubmit={confirmIssue} style={{ display: "inline" }}>
          <button type="submit" disabled={issue.busy}>
            {issue.busy ? "Готовим…" : exists ? "Выпустить новую ссылку" : "Гостевая ссылка"}
          </button>
        </form>
        {exists && (
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
      {exists && (
        <p style={{ color: "#666", margin: "0.5rem 0 0" }}>
          {link
            ? "Ссылка многоразовая: по ней зайдут все, кому вы её отправите. Перевыпуск отключает предыдущую."
            : "Ссылка уже выпущена и действует. Показать её ещё раз нельзя — хранится только отпечаток; нужна снова — выпустите новую, старая при этом перестанет работать."}
        </p>
      )}
    </div>
  );
}
