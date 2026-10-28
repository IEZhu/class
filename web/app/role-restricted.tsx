import Link from "next/link";
import type { ReactNode } from "react";

import type { User } from "../lib/api";
import { buttonClass } from "./ui/button";
import { EmptyState } from "./ui/empty";
import type { IconName } from "./ui/icons";
import { PageHeader } from "./ui/page-header";
import UserBar from "./user-bar";

// RoleRestricted — страница, которой у роли зрителя нет (DESIGN §3.16):
// каркас приложения с навигацией роли, заголовок страницы и карточка
// пустого состояния с причиной и путём к урокам — главной каждой роли.
// Один вид для всех таких страниц: практика студента у преподавателя,
// админа и гостя, «Люди и группы» у студента и гостя.
export default function RoleRestricted({
  me,
  title,
  icon,
  children,
}: {
  me: User;
  // title — название страницы, как в навигации
  title: string;
  icon: IconName;
  // children — кому страница доступна
  children: ReactNode;
}) {
  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page page--narrow">
        <PageHeader title={title} />
        <div className="card">
          <EmptyState
            icon={icon}
            title="Недоступно для вашей роли"
            action={
              <Link href="/lessons" className={buttonClass({ variant: "primary" })}>
                К урокам
              </Link>
            }
          >
            {children}
          </EmptyState>
        </div>
      </main>
    </>
  );
}
