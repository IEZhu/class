"use client";

import { useCallback, useRef, useState } from "react";
import type { ReactNode } from "react";

import LessonPlayer from "./lesson-player";
import styles from "./media.module.css";
import TranscriptPanel from "./transcript-panel";
import type { Person } from "./transcript-panel";

// Прошедший урок (S2-4): запись и транскрипт рядом. Позиция плеера
// подсвечивает реплику, клик по реплике мотает запись. На узком экране
// транскрипт уходит под плеер. below — блок страницы под плеером (словарь
// урока): на широком экране он слева от транскрипта, на узком — после него.
export default function LessonReplay({
  lessonId,
  canEdit,
  people,
  below,
}: {
  lessonId: number;
  canEdit: boolean;
  people: Person[];
  below?: ReactNode;
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
    <div className={styles.replay}>
      <div className={styles.replayPlayer}>
        <LessonPlayer lessonId={lessonId} videoRef={videoRef} onTime={setTimeMs} />
      </div>
      <div className={styles.replayTranscript}>
        <TranscriptPanel lessonId={lessonId} timeMs={timeMs} onSeek={seek} canEdit={canEdit} people={people} />
      </div>
      {below && <div className={styles.replayBelow}>{below}</div>}
    </div>
  );
}
