// Роли и их подписи. Отдельно от lib/api.ts: тот тянет next/headers и
// потому доступен только Server Components, а подписи ролей нужны и
// клиентским компонентам (шапка, админ-кабинет).
// guest — гость открытого события (ADR-010): учётка без пароля на одно событие.
export type Role = "admin" | "teacher" | "student" | "guest";

export const roleLabels: Record<Role, string> = {
  admin: "админ",
  teacher: "преподаватель",
  student: "студент",
  guest: "гость",
};
