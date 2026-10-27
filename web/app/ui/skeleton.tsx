import { cx } from "./cx";

// Skeleton — заглушка на время загрузки (DESIGN §3.17): держит место под
// содержимое, чтобы страница не прыгала. Полосы скрыты от чтения с экрана,
// вместо них читается label; showLabel — показать его и глазами
// («Готовим запись…»).
export function Skeleton({
  variant = "text",
  lines = 3,
  label,
  showLabel = false,
  className,
}: {
  variant?: "text" | "card" | "player";
  lines?: number;
  label: string;
  showLabel?: boolean;
  className?: string;
}) {
  const count = variant === "text" ? lines : 1;
  return (
    <div className={cx("skeleton-group", className)} aria-busy="true">
      <span className={showLabel ? "small muted" : "sr-only"}>{label}</span>
      {Array.from({ length: count }, (_, i) => (
        <span key={i} className={cx("skeleton", `skeleton--${variant}`)} aria-hidden="true" />
      ))}
    </div>
  );
}

// Spinner — крутилка в кнопке или строке; label читается вместо неё.
export function Spinner({ label, size = "sm" }: { label: string; size?: "sm" | "lg" }) {
  return (
    <>
      <span className={cx("spinner", size === "lg" && "spinner--lg")} aria-hidden="true" />
      <span className="sr-only">{label}</span>
    </>
  );
}
