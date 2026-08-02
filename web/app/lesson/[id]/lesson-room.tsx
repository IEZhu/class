"use client";

import "@livekit/components-styles";

import { LiveKitRoom, VideoConference } from "@livekit/components-react";
import { MediaDeviceFailure } from "livekit-client";
import { useState } from "react";

import { requestJson } from "../../forms";

type RoomToken = { url: string; token: string; room: string };

// Отказ getUserMedia — самая частая причина «вошёл, но меня не видно»:
// сигналинг при этом проходит, а треки не публикуются. Поэтому каждую
// причину показываем текстом, а не молча.
const mediaFailureHints: Record<string, string> = {
  [MediaDeviceFailure.PermissionDenied]:
    "браузер не дал доступ к камере и микрофону. Нажмите на замок в адресной строке и разрешите их для этого сайта, затем обновите страницу.",
  [MediaDeviceFailure.NotFound]:
    "камера или микрофон не найдены. Подключите устройство или войдите только со звуком.",
  [MediaDeviceFailure.DeviceInUse]:
    "камера или микрофон заняты другой программой (Zoom, Meet, другая вкладка). Закройте её и попробуйте снова.",
  [MediaDeviceFailure.Other]: "не удалось включить камеру или микрофон.",
};

export default function LessonRoom({ lessonId }: { lessonId: number }) {
  const [conn, setConn] = useState<RoomToken | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [mediaError, setMediaError] = useState<string | null>(null);

  // Токен берём по клику, а не при загрузке страницы: открытая вкладка
  // не должна занимать участнико-минуты, пока человек не решил войти.
  async function join() {
    setBusy(true);
    setError(null);
    setMediaError(null);
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
    <div>
      {mediaError && (
        <p style={{ color: "crimson", margin: "0 0 0.75rem" }} role="alert">
          {mediaError}
        </p>
      )}
      <div style={{ height: "70vh", minHeight: 360 }}>
        <LiveKitRoom
          serverUrl={conn.url}
          token={conn.token}
          connect
          video
          audio
          onConnected={() => setMediaError(null)}
          onMediaDeviceFailure={(failure, kind) => {
            const what = kind === "audioinput" ? "Микрофон" : "Камера";
            setMediaError(`${what}: ${mediaFailureHints[failure ?? MediaDeviceFailure.Other]}`);
          }}
          onError={(e) => setMediaError(`Ошибка комнаты: ${e.message}`)}
          onDisconnected={() => setConn(null)}
          data-lk-theme="default"
          style={{ height: "100%" }}
        >
          <VideoConference />
        </LiveKitRoom>
      </div>
    </div>
  );
}
