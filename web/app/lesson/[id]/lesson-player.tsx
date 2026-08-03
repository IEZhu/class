"use client";

import "plyr/dist/plyr.css";

import { useEffect, useRef, useState } from "react";

import { requestJson } from "../../forms";

type MediaURL = { url: string; expires_at: string };

// Плеер записи урока (S1-6). Файл идёт хранилище ↔ браузер по presigned
// GET, минуя VPS; перемотка работает на Range-запросах, которые R2 отдаёт
// сам — отсюда обычный <video> под Plyr, без стриминговой обвязки.
export default function LessonPlayer({ lessonId }: { lessonId: number }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [src, setSrc] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const media = await requestJson<MediaURL>(`/api/media/${lessonId}/url`, "GET");
        if (!cancelled) setSrc(media.url);
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : "не удалось получить запись");
      }
    }
    void load();

    return () => {
      cancelled = true;
    };
  }, [lessonId]);

  useEffect(() => {
    if (!src || !videoRef.current) return;
    const video = videoRef.current;
    let player: { destroy: () => void } | null = null;
    let disposed = false;

    // Динамический импорт: сам Plyr нужен только на странице с записью,
    // и в типах он объявлен через export =, без default-импорта.
    void import("plyr").then(({ default: Plyr }) => {
      if (disposed) return;
      player = new Plyr(video, {
        // Скорость — самое востребованное при разборе урока
        controls: ["play-large", "play", "progress", "current-time", "mute", "volume", "settings", "fullscreen"],
        speed: { selected: 1, options: [0.75, 1, 1.25, 1.5, 2] },
        i18n: { restart: "Сначала", play: "Смотреть", pause: "Пауза", mute: "Без звука", speed: "Скорость" },
      });
    });

    return () => {
      disposed = true;
      player?.destroy();
    };
  }, [src]);

  if (error) return <p style={{ color: "crimson", margin: 0 }}>{error}</p>;
  if (!src) return <p style={{ color: "#666", margin: 0 }}>Готовим запись…</p>;

  return (
    <video
      ref={videoRef}
      controls
      playsInline
      preload="metadata"
      style={{ width: "100%" }}
      // Ссылка живёт 30 минут (storage.PresignTTL). Для длинной записи
      // она может истечь прямо во время просмотра — тогда браузер отдаст
      // ошибку загрузки, и мы берём свежую с той же позиции.
      onError={() => {
        const at = videoRef.current?.currentTime ?? 0;
        void requestJson<MediaURL>(`/api/media/${lessonId}/url`, "GET")
          .then((media) => {
            setSrc(media.url);
            if (videoRef.current) videoRef.current.currentTime = at;
          })
          .catch(() => setError("запись недоступна, обновите страницу"));
      }}
      src={src}
    />
  );
}
