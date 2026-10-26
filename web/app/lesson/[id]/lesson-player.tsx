"use client";

import "plyr/dist/plyr.css";

import { useEffect, useRef, useState } from "react";

import { requestJson } from "../../forms";

type MediaURL = { url: string; expires_at: string };

// Куда вернуть просмотр после смены ссылки: позиция и шло ли воспроизведение.
type Resume = { at: number; playing: boolean };

// Плеер записи урока (S1-6). Файл идёт хранилище ↔ браузер по presigned
// GET, минуя VPS; перемотка работает на Range-запросах, которые R2 отдаёт
// сам — отсюда обычный <video> под Plyr, без стриминговой обвязки.
export default function LessonPlayer({ lessonId }: { lessonId: number }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const resumeRef = useRef<Resume | null>(null);
  const [src, setSrc] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const playable = src !== null && error === null;

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

  // Plyr ставится один раз на <video>, а не на каждую ссылку: его destroy()
  // сбрасывает src и подменяет элемент копией. Свежая ссылка меняет только
  // src, Plyr подхватывает её по событиям самого <video>.
  useEffect(() => {
    if (!playable || !videoRef.current) return;
    const video = videoRef.current;
    let player: { destroy: () => void } | null = null;
    let disposed = false;

    // Динамический импорт: сам Plyr нужен только на странице с записью,
    // и в типах он объявлен через export =, без default-импорта.
    void import("plyr")
      .then(({ default: Plyr }) => {
        if (disposed) return;
        player = new Plyr(video, {
          // Скорость — самое востребованное при разборе урока
          controls: ["play-large", "play", "progress", "current-time", "mute", "volume", "settings", "fullscreen"],
          speed: { selected: 1, options: [0.75, 1, 1.25, 1.5, 2] },
          i18n: { restart: "Сначала", play: "Смотреть", pause: "Пауза", mute: "Без звука", speed: "Скорость" },
        });
      })
      // Чанк не догрузился (сеть, выкатка новой сборки) или Plyr упал при
      // разборе — запись остаётся во встроенном плеере браузера. Родные
      // контролы возвращаем: Plyr мог успеть их снять.
      .catch(() => {
        if (disposed) return;
        video.controls = true;
        setNotice("Расширенный плеер не загрузился — запись открыта во встроенном.");
      });

    return () => {
      disposed = true;
      player?.destroy();
    };
  }, [playable]);

  if (error) return <p style={{ color: "crimson", margin: 0 }}>{error}</p>;
  if (!src) return <p style={{ color: "#666", margin: 0 }}>Готовим запись…</p>;

  return (
    <>
      {/* Plyr переносит <video> в свою разметку. Обёртка держит её внутри
          одного узла, который React снимает целиком: сам <video> на старом
          месте он бы уже не нашёл. */}
      <div>
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
            const video = videoRef.current;
            if (!video) return;
            // Не открылась и свежая ссылка (файла нет, хранилище недоступно):
            // второй круг не крутим, иначе ссылки запрашивались бы без конца.
            if (resumeRef.current) {
              setError("запись недоступна, обновите страницу");
              return;
            }
            resumeRef.current = { at: video.currentTime, playing: !video.paused };
            void requestJson<MediaURL>(`/api/media/${lessonId}/url`, "GET")
              .then((media) => setSrc(media.url))
              .catch(() => setError("запись недоступна, обновите страницу"));
          }}
          // Новый src грузится асинхронно, и до его метаданных currentTime
          // относится к старому источнику: перематываем только здесь.
          onLoadedMetadata={() => {
            const video = videoRef.current;
            const resume = resumeRef.current;
            if (!video || !resume) return;
            resumeRef.current = null;
            video.currentTime = resume.at;
            // play() может отказать (политика автозапуска) — тогда запись
            // просто стоит на паузе на нужной позиции.
            if (resume.playing) void video.play().catch(() => {});
          }}
          src={src}
        />
      </div>
      {notice && <p style={{ color: "#666", margin: "0.5rem 0 0" }}>{notice}</p>}
    </>
  );
}
