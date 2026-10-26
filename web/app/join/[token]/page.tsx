import { apiFetch, ApiError } from "../../../lib/api";
import JoinForm from "./join-form";

type GuestEvent = {
  title: string;
  teacher_name: string;
  starts_at: string;
  ends_at: string;
  recorded: boolean;
};

const dateFmt = new Intl.DateTimeFormat("ru-RU", {
  dateStyle: "full",
  timeStyle: "short",
  timeZone: "UTC",
});

// Публичная страница события: человек ещё не залогинен и учётки у него
// не будет — он представляется именем и попадает в комнату (ADR-010).
export default async function JoinPage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params;

  let event: GuestEvent;
  try {
    event = await apiFetch<GuestEvent>(`/join/${encodeURIComponent(token)}`);
  } catch (e) {
    const over = e instanceof ApiError && e.status === 410;
    if (e instanceof ApiError && (e.status === 404 || over)) {
      return (
        <main style={pageStyle}>
          <h1>{over ? "Событие завершилось" : "Ссылка недействительна"}</h1>
          <p>
            {over
              ? "Это событие уже прошло, войти в него нельзя."
              : "Такого события нет — возможно, ссылку отозвали."}
          </p>
        </main>
      );
    }
    throw e;
  }

  return (
    <main style={pageStyle}>
      <h1>{event.title}</h1>
      <p>
        {dateFmt.format(new Date(event.starts_at))} (UTC) · ведёт {event.teacher_name}
      </p>
      {/* Гость попадает в запись и транскрипт — предупреждаем до того,
          как он представится (ADR-010) */}
      {event.recorded && (
        <p style={{ color: "#a60", border: "1px solid #eca", borderRadius: 8, padding: "0.75rem" }}>
          Событие записывается: видео и расшифровка сохранятся у организатора.
        </p>
      )}
      <JoinForm token={token} />
    </main>
  );
}

const pageStyle = {
  fontFamily: "system-ui, sans-serif",
  maxWidth: 420,
  margin: "5rem auto",
  padding: "0 1rem",
} as const;
