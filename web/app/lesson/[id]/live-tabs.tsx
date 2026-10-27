"use client";

import { useState } from "react";
import type { ReactNode } from "react";

type Tab = "room" | "board";

const tabs: { id: Tab; title: string }[] = [
  { id: "room", title: "Класс" },
  { id: "board", title: "Доска" },
];

// Вкладки живого урока (S3-6): класс и доска. Обе остаются смонтированными:
// уход на доску не рвёт видео и звук, возврат на неё не переподключает
// доску. Доска монтируется при первом открытии.
export default function LiveTabs({ room, board }: { room: ReactNode; board: ReactNode }) {
  const [tab, setTab] = useState<Tab>("room");
  const [boardOpened, setBoardOpened] = useState(false);

  return (
    <div>
      <div role="tablist" style={{ display: "flex", gap: "0.5rem", marginBottom: "0.75rem" }}>
        {tabs.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => {
              setTab(t.id);
              if (t.id === "board") setBoardOpened(true);
            }}
            style={{
              padding: "0.4rem 1rem",
              border: "1px solid #2a7",
              borderRadius: 6,
              background: tab === t.id ? "#2a7" : "transparent",
              color: tab === t.id ? "#fff" : "inherit",
              cursor: "pointer",
            }}
          >
            {t.title}
          </button>
        ))}
      </div>
      <div role="tabpanel" hidden={tab !== "room"}>
        {room}
      </div>
      {boardOpened && (
        <div role="tabpanel" hidden={tab !== "board"}>
          {board}
        </div>
      )}
    </div>
  );
}
