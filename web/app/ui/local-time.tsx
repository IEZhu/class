"use client";

import { useSyncExternalStore } from "react";

// Время в часовом поясе зрителя (DESIGN §5.4). Сервер пояса зрителя не
// знает и печатает UTC; после гидратации тот же <time> перерисовывается
// в поясе браузера. Расхождение текста сервера и браузера ожидаемо
// (разные пояса и версии ICU), поэтому у <time> suppressHydrationWarning.

const UTC = "UTC";
const utcAliases = new Set(["UTC", "Etc/UTC", "Etc/GMT", "GMT", "Etc/UCT", "UCT", "Etc/Universal", "Etc/Zulu"]);

function browserZone(): string {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return zone && !utcAliases.has(zone) ? zone : UTC;
}

// Пояс браузера не меняется за время жизни страницы: подписка пустая
function subscribe(): () => void {
  return () => {};
}

function serverZone(): string {
  return UTC;
}

// useViewerTimeZone — IANA-пояс зрителя; на сервере и при гидратации — "UTC".
export function useViewerTimeZone(): string {
  return useSyncExternalStore(subscribe, browserZone, serverZone);
}

function format(value: string, end: string | undefined, options: Intl.DateTimeFormatOptions, timeZone: string): string {
  const fmt = new Intl.DateTimeFormat("ru-RU", { ...options, timeZone });
  return end ? fmt.formatRange(new Date(value), new Date(end)) : fmt.format(new Date(value));
}

// LocalTime — момент value (ISO 8601) или промежуток value–end. options —
// как у Intl.DateTimeFormat, без timeZone. utcLabel — дописывать «UTC», пока
// время показано по UTC; в поясе зрителя подпись не нужна, UTC — в title.
export function LocalTime({
  value,
  end,
  options = { dateStyle: "medium", timeStyle: "short" },
  utcLabel = true,
}: {
  value: string;
  end?: string;
  options?: Intl.DateTimeFormatOptions;
  utcLabel?: boolean;
}) {
  const zone = useViewerTimeZone();
  const text = format(value, end, options, zone);
  const isUtc = zone === UTC;
  return (
    <time dateTime={value} title={isUtc ? undefined : `${format(value, end, options, UTC)} UTC`} suppressHydrationWarning>
      {isUtc && utcLabel ? `${text} UTC` : text}
    </time>
  );
}
