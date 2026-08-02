import { apiFetch, ApiError } from "../../../lib/api";
import type { Invite } from "../../../lib/api";
import { roleLabels } from "../../../lib/roles";
import AcceptForm from "./accept-form";

// Публичная страница: приглашённый ещё не залогинен. Токен из URL уходит
// в api, дальше человек задаёт себе пароль (ADR-008).
export default async function InvitePage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params;

  let invite: Invite;
  try {
    invite = await apiFetch<Invite>(`/signup/${encodeURIComponent(token)}`);
  } catch (e) {
    const used = e instanceof ApiError && e.status === 410;
    if (e instanceof ApiError && (e.status === 404 || used)) {
      return (
        <main style={pageStyle}>
          <h1>Ссылка недействительна</h1>
          <p>
            {used
              ? "Этой ссылкой уже воспользовались или истёк её срок."
              : "Такого приглашения нет."}{" "}
            Попросите пригласившего выпустить новую.
          </p>
        </main>
      );
    }
    throw e;
  }

  return (
    <main style={pageStyle}>
      <h1>Lingua Class</h1>
      <p>
        {invite.inviter_name} приглашает вас как <strong>{roleLabels[invite.role]}</strong>
        {invite.group_name && <> в группу «{invite.group_name}»</>}.
      </p>
      <p style={{ color: "#666" }}>{invite.email}</p>
      <AcceptForm token={token} />
    </main>
  );
}

const pageStyle = {
  fontFamily: "system-ui, sans-serif",
  maxWidth: 400,
  margin: "5rem auto",
  padding: "0 1rem",
} as const;
