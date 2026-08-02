// Роли и их подписи. Отдельно от lib/api.ts: тот тянет next/headers и
// потому доступен только Server Components, а подписи ролей нужны и
// клиентским компонентам (шапка, админ-кабинет).
export type Role = "admin" | "teacher" | "student";

export const roleLabels: Record<Role, string> = {
  admin: "админ",
  teacher: "преподаватель",
  student: "студент",
};
