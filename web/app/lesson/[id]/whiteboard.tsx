"use client";

import dynamic from "next/dynamic";

// Доска урока (S3-6, ADR-020). Excalidraw трогает window уже при импорте,
// поэтому редактор грузится только в браузере и отдельным чанком: страницы
// без доски его не тянут.
const WhiteboardCanvas = dynamic(() => import("./whiteboard-canvas"), {
  ssr: false,
  loading: () => <p style={{ color: "#666", margin: 0 }}>Загружаем доску…</p>,
});

export default function Whiteboard({ lessonId, userName }: { lessonId: number; userName: string }) {
  return <WhiteboardCanvas lessonId={lessonId} userName={userName} />;
}
