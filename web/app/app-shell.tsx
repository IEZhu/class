"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useCallback, useEffect, useId, useRef, useState } from "react";

import { roleLabels } from "../lib/roles";
import type { Role } from "../lib/roles";
import { Icon } from "./ui/icons";
import type { IconName } from "./ui/icons";

// Каркас авторизованных страниц (ADR-040, DESIGN §1): боковая панель от
// 1024px, верхняя панель на планшете, нижняя навигация с листом «Ещё» на
// телефоне; элементы каркаса — fixed. Ссылки — те же, что были в шапке
// (user-bar.tsx), только сгруппированы; текущий раздел — aria-current.

type NavItem = {
  href: string;
  label: string;
  // short — подпись в нижней навигации телефона, где на пункт ~78px
  short?: string;
  icon: IconName;
  // match — адреса, на которых пункт текущий (по умолчанию href), с подпутями
  match?: string[];
};

const lessons: NavItem = { href: "/lessons", label: "Уроки", icon: "lessons", match: ["/lessons", "/lesson"] };
const people: NavItem = { href: "/admin", label: "Люди и группы", short: "Люди", icon: "people" };
const dictionary: NavItem = { href: "/dictionary", label: "Словарь", icon: "dict" };
const cards: NavItem = { href: "/practice/cards", label: "Карточки", icon: "cards" };
const cloze: NavItem = { href: "/practice/cloze", label: "Вставь слово", short: "Вставь", icon: "cloze" };
// «Текст по словам» (/practice/text) открывается из «Текстов» — пункт тот же
const texts: NavItem = { href: "/practice/texts", label: "Тексты", icon: "text", match: ["/practice/texts", "/practice/text"] };
const video: NavItem = { href: "/practice/video", label: "Видео", icon: "video" };
const account: NavItem = { href: "/account", label: "Учётная запись", short: "Профиль", icon: "user" };

type RoleNav = {
  main: NavItem[];
  practice: NavItem[];
  // bottom — нижняя навигация телефона; more — пункты листа «Ещё» над
  // учётной записью и выходом, null — листа нет (у роли мало разделов)
  bottom: NavItem[];
  more: NavItem[] | null;
};

// «Люди и группы» — только тем, у кого api всё равно не ответит 403
// (админ, преподаватель); словарь, карточки и «вставь слово» — по личному
// словарю студента (S3-5, S4-3, S4-4/S4-5); тексты и видео — студенту и
// преподавателю (S5-5…S5-7). Гость видит только свои уроки.
const navByRole: Record<Role, RoleNav> = {
  student: {
    main: [lessons],
    practice: [dictionary, cards, cloze, texts, video],
    bottom: [lessons, dictionary, cards, cloze],
    more: [texts, video],
  },
  teacher: { main: [lessons, people], practice: [texts, video], bottom: [lessons, people, texts, video], more: [] },
  admin: { main: [lessons, people], practice: [], bottom: [lessons, people, account], more: null },
  guest: { main: [lessons], practice: [], bottom: [lessons, account], more: null },
};

function isCurrent(pathname: string, item: NavItem): boolean {
  return (item.match ?? [item.href]).some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

function initials(name: string): string {
  const letters = name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((w) => Array.from(w)[0] ?? "")
    .join("");
  return letters.toUpperCase() || "?";
}

function Brand() {
  return (
    <Link href="/lessons" className="brand">
      <span className="brand-mark" aria-hidden="true">
        L
      </span>
      <span>Lingua Class</span>
    </Link>
  );
}

function NavLinks({ items, pathname }: { items: NavItem[]; pathname: string }) {
  return items.map((item) => (
    <li key={item.href}>
      <Link href={item.href} className="nav-item" aria-current={isCurrent(pathname, item) ? "page" : undefined}>
        <Icon name={item.icon} />
        <span>{item.label}</span>
      </Link>
    </li>
  ));
}

export default function AppShell({ name, role }: { name: string; role: Role }) {
  const router = useRouter();
  const pathname = usePathname();
  const nav = navByRole[role];
  const avatar = initials(name);
  const practiceId = useId();

  async function logout() {
    // редирект в finally: даже при сетевой ошибке уводим на /login
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } finally {
      router.push("/login");
      router.refresh();
    }
  }

  return (
    <div className="app-shell">
      <div className="shell-sidebar">
        <Brand />
        <nav aria-label="Разделы">
          <ul className="nav-list">
            <NavLinks items={nav.main} pathname={pathname} />
          </ul>
          {nav.practice.length > 0 && (
            <>
              <div className="nav-label" id={practiceId}>
                Практика
              </div>
              <ul className="nav-list" aria-labelledby={practiceId}>
                <NavLinks items={nav.practice} pathname={pathname} />
              </ul>
            </>
          )}
        </nav>
        <div className="sidebar-user">
          <span className="avatar" aria-hidden="true">
            {avatar}
          </span>
          <Link href="/account" className="user-meta" aria-current={isCurrent(pathname, account) ? "page" : undefined}>
            <span className="sr-only">Учётная запись: </span>
            <b>{name}</b>
            <span>{roleLabels[role]}</span>
          </Link>
          <button type="button" className="icon-btn icon-btn--sm" aria-label="Выйти" title="Выйти" onClick={logout}>
            <Icon name="logout" />
          </button>
        </div>
      </div>

      <header className="shell-topbar">
        <Brand />
        <nav className="topbar-nav" aria-label="Разделы">
          <ul className="nav-list">
            <NavLinks items={[...nav.main, ...nav.practice]} pathname={pathname} />
          </ul>
        </nav>
        <AccountMenu name={name} role={role} avatar={avatar} pathname={pathname} onLogout={logout} />
      </header>

      <BottomNav nav={nav} name={name} role={role} avatar={avatar} pathname={pathname} onLogout={logout} />

      {/* Цель ссылки «К содержимому» (layout.tsx): сразу за навигацией */}
      <span id="content" className="skip-target" tabIndex={-1} />
    </div>
  );
}

