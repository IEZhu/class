import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { Group, Invite, User, UserWithGroups } from "../../lib/api";
import RoleRestricted from "../role-restricted";
import UserBar from "../user-bar";
import AdminPanel from "./admin-panel";

export const metadata: Metadata = { title: "Люди и группы" };

export default async function AdminPage() {
  // Кто зритель — отдельным запросом в том же Promise.all: при 403 списков
  // (студент, гость) он нужен для каркаса страницы
  const meRequest = apiFetch<User>("/auth/me");
  let me: User;
  let users: UserWithGroups[];
  let archived: UserWithGroups[];
  let groups: Group[];
  let invites: Invite[];
  try {
    [me, users, archived, groups, invites] = await Promise.all([
      meRequest,
      apiFetch<UserWithGroups[]>("/users"),
      // Архив — отдельным списком: в основном его нет (ADR-024)
      apiFetch<UserWithGroups[]>("/users?archived=1"),
      apiFetch<Group[]>("/groups"),
      apiFetch<Invite[]>("/invites"),
    ]);
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    // Роль без доступа (студент, гость): каркас и карточка, как у других
    // недоступных роли страниц
    if (e instanceof ApiError && e.status === 403) {
      return (
        <RoleRestricted me={await meRequest} title="Люди и группы" icon="people">
          Людьми и группами управляют админ и преподаватели.
        </RoleRestricted>
      );
    }
    throw e;
  }

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page">
        <AdminPanel me={me} users={users} archived={archived} groups={groups} invites={invites} />
      </main>
    </>
  );
}
