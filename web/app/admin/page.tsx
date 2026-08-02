import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { Group, User, UserWithGroups } from "../../lib/api";
import UserBar from "../user-bar";
import AdminPanel from "./admin-panel";

export default async function AdminPage() {
  let me: User;
  let users: UserWithGroups[];
  let groups: Group[];
  try {
    [me, users, groups] = await Promise.all([
      apiFetch<User>("/auth/me"),
      apiFetch<UserWithGroups[]>("/users"),
      apiFetch<Group[]>("/groups"),
    ]);
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    if (e instanceof ApiError && e.status === 403) {
      return (
        <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 640, margin: "4rem auto", padding: "0 1rem" }}>
          <h1>Нет доступа</h1>
          <p>Людьми и группами управляют админ и преподаватели.</p>
        </main>
      );
    }
    throw e;
  }

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 760, margin: "2rem auto", padding: "0 1rem" }}>
      <UserBar name={me.name} role={me.role} />
      <h1>Люди и группы</h1>
      <p style={{ color: "#666" }}>
        {me.role === "admin"
          ? "Админ: полный список людей и групп."
          : "Преподаватель: студенты ваших групп; заводить можно только студентов."}
      </p>
      <AdminPanel me={me} users={users} groups={groups} />
    </main>
  );
}
