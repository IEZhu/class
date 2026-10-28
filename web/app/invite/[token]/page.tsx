import type { Metadata } from "next";
import Link from "next/link";

import { apiFetch, ApiError } from "../../../lib/api";
import type { Invite } from "../../../lib/api";
import { roleLabels } from "../../../lib/roles";
import { buttonClass } from "../../ui/button";
import { EmptyState } from "../../ui/empty";
import AcceptForm from "./accept-form";

export const metadata: Metadata = { title: "Приглашение" };

// Публичная страница: приглашённый ещё не залогинен. Токен из URL уходит
// в api, дальше человек задаёт себе пароль (ADR-008). Вид — карточка
// страниц без сессии (DESIGN §6.1).
export default async function InvitePage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params;

  let invite: Invite;
  try {
    invite = await apiFetch<Invite>(`/signup/${encodeURIComponent(token)}`);
  } catch (e) {
    const used = e instanceof ApiError && e.status === 410;
    if (e instanceof ApiError && (e.status === 404 || used)) {
      return (
        <main className="auth-page">
          <div className="card card-pad">
            <EmptyState
              icon="warning"
              title="Ссылка недействительна"
              titleAs="h1"
              action={
                <Link href="/login" className={buttonClass({ variant: "secondary" })}>
                  Ко входу
                </Link>
              }
            >
              {used
                ? "Этой ссылкой уже воспользовались или истёк её срок."
                : "Такого приглашения нет."}{" "}
              Попросите пригласившего выпустить новую.
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
          <h1>Приглашение</h1>
          <p>
            {invite.inviter_name} приглашает вас как <strong>{roleLabels[invite.role]}</strong>
            {invite.group_name && <> в группу «{invite.group_name}»</>}.
          </p>
          <p className="muted small">{invite.email}</p>
        </div>
        <AcceptForm token={token} />
      </div>
    </main>
  );
}
