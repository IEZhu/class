import type { ReactNode } from "react";

import { Icon } from "./icons";
import type { IconName } from "./icons";

// EmptyState — пустое состояние (DESIGN §3.16): иконка, заголовок, одна
// строка пояснения и необязательное действие. titleAs — уровень заголовка
// по месту на странице (h1 — когда это вся страница, как у 404).
export function EmptyState({
  icon = "info",
  title,
  titleAs: Title = "h2",
  children,
  action,
}: {
  icon?: IconName;
  title: ReactNode;
  titleAs?: "h1" | "h2" | "h3" | "p";
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <span className="empty-icon">
        <Icon name={icon} />
      </span>
      <Title className="empty-title">{title}</Title>
      {children && <p className="empty-text">{children}</p>}
      {action && <div className="empty-action">{action}</div>}
    </div>
  );
}
