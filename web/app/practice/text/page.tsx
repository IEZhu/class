import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../../lib/api";
import type { GeneratedText, User } from "../../../lib/api";
import { ClickableTextScope } from "../../clickable-text";
import RoleRestricted from "../../role-restricted";
import { EmptyState } from "../../ui/empty";
import { PageHeader } from "../../ui/page-header";
import UserBar from "../../user-bar";
import GenerateForm from "./generate-form";
import GeneratedTextView from "./generated-text";
import t from "./text.module.css";

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
  if (me.role !== "student" && me.role !== "teacher") {
    return (
      <RoleRestricted me={me} title="Текст по словам" icon="text">
        Генерация текстов доступна студентам и преподавателям.
      </RoleRestricted>
    );
  }
  const student = me.role === "student";
  const texts = await apiFetch<GeneratedText[]>("/practice/texts");

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page page--narrow">
        <PageHeader title="Текст по словам" crumb={{ href: "/practice/texts", label: "Тексты для практики" }}>
          <p className="muted">
            Модель пишет текст, где каждое слово встречается хотя бы дважды; текст сохраняется, если проверка это
            подтвердила. Готовые тексты и предложения по вашим словам — в{" "}
            <Link href="/practice/texts">подборке для практики</Link>.
          </p>
        </PageHeader>
        <ClickableTextScope studentId={student ? me.id : null}>
          <GenerateForm fromDictionary={student} />
          <section className={t.history} aria-labelledby="my-texts">
            <h2 id="my-texts">Мои тексты</h2>
            {texts.length === 0 && (
              <div className="card">
                <EmptyState icon="text" title="Пока нет текстов" titleAs="p" />
              </div>
            )}
            {texts.map((text) => (
              <GeneratedTextView key={text.id} text={text} />
            ))}
          </section>
        </ClickableTextScope>
      </main>
    </>
  );
}
