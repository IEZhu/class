import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { User } from "../../lib/api";
import { roleLabels } from "../../lib/roles";
import UserBar from "../user-bar";
import AccountForms from "./account-forms";

export default async function AccountPage() {
  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>Моя учётная запись</h1>
      <p style={{ color: "#666" }}>
        {me.email} · {roleLabels[me.role]}
      </p>
      {/* email и роль себе не меняют: email — логин, роль выдаёт админ (ADR-007) */}
      <AccountForms name={me.name} />
    </main>
  );
}
