"use client";

import { useSyncExternalStore } from "react";

// Время в часовом поясе зрителя (DESIGN §5.4). Сервер пояса зрителя не
// знает и печатает UTC; после гидратации тот же <time> перерисовывается
// в поясе браузера. Расхождение текста сервера и браузера ожидаемо
// (разные пояса и версии ICU), поэтому у <time> suppressHydrationWarning.

const UTC = "UTC";
const utcAliases = new Set(["UTC", "Etc/UTC", "Etc/GMT", "GMT", "Etc/UCT", "UCT", "Etc/Universal", "Etc/Zulu"]);

// Пояс браузера не меняется за время жизни страницы: он считается один
// раз, подписка пустая
let viewerZone: string | null = null;

function browserZone(): string {
  if (viewerZone === null) {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    viewerZone = zone && !utcAliases.has(zone) ? zone : UTC;
  }
  return viewerZone;
}

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

// Форматтеры — на модуль: new Intl.DateTimeFormat в десятки раз дороже
// format(), а страница печатает одно и то же время во многих строках.
// Ключ — пояс и опции; наборов опций в коде несколько, поясов — два
// (UTC на сервере, пояс зрителя в браузере), поэтому Map не растёт.
const formatters = new Map<string, Intl.DateTimeFormat>();

function formatter(options: Intl.DateTimeFormatOptions, timeZone: string): Intl.DateTimeFormat {
  const key = `${timeZone} ${JSON.stringify(options)}`;
  let fmt = formatters.get(key);
  if (!fmt) {
    fmt = new Intl.DateTimeFormat("ru-RU", { ...options, timeZone });
    formatters.set(key, fmt);
  }
  return fmt;
}

function format(value: string, end: string | undefined, options: Intl.DateTimeFormatOptions, timeZone: string): string {
  const fmt = formatter(options, timeZone);
  return end ? fmt.formatRange(new Date(value), new Date(end)) : fmt.format(new Date(value));
}

// useLocalTimeText — момент value текстом в поясе зрителя, для подписей и
// title, где <time> не поставить; пока пояс — UTC (сервер, гидратация), с
// припиской «UTC». Узел с этим текстом — с suppressHydrationWarning
export function useLocalTimeText(value: string, options: Intl.DateTimeFormatOptions): string {
  const zone = useViewerTimeZone();
  const text = format(value, undefined, options, zone);
  return zone === UTC ? `${text} UTC` : text;
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
