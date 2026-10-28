import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { ClozePage, User } from "../../../lib/api";
import { ClickableTextScope } from "../../clickable-text";
import { Alert } from "../../ui/alert";
import { PageHeader } from "../../ui/page-header";
import UserBar from "../../user-bar";
import ClozeSession from "./cloze-session";

export const metadata: Metadata = { title: "Вставь слово" };

// S4-4/S4-5: «вставь слово» — предложения из уроков, своих текстов и
// Tatoeba с пропуском на месте слова из словаря (GET /practice/cloze);
// ответ двигает график повторений FSRS (ADR-037).
export default async function PracticeClozePage() {
  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  if (me.role !== "student") {
    return (
      <>
        <UserBar name={me.name} role={me.role} />
        <main className="page page--narrow">
          <PageHeader title="Вставь слово" />
          <Alert tone="info" live={false}>
            Упражнение есть только у студентов: оно строится по словам личного словаря.
          </Alert>
        </main>
      </>
    );
  }
  const page = await apiFetch<ClozePage>("/practice/cloze");

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page page--narrow">
        <PageHeader title="Вставь слово">
          <p className="muted">
            Какое слово из вашего <Link href="/dictionary">словаря</Link> пропущено? Выберите вариант или впишите слово.
            Ответ двигает график повторений, как <Link href="/practice/cards">карточки</Link>.
          </p>
        </PageHeader>
        <ClickableTextScope studentId={me.id}>
          {/* Новая выдача — новая сессия: счётчики начинаются с нуля */}
          <ClozeSession key={Date.now()} page={page} studentId={me.id} />
        </ClickableTextScope>
      </main>
    </>
  );
}
