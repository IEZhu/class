"use client";

import dynamic from "next/dynamic";

import { cx } from "../../ui/cx";
import styles from "./media.module.css";

// Доска урока (S3-6, ADR-020). Excalidraw трогает window уже при импорте,
// поэтому редактор грузится только в браузере и отдельным чанком: страницы
// без доски его не тянут.
const WhiteboardCanvas = dynamic(() => import("./whiteboard-canvas"), {
  ssr: false,
  // Место доски держится сразу: светлый лист той же высоты, что редактор
  loading: () => (
    <div className={cx("light-island", styles.boardCanvas, styles.boardLoading)} role="status">
      <span className="spinner" aria-hidden="true" />
      Загружаем доску…
    </div>
  ),
});

export default function Whiteboard({ lessonId, userName }: { lessonId: number; userName: string }) {
  return <WhiteboardCanvas lessonId={lessonId} userName={userName} />;
}
