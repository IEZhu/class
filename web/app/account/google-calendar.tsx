"use client";

import { useRouter } from "next/navigation";

import type { GoogleCalendarStatus } from "../../lib/api";
import { FormStatus, request, requestJson, useSubmit } from "../forms";

const sectionStyle = {
  border: "1px solid #ddd",
  borderRadius: 8,
  padding: "1rem",
  margin: "1rem 0",
  display: "grid",
  gap: "0.75rem",
} as const;

// Итоги возврата с экрана согласия Google (?google= от api, ADR-023)
const results: Record<string, { text: string; ok: boolean }> = {
  connected: { text: "Календарь подключён: уроки появятся в нём в течение минуты.", ok: true },
  denied: { text: "Доступ не выдан — календарь не подключён.", ok: false },
  scope: {
    text: "Google не дал доступ к событиям календаря: при подключении оставьте отмеченным пункт про календарь.",
    ok: false,
  },
  failed: { text: "Не удалось подключить календарь, попробуйте ещё раз.", ok: false },
  unavailable: { text: "Подключение календаря на стенде пока не настроено.", ok: false },
};

// Google Calendar преподавателя (S1-1): уроки уходят событиями в его
// календарь, а участники получают приглашения от Google.
export default function GoogleCalendar({
  status,
  result,
}: {
  status: GoogleCalendarStatus;
  result?: string;
}) {
  const router = useRouter();
  // Старт — POST: api кладёт state в cookie и отдаёт адрес экрана согласия
  const connect = useSubmit(async () => {
    const { url } = await requestJson<{ url: string }>("/api/auth/google/start", "POST");
    window.location.assign(url);
  });
  const disconnect = useSubmit(async () => {
    await request("/api/auth/google", "DELETE");
    router.replace("/account");
    router.refresh();
  });
  const shown = result ? results[result] : undefined;

  return (
    <section style={sectionStyle}>
      <h2 style={{ margin: 0 }}>Google Календарь</h2>
      {shown && <span style={{ color: shown.ok ? "#2a7" : "crimson" }}>{shown.text}</span>}
      {!status.available ? (
        <p style={{ margin: 0, color: "#666" }}>
          Недоступно: на стенде не настроено приложение Google. Уроки пока видны только здесь, на
          платформе.
        </p>
      ) : status.connected ? (
        <form onSubmit={disconnect.submit} style={{ display: "grid", gap: "0.75rem" }}>
          <p style={{ margin: 0 }}>
            Подключён: созданные, перенесённые и отменённые уроки отражаются в вашем календаре,
            участники получают приглашения от Google.
          </p>
          <button type="submit" disabled={disconnect.busy} style={{ padding: "0.6rem" }}>
            {disconnect.busy ? "Отключаем…" : "Отключить"}
          </button>
          <FormStatus error={disconnect.error} done={false} doneText="" />
        </form>
      ) : (
        <form onSubmit={connect.submit} style={{ display: "grid", gap: "0.75rem" }}>
          <p style={{ margin: 0 }}>
            Подключите календарь, чтобы уроки появлялись в нём, а участники получали приглашения.
          </p>
          <button type="submit" disabled={connect.busy} style={{ padding: "0.6rem" }}>
            {connect.busy ? "Переходим в Google…" : "Подключить Google Календарь"}
          </button>
          <FormStatus error={connect.error} done={false} doneText="" />
        </form>
      )}
    </section>
  );
}
