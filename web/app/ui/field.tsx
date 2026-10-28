import { cloneElement, useId } from "react";
import type { ReactElement, ReactNode } from "react";

import { cx } from "./cx";
import { Icon } from "./icons";

type ControlProps = {
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean | "true" | "false";
};

// Field — подпись, контрол, подсказка и ошибка (DESIGN §3.3). Контрол —
// единственный ребёнок (<input className="input">, <select className="select">,
// <textarea className="textarea">); Field дописывает ему id, связь
// aria-describedby с подсказкой и ошибкой и aria-invalid при ошибке.
// Подпись видна всегда: hideLabel — только там, где смысл ясен из
// окружения (поле ответа cloze), подпись тогда остаётся для чтения с экрана.
export function Field({
  label,
  hint,
  error,
  hideLabel = false,
  className,
  children,
}: {
  label: ReactNode;
  hint?: ReactNode;
  error?: string | null;
  hideLabel?: boolean;
  className?: string;
  children: ReactElement<ControlProps>;
}) {
  const autoId = useId();
  const id = children.props.id ?? `field${autoId}`;
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [children.props["aria-describedby"], hintId, errorId].filter(Boolean).join(" ") || undefined;

  return (
    <div className={cx("field", className)}>
      <label className={hideLabel ? "sr-only" : "label"} htmlFor={id}>
        {label}
      </label>
      {cloneElement(children, {
        id,
        "aria-describedby": describedBy,
        "aria-invalid": error ? true : children.props["aria-invalid"],
      })}
      {hint && (
        <p id={hintId} className="hint">
          {hint}
        </p>
      )}
      {error && (
        <p id={errorId} className="field-error">
          <Icon name="error" size="sm" />
          <span>{error}</span>
        </p>
      )}
    </div>
  );
}
