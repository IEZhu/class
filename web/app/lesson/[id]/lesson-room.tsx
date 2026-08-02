"use client";

import "@livekit/components-styles";

import { LiveKitRoom, VideoConference } from "@livekit/components-react";
import { useState } from "react";

import { requestJson } from "../../forms";

type RoomToken = { url: string; token: string; room: string };

// Комната урока (S1-4). Токен берём по кнопке, а не при загрузке страницы:
// открытая вкладка не должна занимать участнико-минуты free tier, пока
// человек не решил войти. Медиа идёт браузер ↔ LiveKit Cloud, мимо VPS.
export default function LessonRoom({ lessonId }: { lessonId: number }) {
  const [conn, setConn] = useState<RoomToken | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function join() {
    setBusy(true);
    setError(null);
    try {
      setConn(await requestJson<RoomToken>(`/api/lessons/${lessonId}/room-token`, "GET"));
    } catch (e) {
      setError(e instanceof Error ? e.message : "не удалось войти в класс");
    } finally {
      setBusy(false);
    }
  }

  if (!conn) {
    return (
      <div>
        <button onClick={join} disabled={busy} style={{ padding: "0.75rem 1.5rem", fontSize: "1rem" }}>
          {busy ? "Подключаемся…" : "Войти в класс"}
        </button>
        {error && <p style={{ color: "crimson" }}>{error}</p>}
      </div>
    );
  }

  return (
    <div style={{ height: "70vh", minHeight: 360 }}>
      <LiveKitRoom
        serverUrl={conn.url}
        token={conn.token}
        connect
        video
        audio
        onDisconnected={() => setConn(null)}
        data-lk-theme="default"
        style={{ height: "100%" }}
      >
        <VideoConference />
      </LiveKitRoom>
    </div>
  );
}
