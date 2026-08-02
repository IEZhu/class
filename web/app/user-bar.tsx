"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";

import { roleLabels } from "../lib/roles";
import type { Role } from "../lib/roles";

// Шапка авторизованных страниц: кто вошёл, ссылки по роли, выход.
export default function UserBar({ name, role }: { name: string; role: Role }) {
  const router = useRouter();

  async function logout() {
    // редирект в finally: даже при сетевой ошибке уводим на /login
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } finally {
      router.push("/login");
      router.refresh();
    }
  }

  return (
    <header
      style={{
        display: "flex",
        justifyContent: "space-between",
        alignItems: "center",
        borderBottom: "1px solid #ddd",
        paddingBottom: "0.75rem",
        marginBottom: "1.5rem",
      }}
    >
      <Link href="/lessons" style={{ fontWeight: 600, textDecoration: "none", color: "inherit" }}>
        Lingua Class
      </Link>
      <span>
        {/* Админка — только тем, у кого api всё равно не ответит 403 */}
        {role !== "student" && (
          <Link href="/admin" style={{ marginRight: "0.75rem" }}>
            Люди и группы
          </Link>
        )}
        <Link href="/account" style={{ marginRight: "0.75rem" }}>
          {name}
        </Link>
        · {roleLabels[role]}{" "}
        <button onClick={logout} style={{ marginLeft: "0.75rem" }}>
          Выйти
        </button>
      </span>
    </header>
  );
}
