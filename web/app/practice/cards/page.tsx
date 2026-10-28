import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { PracticeQueue, User } from "../../../lib/api";
import { ClickableTextScope } from "../../clickable-text";
import RoleRestricted from "../../role-restricted";
import { PageHeader } from "../../ui/page-header";
import UserBar from "../../user-bar";
import CardsSession from "./cards-session";

export const metadata: Metadata = { title: "Карточки" };

// S4-3: карточки повторения по FSRS — слова личного словаря, чей показ
// наступил (GET /practice/queue, S4-2), самые просроченные первыми.
export default async function PracticeCardsPage() {
  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  if (me.role !== "student") {
    return (
      <RoleRestricted me={me} title="Карточки" icon="cards">
        Карточки повторения есть только у студентов: они заводятся по словам личного словаря.
      </RoleRestricted>
    );
  }
  const queue = await apiFetch<PracticeQueue>("/practice/queue");

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page page--narrow">
        <PageHeader title="Карточки">
          <p className="muted">
            Вспомните слово, переверните карточку и оцените себя. Слова попадают сюда из{" "}
            <Link href="/dictionary">словаря</Link>.
          </p>
        </PageHeader>
        <ClickableTextScope studentId={me.id}>
          {/* Новая выдача очереди — новая сессия: счётчики начинаются с нуля */}
          <CardsSession key={Date.now()} queue={queue} studentId={me.id} />
        </ClickableTextScope>
      </main>
    </>
  );
}
