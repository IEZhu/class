"use client";

import { useCallback, useRef, useState } from "react";

import LessonPlayer from "./lesson-player";
import TranscriptPanel from "./transcript-panel";
import type { Person } from "./transcript-panel";

// Прошедший урок (S2-4): запись и транскрипт рядом. Позиция плеера
// подсвечивает реплику, клик по реплике мотает запись. На узком экране
// транскрипт уходит под плеер.
export default function LessonReplay({
  lessonId,
  canEdit,
  people,
}: {
  lessonId: number;
  canEdit: boolean;
  people: Person[];
}) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [timeMs, setTimeMs] = useState(0);

  const seek = useCallback((ms: number) => {
    const video = videoRef.current;
    if (!video) return;
    video.currentTime = ms / 1000;
    // Подсветка сразу, не дожидаясь timeupdate
    setTimeMs(ms);
    // play() может отказать (политика автозапуска) — тогда запись стоит на нужной позиции
    if (video.paused) void video.play().catch(() => {});
  }, []);

  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: "1rem", alignItems: "flex-start" }}>
      <div style={{ flex: "3 1 420px", minWidth: 0 }}>
        <LessonPlayer lessonId={lessonId} videoRef={videoRef} onTime={setTimeMs} />
      </div>
      <div style={{ flex: "2 1 300px", minWidth: 0 }}>
        <TranscriptPanel lessonId={lessonId} timeMs={timeMs} onSeek={seek} canEdit={canEdit} people={people} />
      </div>
    </div>
  );
}
