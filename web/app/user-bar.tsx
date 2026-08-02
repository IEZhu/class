"use client";

import { useRouter } from "next/navigation";

// Шапка авторизованных страниц: кто вошёл + выход.
export default function UserBar({ name, role }: { name: string; role: string }) {
  const router = useRouter();

  async function logout() {
    await fetch("/api/auth/logout", { method: "POST" });
    router.push("/login");
    router.refresh();
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
      <a href="/lessons" style={{ fontWeight: 600, textDecoration: "none", color: "inherit" }}>
        Lingua Class
      </a>
      <span>
        {name} · {role === "teacher" ? "преподаватель" : "студент"}{" "}
        <button onClick={logout} style={{ marginLeft: "0.75rem" }}>
          Выйти
        </button>
      </span>
    </header>
  );
}
