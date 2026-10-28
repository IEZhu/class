"use client";

import { useId, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";

import { Icon } from "../../ui/icons";
import type { IconName } from "../../ui/icons";
import styles from "./media.module.css";

type Tab = "room" | "board";

const tabs: { id: Tab; title: string; icon: IconName }[] = [
  { id: "room", title: "Класс", icon: "cam" },
  { id: "board", title: "Доска", icon: "edit" },
];

// Вкладки живого урока (S3-6): класс и доска. Обе остаются смонтированными:
// уход на доску не рвёт видео и звук, возврат на неё не переподключает
// доску. Доска монтируется при первом открытии. Клавиатура — по образцу
// вкладок WAI-ARIA (DESIGN §3.8): Tab попадает на выбранную вкладку,
// стрелки, Home и End переключают.
export default function LiveTabs({ room, board }: { room: ReactNode; board: ReactNode }) {
  const [tab, setTab] = useState<Tab>("room");
  const [boardOpened, setBoardOpened] = useState(false);
  const refs = useRef<Partial<Record<Tab, HTMLButtonElement | null>>>({});
  const id = useId();

  function open(next: Tab) {
    setTab(next);
    if (next === "board") setBoardOpened(true);
  }

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const i = tabs.findIndex((t) => t.id === tab);
    const target =
      e.key === "ArrowRight"
        ? (i + 1) % tabs.length
        : e.key === "ArrowLeft"
          ? (i - 1 + tabs.length) % tabs.length
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? tabs.length - 1
              : null;
    if (target === null) return;
    e.preventDefault();
    open(tabs[target].id);
    refs.current[tabs[target].id]?.focus();
  }

  const mounted = (t: Tab) => t === "room" || boardOpened;

  return (
    <div className={styles.liveTabs}>
      <div role="tablist" aria-label="Класс и доска" className="tabs" onKeyDown={onKeyDown}>
        {tabs.map((t) => (
          <button
            key={t.id}
            ref={(el) => {
              refs.current[t.id] = el;
            }}
            type="button"
            role="tab"
            id={`${id}-${t.id}-tab`}
            aria-selected={tab === t.id}
            aria-controls={mounted(t.id) ? `${id}-${t.id}-panel` : undefined}
            tabIndex={tab === t.id ? 0 : -1}
            onClick={() => open(t.id)}
          >
            <Icon name={t.icon} size="sm" />
            {t.title}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${id}-room-panel`} aria-labelledby={`${id}-room-tab`} hidden={tab !== "room"}>
        {room}
      </div>
      {boardOpened && (
        <div role="tabpanel" id={`${id}-board-panel`} aria-labelledby={`${id}-board-tab`} hidden={tab !== "board"}>
          {board}
        </div>
      )}
    </div>
  );
}
