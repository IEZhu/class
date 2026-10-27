import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { GeneratedText, User } from "../../../lib/api";
import { ClickableTextScope } from "../../clickable-text";
import UserBar from "../../user-bar";
import GenerateForm from "./generate-form";
import GeneratedTextView from "./generated-text";

export const metadata: Metadata = { title: "Текст по словам" };

// S5-6: учебный текст по целевым словам от LLM и история своих текстов.
// Генерация — студенту и преподавателю (каждый вызов стоит денег).
// Тексты кликаются; раскраска, «в мой словарь» и слова из словаря по
// умолчанию — у студента (S3-1, S3-5).
export default async function PracticeTextPage() {
  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  const student = me.role === "student";
  const allowed = student || me.role === "teacher";
  const texts = allowed ? await apiFetch<GeneratedText[]>("/practice/texts") : [];

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>Текст по словам</h1>
      {allowed ? (
        <ClickableTextScope studentId={student ? me.id : null}>
          <p style={{ color: "#666" }}>
            Модель пишет текст, где каждое слово встречается хотя бы дважды; текст сохраняется, если проверка это
            подтвердила. Готовые тексты и предложения по вашим словам — в{" "}
            <Link href="/practice/texts">подборке для практики</Link>.
          </p>
          <GenerateForm fromDictionary={student} />
          <h2>Мои тексты</h2>
          {texts.length === 0 && <p>Пока нет текстов.</p>}
          <div style={{ display: "grid", gap: "0.75rem" }}>
            {texts.map((t) => (
              <GeneratedTextView key={t.id} text={t} />
            ))}
          </div>
        </ClickableTextScope>
      ) : (
        <p>Генерация текстов доступна студентам и преподавателям.</p>
      )}
    </main>
  );
}
