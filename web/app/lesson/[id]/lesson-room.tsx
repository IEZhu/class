"use client";

import "@livekit/components-styles";

import { LiveKitRoom, VideoConference } from "@livekit/components-react";
import { MediaDeviceFailure } from "livekit-client";
import { useState } from "react";

import { requestJson } from "../../forms";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import { Icon } from "../../ui/icons";
import styles from "./media.module.css";

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

  // Сбой последней попытки виден и после отключения: LiveKit при ошибке
  // рвёт соединение, и сообщение не должно пропадать вместе с комнатой
  const lastError = error ?? mediaError;

  if (!conn) {
    return (
      <div className={styles.stage}>
        <div className={styles.stageInvite}>
          <span className={styles.stageIcon}>
            <Icon name="cam" size="lg" />
          </span>
          <p>Войдите в класс, чтобы включить видео и звук</p>
          <Button type="button" variant="primary" size="lg" onClick={join} disabled={busy} busy={busy}>
            {busy ? "Подключаемся…" : "Войти в класс"}
          </Button>
          {lastError && (
            <Alert tone="danger" className={styles.stageAlert}>
              <span className={styles.cap}>{lastError}</span>
            </Alert>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className={styles.roomWrap}>
      {mediaError && (
        <Alert tone="danger">
          <span className={styles.cap}>{mediaError}</span>
        </Alert>
      )}
      {/* Тёмная тема LiveKit (data-lk-theme) — своя, сцена вокруг неё —
          цвета видео (DESIGN §4.5) */}
      <div className={styles.stageLive}>
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
          className={styles.lkRoom}
        >
          <VideoConference />
        </LiveKitRoom>
      </div>
    </div>
  );
}
