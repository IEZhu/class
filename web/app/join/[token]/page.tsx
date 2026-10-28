import type { Metadata } from "next";
import Link from "next/link";

import { apiFetch, ApiError } from "../../../lib/api";
import { Alert } from "../../ui/alert";
import { buttonClass } from "../../ui/button";
import { EmptyState } from "../../ui/empty";
import { LocalTime } from "../../ui/local-time";
import JoinForm from "./join-form";

export const metadata: Metadata = { title: "Вход на событие" };

type GuestEvent = {
  title: string;
  teacher_name: string;
  starts_at: string;
  ends_at: string;
  recorded: boolean;
};

// Публичная страница события: человек ещё не залогинен и учётки у него
// не будет — он представляется именем и попадает в комнату (ADR-010).
// Вид — карточка страниц без сессии (DESIGN §6.1).
export default async function JoinPage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params;

  let event: GuestEvent;
  try {
    event = await apiFetch<GuestEvent>(`/join/${encodeURIComponent(token)}`);
  } catch (e) {
    const over = e instanceof ApiError && e.status === 410;
    if (e instanceof ApiError && (e.status === 404 || over)) {
      return (
        <main className="auth-page">
          <div className="card card-pad">
            <EmptyState
              icon={over ? "clock" : "warning"}
              title={over ? "Событие завершилось" : "Ссылка недействительна"}
              titleAs="h1"
              action={
                <Link href="/login" className={buttonClass({ variant: "secondary" })}>
                  Ко входу
                </Link>
              }
            >
              {over
                ? "Это событие уже прошло, войти в него нельзя."
                : "Такого события нет — возможно, ссылку отозвали."}
            </EmptyState>
          </div>
        </main>
      );
    }
    throw e;
  }

  return (
    <main className="auth-page">
      <div className="card card-pad">
        <div className="stack stack--sm">
          <h1>{event.title}</h1>
          <p className="meta">
            {/* Время — в поясе зрителя; без JS — по UTC с подписью */}
            <LocalTime value={event.starts_at} options={{ dateStyle: "full", timeStyle: "short" }} />
            <span>ведёт {event.teacher_name}</span>
          </p>
        </div>
        {/* Гость попадает в запись и транскрипт — предупреждаем до того,
            как он представится (ADR-010) */}
        {event.recorded && (
          <Alert tone="warning" live={false}>
            Событие записывается: видео и расшифровка сохранятся у организатора.
          </Alert>
        )}
        <JoinForm token={token} />
      </div>
    </main>
  );
}
