import type { ReactNode } from "react";

import { phaseTitles } from "../../lib/lesson-phase";
import type { LessonPhase } from "../../lib/lesson-phase";
import { cx } from "./cx";

export type PillTone = LessonPhase | "plain" | "accent" | "success" | "danger";

// Pill — статус или бейдж (DESIGN §3.11–3.12): текст есть всегда, цвет его
// только дополняет. Тона фаз урока совпадают с LessonPhase.
export function Pill({ tone = "plain", children, className }: { tone?: PillTone; children: ReactNode; className?: string }) {
  return <span className={cx("pill", `pill--${tone}`, className)}>{children}</span>;
}

// PhasePill — фаза урока русской подписью (lib/lesson-phase.ts), не сырой статус api.
export function PhasePill({ phase }: { phase: LessonPhase }) {
  return <Pill tone={phase}>{phaseTitles[phase]}</Pill>;
}
