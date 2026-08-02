"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";

import type { User } from "../lib/api";

// Шапка авторизованных страниц: кто вошёл + выход.
export default function UserBar({ name, role }: { name: string; role: User["role"] }) {
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
        {name} · {role === "teacher" ? "преподаватель" : "студент"}{" "}
        <button onClick={logout} style={{ marginLeft: "0.75rem" }}>
          Выйти
        </button>
      </span>
    </header>
  );
}
