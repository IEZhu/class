import type { ReactNode } from "react";

import { cx } from "./cx";
import { Icon } from "./icons";
import type { IconName } from "./icons";

export type AlertTone = "info" | "success" | "warning" | "danger" | "neutral";

const icons: Record<AlertTone, IconName> = {
  info: "info",
  success: "check",
  warning: "warning",
  danger: "error",
  neutral: "info",
};

// Alert — сообщение в рамке (DESIGN §3.14): иконка + текст, цвет — не
// единственный признак. Ошибка — role="alert", остальное — role="status",
// чтобы появившееся сообщение прочла программа экранного доступа.
// live={false} — для постоянной плашки страницы (предупреждение о записи),
// которую незачем объявлять.
export function Alert({
  tone = "info",
  title,
  children,
  action,
  live = true,
  className,
}: {
  tone?: AlertTone;
  title?: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  live?: boolean;
  className?: string;
}) {
  const role = live ? (tone === "danger" ? "alert" : "status") : undefined;
  return (
    <div className={cx("alert", `alert--${tone}`, className)} role={role}>
      <Icon name={icons[tone]} />
      <div className="alert-body">
        {title && <p className="alert-title">{title}</p>}
        {children && <div>{children}</div>}
      </div>
      {action}
    </div>
  );
}
