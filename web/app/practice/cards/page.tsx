import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { PracticeQueue, User } from "../../../lib/api";
import { ClickableTextScope } from "../../clickable-text";
import UserBar from "../../user-bar";
import CardsSession from "./cards-session";

const pageStyle = { fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" } as const;

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
      <main style={pageStyle}>
        <UserBar name={me.name} role={me.role} />
        <h1>Карточки</h1>
        <p>Карточки повторения есть только у студентов: они заводятся по словам личного словаря.</p>
      </main>
    );
  }
  const queue = await apiFetch<PracticeQueue>("/practice/queue");

  return (
    <main style={pageStyle}>
      <UserBar name={me.name} role={me.role} />
      <h1>Карточки</h1>
      <p style={{ color: "#666" }}>
        Вспомните слово, переверните карточку (пробел) и оцените себя клавишами 1–4. Слова попадают сюда из{" "}
        <Link href="/dictionary">словаря</Link>.
      </p>
      <ClickableTextScope studentId={me.id}>
        {/* Новая выдача очереди — новая сессия: счётчики начинаются с нуля */}
        <CardsSession key={Date.now()} queue={queue} studentId={me.id} />
      </ClickableTextScope>
    </main>
  );
}