type MenuProps = { name: string; role: Role; avatar: string; pathname: string; onLogout: () => void };

// Меню учётной записи в верхней панели (планшет и телефон): раскрывашка
// с кнопкой-аватаром; Escape и клик мимо закрывают, фокус — на кнопку.
function AccountMenu({ name, role, avatar, pathname, onLogout }: MenuProps) {
  const [open, setOpen] = useState(false);
  const menuId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => setOpen(false), [pathname]);

  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key !== "Escape") return;
      setOpen(false);
      buttonRef.current?.focus();
    }
    function onDown(e: PointerEvent) {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onDown);
    };
  }, [open]);

  return (
    <div className="account" ref={rootRef}>
      <button
        ref={buttonRef}
        type="button"
        className="account-btn"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        // на /account текущий пункт спрятан в меню — кнопка помечена текущей, как «Ещё»
        aria-current={isCurrent(pathname, account) ? "true" : undefined}
        aria-label={`${name}: учётная запись и выход`}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="avatar avatar--sm" aria-hidden="true">
          {avatar}
        </span>
      </button>
      {open && (
        <div id={menuId} className="account-menu">
          <div className="menu-head">
            <b>{name}</b>
            <span>{roleLabels[role]}</span>
          </div>
          <Link
            href={account.href}
            className="menu-item"
            aria-current={isCurrent(pathname, account) ? "page" : undefined}
            onClick={() => setOpen(false)}
          >
            <Icon name="user" />
            {account.label}
          </Link>
          <button type="button" className="menu-item" onClick={onLogout}>
            <Icon name="logout" />
            Выйти
          </button>
        </div>
      )}
    </div>
  );
}

// Нижняя навигация телефона; «Ещё» открывает лист с остальными разделами,
// учётной записью и выходом.
function BottomNav({ nav, ...menu }: MenuProps & { nav: RoleNav }) {
  const { pathname } = menu;
  const [sheetOpen, setSheetOpen] = useState(false);
  const moreRef = useRef<HTMLButtonElement>(null);
  const close = useCallback(() => {
    setSheetOpen(false);
    moreRef.current?.focus();
  }, []);

  useEffect(() => setSheetOpen(false), [pathname]);

  const more = nav.more;
  const moreCurrent = more !== null && [...more, account].some((item) => isCurrent(pathname, item));

  return (
    <>
      <nav className="shell-bottom" aria-label="Разделы">
        {nav.bottom.map((item) => (
          <Link
            key={item.href}
            href={item.href}
            className="bottom-item"
            aria-current={isCurrent(pathname, item) ? "page" : undefined}
          >
            <span className="nav-ic">
              <Icon name={item.icon} />
            </span>
            <span>{item.short ?? item.label}</span>
          </Link>
        ))}
        {more !== null && (
          <button
            ref={moreRef}
            type="button"
            className="bottom-item"
            // текущий раздел спрятан в листе — «Ещё» помечен текущим элементом набора
            aria-current={moreCurrent ? "true" : undefined}
            aria-haspopup="dialog"
            aria-expanded={sheetOpen}
            onClick={() => setSheetOpen(true)}
          >
            <span className="nav-ic">
              <Icon name="more" />
            </span>
            <span>Ещё</span>
          </button>
        )}
      </nav>
      {sheetOpen && more !== null && <MoreSheet items={more} onClose={close} {...menu} />}
    </>
  );
}

// Лист «Ещё»: модальный диалог, фокус внутри и по кругу, Escape, крестик
// и тап по фону закрывают, фокус возвращается на кнопку «Ещё».
function MoreSheet({ items, onClose, name, role, avatar, pathname, onLogout }: MenuProps & { items: NavItem[]; onClose: () => void }) {
  const titleId = useId();
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const sheet = ref.current;
    if (!sheet) return;
    sheet.querySelector<HTMLElement>(".menu-item")?.focus();
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
        return;
      }
      if (e.key !== "Tab" || !sheet) return;
      const focusable = [...sheet.querySelectorAll<HTMLElement>("a[href], button:not(:disabled)")];
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!first || !last) return;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="sheet-layer">
      <div className="backdrop" onClick={onClose} />
      <div ref={ref} className="sheet" role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <span className="sheet-handle" aria-hidden="true" />
        <div className="sheet-head">
          <h2 id={titleId} className="sheet-title">
            Ещё
          </h2>
          <span className="spacer" />
          <button type="button" className="icon-btn" aria-label="Закрыть" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        {items.length > 0 && (
          <ul className="nav-list">
            {items.map((item) => (
              <li key={item.href}>
                <Link href={item.href} className="menu-item" aria-current={isCurrent(pathname, item) ? "page" : undefined}>
                  <Icon name={item.icon} />
                  {item.label}
                </Link>
              </li>
            ))}
          </ul>
        )}
        <div className="sheet-user">
          <span className="avatar" aria-hidden="true">
            {avatar}
          </span>
          <span>
            <b>{name}</b>
            <span>{roleLabels[role]}</span>
          </span>
        </div>
        <Link href={account.href} className="menu-item" aria-current={isCurrent(pathname, account) ? "page" : undefined}>
          <Icon name="user" />
          {account.label}
        </Link>
        <button type="button" className="menu-item" onClick={onLogout}>
          <Icon name="logout" />
          Выйти
        </button>
      </div>
    </div>
  );
}
