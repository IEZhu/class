import type { ComponentPropsWithRef } from "react";

import { cx } from "./cx";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger" | "danger-ghost";
export type ButtonSize = "sm" | "md" | "lg";

export type ButtonLook = {
  variant?: ButtonVariant;
  size?: ButtonSize;
  // block — во всю ширину; blockOnPhone — только на телефоне, с высотой 52px
  block?: boolean;
  blockOnPhone?: boolean;
  className?: string;
};

// buttonClass — классы кнопки для элементов, которые не <button>: ссылка
// <Link className={buttonClass({ variant: "primary" })}> или <label> файла.
export function buttonClass({ variant = "secondary", size = "md", block, blockOnPhone, className }: ButtonLook = {}): string {
  return cx("btn", `btn--${variant}`, size !== "md" && `btn--${size}`, block && "btn--block", blockOnPhone && "btn--block-sm", className);
}

type ButtonProps = Omit<ComponentPropsWithRef<"button">, "type" | "className"> &
  ButtonLook & {
    // type обязателен: у <button> по умолчанию submit, и кнопка без type
    // внутри формы отправила бы её
    type: "button" | "submit" | "reset";
    // busy — идёт действие: aria-busy; текст «Сохраняем…» и disabled задаёт вызывающий
    busy?: boolean;
  };

// Button — кнопка дизайн-системы (DESIGN §3.1).
export function Button({ variant, size, block, blockOnPhone, className, busy, ...rest }: ButtonProps) {
  return (
    <button
      {...rest}
      className={buttonClass({ variant, size, block, blockOnPhone, className })}
      aria-busy={busy || undefined}
    />
  );
}
