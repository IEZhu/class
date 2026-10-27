import Link from "next/link";
import type { ReactNode } from "react";

import { Icon } from "./icons";

// PageHeader — шапка страницы (DESIGN §3.21): ссылка на уровень выше,
// заголовок h1 со статусом рядом, строка сведений и действия справа
// (на телефоне — под заголовком).
export function PageHeader({
  title,
  crumb,
  pill,
  meta,
  actions,
  children,
}: {
  title: ReactNode;
  crumb?: { href: string; label: string };
  pill?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div className="titles">
        {crumb && (
          <Link className="crumb" href={crumb.href}>
            <Icon name="back" size="sm" />
            {crumb.label}
          </Link>
        )}
        <div className="title-row">
          <h1>{title}</h1>
          {pill}
        </div>
        {meta && <div className="meta">{meta}</div>}
        {children}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </div>
  );
}
