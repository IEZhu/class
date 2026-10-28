import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { Group, Invite, User, UserWithGroups } from "../../lib/api";
import { buttonClass } from "../ui/button";
import { EmptyState } from "../ui/empty";
import UserBar from "../user-bar";
import AdminPanel from "./admin-panel";

export const metadata: Metadata = { title: "Люди и группы" };

export default async function AdminPage() {
  let me: User;
  let users: UserWithGroups[];
  let archived: UserWithGroups[];
  let groups: Group[];
  let invites: Invite[];
  try {
    [me, users, archived, groups, invites] = await Promise.all([
      apiFetch<User>("/auth/me"),
      apiFetch<UserWithGroups[]>("/users"),
      // Архив — отдельным списком: в основном его нет (ADR-024)
      apiFetch<UserWithGroups[]>("/users?archived=1"),
      apiFetch<Group[]>("/groups"),
      apiFetch<Invite[]>("/invites"),
    ]);
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    // Роль без доступа (студент, гость): карточка как у 404 — каркаса нет,
    // имени и роли из упавшего Promise.all не узнать
    if (e instanceof ApiError && e.status === 403) {
      return (
        <main className="auth-page">
          <div className="card card-pad">
            <EmptyState
              icon="people"
              title="Нет доступа"
              titleAs="h1"
              action={
                <Link href="/lessons" className={buttonClass({ variant: "primary" })}>
                  К урокам
                </Link>
              }
            >
              Людьми и группами управляют админ и преподаватели.
            </EmptyState>
          </div>
        </main>
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
