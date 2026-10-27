import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { ClozePage, User } from "../../../lib/api";
import { ClickableTextScope } from "../../clickable-text";
import UserBar from "../../user-bar";
import ClozeSession from "./cloze-session";

export const metadata: Metadata = { title: "Вставь слово" };

const pageStyle = { fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" } as const;

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
      <main style={pageStyle}>
        <UserBar name={me.name} role={me.role} />
        <h1>Вставь слово</h1>
        <p>Упражнение есть только у студентов: оно строится по словам личного словаря.</p>
      </main>
    );
  }
  const page = await apiFetch<ClozePage>("/practice/cloze");

  return (
    <main style={pageStyle}>
      <UserBar name={me.name} role={me.role} />
      <h1>Вставь слово</h1>
      <p style={{ color: "#666" }}>
        Какое слово из вашего <Link href="/dictionary">словаря</Link> пропущено? Выберите вариант (клавиши 1–4) или
        впишите слово; Enter — дальше. Ответ двигает график повторений, как <Link href="/practice/cards">карточки</Link>.
      </p>
      <ClickableTextScope studentId={me.id}>
        {/* Новая выдача — новая сессия: счётчики начинаются с нуля */}
        <ClozeSession key={Date.now()} page={page} studentId={me.id} />
      </ClickableTextScope>
    </main>
  );
}
