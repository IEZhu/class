import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { apiFetch, ApiError } from "../../lib/api";
import type { GoogleCalendarStatus, User } from "../../lib/api";
import { roleLabels } from "../../lib/roles";
import { cx } from "../ui/cx";
import { Icon } from "../ui/icons";
import { PageHeader } from "../ui/page-header";
import { Pill } from "../ui/pill";
import UserBar from "../user-bar";
import AccountForms from "./account-forms";
import styles from "./account.module.css";
import GoogleCalendar from "./google-calendar";

export const metadata: Metadata = { title: "Учётная запись" };

export default async function AccountPage({
  searchParams,
}: {
  // ?google= — итог подключения: сюда api возвращает с экрана согласия Google
  searchParams: Promise<{ google?: string }>;
}) {
  let me: User;
  try {
    me = await apiFetch<User>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
  // Календарь подключает только преподаватель: уроки создаёт он (ADR-023)
  const calendar = me.role === "teacher" ? await apiFetch<GoogleCalendarStatus>("/auth/google") : null;
  const { google } = await searchParams;

  return (
    <>
      <UserBar name={me.name} role={me.role} />
      <main className="page page--narrow">
        <PageHeader title="Моя учётная запись">
          {/* email и роль себе не меняют: email — логин, роль выдаёт админ (ADR-007) */}
          <div className={styles.profile}>
            <span className={cx("avatar", styles.avatar)} aria-hidden="true">
              <Icon name="user" />
            </span>
            <div className={styles.who}>
              <p className={styles.nameRow}>
                <span className={styles.name}>{me.name}</span>
                <Pill tone="accent">{roleLabels[me.role]}</Pill>
              </p>
              <p className={styles.email}>{me.email}</p>
            </div>
          </div>
        </PageHeader>
        <div className="stack">
          <AccountForms name={me.name} />
          {calendar && <GoogleCalendar status={calendar} result={google} />}
        </div>
      </main>
    </>
  );
}
