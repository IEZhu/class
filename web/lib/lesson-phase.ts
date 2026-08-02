// Фаза отображения /lesson/{id}: state machine по статусу + времени
// (docs/architecture/flows/lesson-lifecycle.md). На этапе 0 статус в БД
// меняют только будущие вебхуки (S1-5/S2-3), поэтому live определяется
// временем при status=scheduled; ended — прошедший урок без записи
// (до появления Egress в S1-5).
export type LessonPhase = "scheduled" | "live" | "ended" | "processing" | "done";

export function lessonPhase(
  status: "scheduled" | "live" | "processing" | "done",
  startsAt: string,
  endsAt: string,
  now: Date = new Date(),
): LessonPhase {
  if (status !== "scheduled") return status;
  if (now < new Date(startsAt)) return "scheduled";
  if (now <= new Date(endsAt)) return "live";
  return "ended";
}
