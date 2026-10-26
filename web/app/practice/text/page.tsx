import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { GeneratedText, User } from "../../../lib/api";
import UserBar from "../../user-bar";
import GenerateForm from "./generate-form";
import GeneratedTextView from "./generated-text";

// S5-6: учебный текст по целевым словам от LLM и история своих текстов.
// Генерация — студенту и преподавателю (каждый вызов стоит денег).
export default async function PracticeTextPage() {
  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  const allowed = me.role === "student" || me.role === "teacher";
  const texts = allowed ? await apiFetch<GeneratedText[]>("/practice/texts") : [];

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>Текст по словам</h1>
      {allowed ? (
        <>
          <p style={{ color: "#666" }}>
            Модель пишет текст, где каждое слово встречается хотя бы дважды; текст сохраняется, если проверка это
            подтвердила.
          </p>
          <GenerateForm />
          <h2>Мои тексты</h2>
          {texts.length === 0 && <p>Пока нет текстов.</p>}
          <div style={{ display: "grid", gap: "0.75rem" }}>
            {texts.map((t) => (
              <GeneratedTextView key={t.id} text={t} />
            ))}
          </div>
        </>
      ) : (
        <p>Генерация текстов доступна студентам и преподавателям.</p>
      )}
    </main>
  );
}
