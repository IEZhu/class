"use client";

import { useRouter } from "next/navigation";
import { useEffect, useId, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";

import type { Group, Invite, User, UserWithGroups } from "../../lib/api";
import { roleLabels } from "../../lib/roles";
import { FormStatus, request, requestJson, useSubmit } from "../forms";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { cx } from "../ui/cx";
import { EmptyState } from "../ui/empty";
import { Field } from "../ui/field";
import { Icon } from "../ui/icons";
import { LocalTime } from "../ui/local-time";
import { PageHeader } from "../ui/page-header";
import { Pill } from "../ui/pill";
import styles from "./admin.module.css";

const CEFR_LEVELS = ["A1", "A2", "B1", "B2", "C1", "C2"] as const;

// Дата приглашения и архива — в поясе зрителя (DESIGN §5.4)
const shortDate: Intl.DateTimeFormatOptions = { dateStyle: "short" };

// Раскладка «Люди и группы» (DESIGN §6.11): шапка с «Пригласить», чипы
// разделов, люди таблицей (на телефоне — карточками) с раскрываемыми
// действиями, приглашения, группы карточками, архив.
export default function AdminPanel({
  me,
  users,
  archived,
  groups,
  invites,
}: {
  me: User;
  users: UserWithGroups[];
  archived: UserWithGroups[];
  groups: Group[];
  invites: Invite[];
}) {
  const router = useRouter();
  const reload = () => router.refresh();
  const isAdmin = me.role === "admin";
  // Преподаватель заводит только студентов — api вернёт 403 на остальное
  const assignableRoles: User["role"][] = isAdmin ? ["student", "teacher", "admin"] : ["student"];

  const [inviting, setInviting] = useState(false);
  const inviteId = useId();
  const inviteToggle = useRef<HTMLButtonElement>(null);

  // Чипы ведут к разделам страницы; приглашений и архива обычно нет —
  // тогда нет ни раздела, ни чипа
  const sections = [
    { id: "people", label: "Люди", count: users.length },
    ...(invites.length > 0 ? [{ id: "invites", label: "Приглашения", count: invites.length }] : []),
    { id: "groups", label: "Группы", count: groups.length },
    ...(archived.length > 0 ? [{ id: "archive", label: "Архив", count: archived.length }] : []),
  ];

  return (
    <>
      <PageHeader
        title="Люди и группы"
        meta={isAdmin ? "Админ: полный список людей и групп." : undefined}
        actions={
          <Button
            ref={inviteToggle}
            type="button"
            variant="primary"
            aria-expanded={inviting}
            aria-controls={inviteId}
            onClick={() => setInviting((v) => !v)}
          >
            <Icon name="plus" />
            Пригласить
          </Button>
        }
      />
      <div className="stack stack--lg">
        {!isAdmin && (
          <Alert tone="info" live={false}>
            Преподаватель: студенты ваших групп; заводить можно только студентов.
          </Alert>
        )}
        <nav aria-label="Разделы страницы" className="chip-row">
          {sections.map((s) => (
            <a key={s.id} href={`#${s.id}`} className="chip">
              {s.label}
              <span className={styles.count}>{s.count}</span>
            </a>
          ))}
        </nav>
        <InviteCard
          id={inviteId}
          open={inviting}
          roles={assignableRoles}
          groups={isAdmin ? groups : []}
          onClose={() => {
            setInviting(false);
            inviteToggle.current?.focus();
          }}
          onDone={reload}
        />
        <PeopleSection me={me} users={users} roles={assignableRoles} onDone={reload} />
        <PendingInvites invites={invites} onDone={reload} />
        <GroupsSection groups={groups} isAdmin={isAdmin} onDone={reload} />
        <ArchiveSection users={archived} onDone={reload} />
      </div>
    </>
  );
}

// SectionHead — заголовок раздела со счётчиком и действием справа.
function SectionHead({
  headingId,
  title,
  count,
  action,
}: {
  headingId: string;
  title: string;
  count: number;
  action?: ReactNode;
}) {
  return (
    <div className={styles.sectionHead}>
      <h2 id={headingId}>
        {title} <span className={styles.count}>{count}</span>
      </h2>
      {action}
    </div>
  );
}

function InviteCard({
  id,
  open,
  roles,
  groups,
  onClose,
  onDone,
}: {
  id: string;
  open: boolean;
  roles: User["role"][];
  groups: Group[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<User["role"]>(roles[0]);
  const [groupId, setGroupId] = useState("");
  // Ссылка показывается один раз: в БД лежит только хэш токена (ADR-008)
  const [link, setLink] = useState<string | null>(null);
  const titleId = useId();
  const firstField = useRef<HTMLInputElement>(null);

  // Открыли кнопкой «Пригласить» — фокус в первое поле формы
  useEffect(() => {
    if (open) firstField.current?.focus();
  }, [open]);

  const { busy, error, done, submit } = useSubmit(async () => {
    const created = await requestJson<Invite>("/api/invites", "POST", {
      name,
      email,
      role,
      group_id: groupId ? Number(groupId) : null,
    });
    setLink(created.url ?? null);
    setName("");
    setEmail("");
    // Группу тоже сбрасываем: иначе следующее приглашение молча уедет
    // в предыдущую группу
    setGroupId("");
    onDone();
  });

  // hidden, а не условный рендер: закрытая карточка хранит выпущенную ссылку
  return (
    <section id={id} hidden={!open} className="card" aria-labelledby={titleId}>
      <div className="card-head">
        <h2 id={titleId}>Пригласить по ссылке</h2>
        <span className="spacer" />
        <button type="button" className="icon-btn" aria-label="Закрыть приглашение" onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <form onSubmit={submit} className="card-pad stack">
        <div className={styles.formGrid}>
          <Field label="Имя">
            <input ref={firstField} className="input" value={name} onChange={(e) => setName(e.target.value)} required />
          </Field>
          <Field label="Email">
            <input
              className="input"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              autoComplete="off"
            />
          </Field>
          <Field label="Роль">
            <select className="select" value={role} onChange={(e) => setRole(e.target.value as User["role"])}>
              {roles.map((r) => (
                <option key={r} value={r}>
                  {roleLabels[r]}
                </option>
              ))}
            </select>
          </Field>
          {groups.length > 0 && (
            <Field label="Группа">
              <select className="select" value={groupId} onChange={(e) => setGroupId(e.target.value)}>
                <option value="">без группы</option>
                {groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                  </option>
                ))}
              </select>
            </Field>
          )}
        </div>
        <p className="hint">Ссылка одноразовая и живёт неделю. Человек сам придумает пароль — вы его не увидите.</p>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" blockOnPhone disabled={busy} busy={busy}>
            {busy ? "Готовим…" : "Выпустить ссылку"}
          </Button>
          <FormStatus error={error} done={done && !link} doneText="Ссылка выпущена." />
        </div>
      </form>
      {link && <IssuedLink key={link} link={link} onHide={() => setLink(null)} />}
    </section>
  );
}

function IssuedLink({ link, onHide }: { link: string; onHide: () => void }) {
  const [copied, setCopied] = useState(false);
  const inputId = useId();

  async function copy() {
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
    } catch {
      // Буфер недоступен (нет разрешения, http) — ссылка и так на экране
      setCopied(false);
    }
  }

  return (
    <div className={cx("card-foot", styles.issued)}>
      <div className="field">
        <label className="label" htmlFor={inputId}>
          Ссылка-приглашение
        </label>
        <div className="copy-field">
          <input id={inputId} className="input" readOnly value={link} onFocus={(e) => e.target.select()} />
          <Button type="button" variant="primary" onClick={copy}>
            <Icon name={copied ? "check" : "copy"} />
            {copied ? "Скопировано" : "Скопировать"}
          </Button>
          <Button type="button" variant="ghost" onClick={onHide}>
            Скрыть
          </Button>
        </div>
        <span className="sr-only" role="status">
          {copied ? "Ссылка скопирована" : ""}
        </span>
      </div>
    </div>
  );
}

function PendingInvites({ invites, onDone }: { invites: Invite[]; onDone: () => void }) {
  const titleId = useId();
  if (invites.length === 0) return null;

  return (
    <section id="invites" className="section" aria-labelledby={titleId}>
      <SectionHead headingId={titleId} title="Приглашения" count={invites.length} />
      <p className="small muted">Ждут перехода по ссылке.</p>
      <div className={cx("card", styles.tableCard)}>
        <div className="table-wrap">
          <table className={cx("table table--stack", styles.rows)}>
            <thead>
              <tr>
                <th scope="col">Имя</th>
                <th scope="col">Email</th>
                <th scope="col">Роль</th>
                <th scope="col">Группа</th>
                <th scope="col">Действует до</th>
                <th scope="col">
                  <span className="sr-only">Действия</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {invites.map((inv) => (
                <tr key={inv.id}>
                  <td className={styles.strong}>{inv.name}</td>
                  <td data-label="Email" className={styles.email}>
                    {inv.email}
                  </td>
                  <td data-label="Роль">
                    <Pill tone="accent">{roleLabels[inv.role]}</Pill>
                  </td>
                  <td data-label="Группа">{inv.group_name || <span className="muted">без группы</span>}</td>
                  <td data-label="Действует до" className={styles.nowrap}>
                    <LocalTime value={inv.expires_at} options={shortDate} />
                  </td>
                  <td className={styles.actionCell}>
                    <RevokeInviteButton invite={inv} onDone={onDone} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}

function RevokeInviteButton({ invite, onDone }: { invite: Invite; onDone: () => void }) {
  const { busy, error, submit } = useSubmit(async () => {
    await request(`/api/invites/${invite.id}`, "DELETE");
    onDone();
  });

  function confirmThenSubmit(e: FormEvent) {
    e.preventDefault();
    if (window.confirm(`Отозвать приглашение для ${invite.name}?`)) void submit(e);
  }

  return (
    <form onSubmit={confirmThenSubmit} className={styles.rowAction}>
      <Button
        type="submit"
        variant="danger-ghost"
        size="sm"
        className={styles.rowButton}
        disabled={busy}
        busy={busy}
        aria-label={`Отозвать приглашение для ${invite.name}`}
      >
        {busy ? "Отзываем…" : "Отозвать"}
      </Button>
      <FormStatus error={error} done={false} doneText="" />
    </form>
  );
}

function CreateUserForm({ id, roles, onDone }: { id: string; roles: User["role"][]; onDone: () => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<User["role"]>(roles[0]);
  const [password, setPassword] = useState("");
  const titleId = useId();

  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/users", "POST", { name, email, role, password });
    setName("");
    setEmail("");
    setPassword("");
    onDone();
  });

  return (
    <form id={id} onSubmit={submit} className="card" aria-labelledby={titleId}>
      <div className="card-head">
        <h3 id={titleId}>Завести сразу с паролем, без ссылки</h3>
      </div>
      <div className="card-pad stack">
        <div className={styles.formGrid}>
          <Field label="Имя">
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
          </Field>
          <Field label="Email">
            <input
              className="input"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              autoComplete="off"
            />
          </Field>
          <Field label="Роль">
            <select className="select" value={role} onChange={(e) => setRole(e.target.value as User["role"])}>
              {roles.map((r) => (
                <option key={r} value={r}>
                  {roleLabels[r]}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Стартовый пароль" hint="Не короче 8 символов.">
            <input
              className="input"
              type="text"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              minLength={8}
              autoComplete="off"
            />
          </Field>
        </div>
        {/* Запасной путь: пароль знает и пригласивший. Основной — ссылка,
            где человек задаёт пароль сам (ADR-008) */}
        <p className="hint">Пароль передайте лично — письма система не шлёт. Человек сменит его в своём кабинете.</p>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" blockOnPhone disabled={busy} busy={busy}>
            {busy ? "Заводим…" : "Завести"}
          </Button>
          <FormStatus error={error} done={done} doneText="Учётка заведена." />
        </div>
      </div>
    </form>
  );
}

// Поиск фильтрует уже загруженный список в браузере: запросов к api нет
function matches(u: User, query: string): boolean {
  const q = query.trim().toLocaleLowerCase("ru");
  return !q || u.name.toLocaleLowerCase("ru").includes(q) || u.email.toLocaleLowerCase("ru").includes(q);
}

function PeopleSection({
  me,
  users,
  roles,
  onDone,
}: {
  me: User;
  users: UserWithGroups[];
  roles: User["role"][];
  onDone: () => void;
}) {
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState("");
  const titleId = useId();
  const createId = useId();
  const shown = users.filter((u) => matches(u, query));

  return (
    <section id="people" className="section" aria-labelledby={titleId}>
      <SectionHead
        headingId={titleId}
        title="Люди"
        count={users.length}
        action={
          <Button
            type="button"
            aria-expanded={creating}
            aria-controls={createId}
            onClick={() => setCreating((v) => !v)}
          >
            <Icon name="plus" />
            Завести с паролем
          </Button>
        }
      />
      <div hidden={!creating}>
        <CreateUserForm id={createId} roles={roles} onDone={onDone} />
      </div>
      <div className={cx("card", styles.tableCard)}>
        {users.length === 0 ? (
          <EmptyState icon="people" title="Пока никого" titleAs="h3">
            Позовите людей кнопкой «Пригласить» вверху страницы.
          </EmptyState>
        ) : (
          <>
            <div className={cx("card-pad", styles.search)}>
              <Field label="Поиск по имени или email">
                <input
                  className="input"
                  type="search"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  autoComplete="off"
                />
              </Field>
              <p className="small muted" role="status">
                {query.trim() ? `Найдено: ${shown.length} из ${users.length}` : ""}
              </p>
            </div>
            {shown.length === 0 ? (
              <EmptyState icon="search" title="Никого не нашли" titleAs="h3">
                Проверьте написание или очистите поиск.
              </EmptyState>
            ) : (
              <div className="table-wrap">
                <table className={cx("table", styles.people)}>
                  <thead>
                    <tr>
                      <th scope="col">Имя</th>
                      <th scope="col">Email</th>
                      <th scope="col">Роль</th>
                      <th scope="col">Группы</th>
                      <th scope="col">
                        <span className="sr-only">Действия</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.map((u) => (
                      <PersonRows key={u.id} me={me} user={u} roles={roles} onDone={onDone} />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}

// Строка человека и под ней — раскрываемая строка действий (DESIGN §6.11).
function PersonRows({
  me,
  user: u,
  roles,
  onDone,
}: {
  me: User;
  user: UserWithGroups;
  roles: User["role"][];
  onDone: () => void;
}) {
  const [open, setOpen] = useState(false);
  const detailsId = useId();

  return (
    <>
      <tr className={cx(styles.person, open && styles.isOpen)}>
        <td className={cx(styles.strong, styles.cellName)}>{u.name}</td>
        <td className={cx(styles.email, styles.cellEmail)}>{u.email}</td>
        <td className={styles.cellRole}>
          <Pill tone="accent">{roleLabels[u.role]}</Pill>
        </td>
        <td className={styles.cellGroups}>
          {u.groups.length > 0 ? (
            <ul className={styles.tags} aria-label="Группы">
              {u.groups.map((g) => (
                <li key={g}>
                  <Pill>{g}</Pill>
                </li>
              ))}
            </ul>
          ) : (
            <span className="small muted">без группы</span>
          )}
        </td>
        <td className={cx(styles.actionCell, styles.cellToggle)}>
          <button
            type="button"
            className={cx("icon-btn", styles.toggle)}
            aria-expanded={open}
            aria-controls={detailsId}
            aria-label={`Действия: ${u.name}`}
            onClick={() => setOpen((v) => !v)}
          >
            <Icon name="down" />
          </button>
        </td>
      </tr>
      <tr id={detailsId} hidden={!open} className={styles.details}>
        <td colSpan={5}>
          <div className={styles.personActions}>
            <ResetPasswordForm userId={u.id} />
            {me.role === "admin" && u.id !== me.id && (
              <ChangeRoleForm userId={u.id} current={u.role} roles={roles} onDone={onDone} />
            )}
            {/* Себя в архив не отправить; преподавателю api даст только
                студентов его групп — других в его списке и нет */}
            {u.id !== me.id && <ArchiveButton user={u} archive onDone={onDone} />}
          </div>
        </td>
      </tr>
    </>
  );
}

// Архив вместо удаления (ADR-024): учётка и её история остаются, вход
// закрывается, все сессии гаснут сразу; вернуть можно в любой момент.
function ArchiveSection({ users, onDone }: { users: UserWithGroups[]; onDone: () => void }) {
  const titleId = useId();
  if (users.length === 0) return null;

  return (
    <section id="archive" className="section" aria-labelledby={titleId}>
      <SectionHead headingId={titleId} title="Архив" count={users.length} />
      <p className="small muted">Вход закрыт, история сохранена; вернуть можно в любой момент.</p>
      <div className={cx("card", styles.tableCard)}>
        <div className="table-wrap">
          <table className={cx("table table--stack", styles.rows)}>
            <thead>
              <tr>
                <th scope="col">Имя</th>
                <th scope="col">Email</th>
                <th scope="col">Роль</th>
                <th scope="col">В архиве с</th>
                <th scope="col">
                  <span className="sr-only">Действия</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id} className="is-archived">
                  <td className={styles.strong}>{u.name}</td>
                  <td data-label="Email" className={styles.email}>
                    {u.email}
                  </td>
                  <td data-label="Роль">
                    <Pill>{roleLabels[u.role]}</Pill>
                  </td>
                  <td data-label="В архиве с" className={styles.nowrap}>
                    {u.deactivated_at ? <LocalTime value={u.deactivated_at} options={shortDate} /> : "—"}
                  </td>
                  <td className={styles.actionCell}>
                    <ArchiveButton user={u} archive={false} onDone={onDone} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}

function ArchiveButton({ user, archive, onDone }: { user: User; archive: boolean; onDone: () => void }) {
  const { busy, error, submit } = useSubmit(async () => {
    await request(`/api/users/${user.id}/${archive ? "deactivate" : "reactivate"}`, "POST");
    onDone();
  });

  function confirmThenSubmit(e: FormEvent) {
    e.preventDefault();
    // Возврат безопасен, а архивация сразу выкидывает человека отовсюду
    if (!archive || window.confirm(`Отправить ${user.name} в архив? Вход закроется, все сессии завершатся.`)) {
      void submit(e);
    }
  }

  return (
    <form onSubmit={confirmThenSubmit} className={cx(styles.rowAction, archive && styles.archiveAction)}>
      {archive ? (
        <Button type="submit" variant="danger-ghost" disabled={busy} busy={busy}>
          {busy ? "Отправляем…" : "В архив"}
        </Button>
      ) : (
        <Button
          type="submit"
          size="sm"
          className={styles.rowButton}
          disabled={busy}
          busy={busy}
          aria-label={`Вернуть из архива: ${user.name}`}
        >
          {busy ? "Возвращаем…" : "Вернуть"}
        </Button>
      )}
      <FormStatus error={error} done={false} doneText="" />
    </form>
  );
}

// InlineForm — поле и кнопка в строку (на телефоне — друг под другом),
// итог отправки под ними.
function InlineForm({
  onSubmit,
  field,
  button,
  status,
  className,
}: {
  onSubmit: (e: FormEvent) => void;
  field: ReactNode;
  button: ReactNode;
  status: ReactNode;
  className?: string;
}) {
  return (
    <form onSubmit={onSubmit} className={cx(styles.inlineForm, className)}>
      <div className={styles.inlineRow}>
        {field}
        {button}
      </div>
      {status}
    </form>
  );
}

function ResetPasswordForm({ userId }: { userId: number }) {
  const [password, setPassword] = useState("");
  const { busy, error, done, submit } = useSubmit(async () => {
    await request(`/api/users/${userId}/password`, "POST", { password });
    setPassword("");
  });

  return (
    <InlineForm
      onSubmit={submit}
      field={
        <Field label="Новый пароль, от 8 символов" className={styles.grow}>
          <input
            className="input"
            type="text"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            minLength={8}
            autoComplete="off"
          />
        </Field>
      }
      button={
        <Button type="submit" disabled={busy} busy={busy}>
          {busy ? "Сбрасываем…" : "Сбросить пароль"}
        </Button>
      }
      status={<FormStatus error={error} done={done} doneText="Пароль сброшен, сессии закрыты." />}
    />
  );
}

function ChangeRoleForm({
  userId,
  current,
  roles,
  onDone,
}: {
  userId: number;
  current: User["role"];
  roles: User["role"][];
  onDone: () => void;
}) {
  const [role, setRole] = useState<User["role"]>(current);
  const { busy, error, done, submit } = useSubmit(async () => {
    await request(`/api/users/${userId}`, "PATCH", { role });
    onDone();
  });

  return (
    <InlineForm
      onSubmit={submit}
      field={
        <Field label="Роль" className={styles.grow}>
          <select className="select" value={role} onChange={(e) => setRole(e.target.value as User["role"])}>
            {roles.map((r) => (
              <option key={r} value={r}>
                {roleLabels[r]}
              </option>
            ))}
          </select>
        </Field>
      }
      button={
        <Button type="submit" disabled={busy || role === current} busy={busy}>
          {busy ? "Меняем…" : "Сменить роль"}
        </Button>
      }
      status={<FormStatus error={error} done={done} doneText="Роль обновлена." />}
    />
  );
}

function GroupsSection({
  groups,
  isAdmin,
  onDone,
}: {
  groups: Group[];
  isAdmin: boolean;
  onDone: () => void;
}) {
  const titleId = useId();

  return (
    <section id="groups" className="section" aria-labelledby={titleId}>
      <SectionHead headingId={titleId} title="Группы" count={groups.length} />
      {/* Состав групп меняет только админ (ADR-007) — преподавателю
          показываем список без форм, api ему всё равно ответит 403 */}
      {!isAdmin && (
        <p className="small muted">Состав групп меняет админ; вам группы видны для переклички на уроке.</p>
      )}
      {isAdmin && <CreateGroupForm onDone={onDone} />}
      {groups.length === 0 ? (
        <div className="card">
          <EmptyState icon="people" title="Групп пока нет" titleAs="h3">
            {isAdmin && "Создайте первую группу формой выше."}
          </EmptyState>
        </div>
      ) : (
        groups.map((g) => <GroupCard key={g.id} group={g} isAdmin={isAdmin} onDone={onDone} />)
      )}
    </section>
  );
}

function GroupCard({ group: g, isAdmin, onDone }: { group: Group; isAdmin: boolean; onDone: () => void }) {
  const titleId = useId();

  return (
    <article className={cx("card", styles.tableCard)} aria-labelledby={titleId}>
      <div className="card-head">
        <h3 id={titleId}>{g.name}</h3>
        <Pill tone="accent">{g.level}</Pill>
      </div>
      {g.members.length === 0 ? (
        <p className="card-pad muted">В группе пока никого.</p>
      ) : (
        <div className="table-wrap">
          <table className="table table--dense table--stack">
            <thead>
              <tr>
                <th scope="col">Имя</th>
                <th scope="col">Email</th>
                {isAdmin && (
                  <th scope="col">
                    <span className="sr-only">Действия</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {g.members.map((m) => (
                <tr key={m.user_id} className={m.archived ? "is-archived" : undefined}>
                  <td className={styles.strong}>
                    <span className={styles.nameLine}>
                      {m.name}
                      {m.archived && <Pill>в архиве</Pill>}
                    </span>
                  </td>
                  <td data-label="Email" className={styles.email}>
                    {m.email}
                  </td>
                  {isAdmin && (
                    <td className={styles.actionCell}>
                      <RemoveMemberButton groupId={g.id} userId={m.user_id} name={m.name} onDone={onDone} />
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {isAdmin && (
        <div className="card-foot">
          <AddMemberForm groupId={g.id} onDone={onDone} />
        </div>
      )}
    </article>
  );
}

function CreateGroupForm({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState("");
  const [level, setLevel] = useState<string>(CEFR_LEVELS[1]);
  const titleId = useId();
  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/groups", "POST", { name, level });
    setName("");
    onDone();
  });

  return (
    <form onSubmit={submit} className="card" aria-labelledby={titleId}>
      <div className="card-head">
        <h3 id={titleId}>Новая группа</h3>
      </div>
      <div className="card-pad stack stack--sm">
        <div className={styles.inlineRow}>
          <Field label="Название группы" className={styles.grow}>
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
          </Field>
          <Field label="Уровень" className={styles.level}>
            <select className="select" value={level} onChange={(e) => setLevel(e.target.value)}>
              {CEFR_LEVELS.map((l) => (
                <option key={l} value={l}>
                  {l}
                </option>
              ))}
            </select>
          </Field>
          <Button type="submit" variant="primary" disabled={busy} busy={busy}>
            {busy ? "Создаём…" : "Создать группу"}
          </Button>
        </div>
        <FormStatus error={error} done={done} doneText="Группа создана." />
      </div>
    </form>
  );
}

function AddMemberForm({ groupId, onDone }: { groupId: number; onDone: () => void }) {
  const [email, setEmail] = useState("");
  const { busy, error, done, submit } = useSubmit(async () => {
    await request(`/api/groups/${groupId}/members`, "POST", { email });
    setEmail("");
    onDone();
  });

  return (
    <InlineForm
      onSubmit={submit}
      className={styles.grow}
      field={
        <Field label="Добавить участника по email" className={styles.grow}>
          <input
            className="input"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoComplete="off"
          />
        </Field>
      }
      button={
        <Button type="submit" disabled={busy} busy={busy}>
          {busy ? "Добавляем…" : "Добавить"}
        </Button>
      }
      status={<FormStatus error={error} done={done} doneText="Добавлен." />}
    />
  );
}

function RemoveMemberButton({
  groupId,
  userId,
  name,
  onDone,
}: {
  groupId: number;
  userId: number;
  name: string;
  onDone: () => void;
}) {
  const { busy, error, submit } = useSubmit(async () => {
    await request(`/api/groups/${groupId}/members/${userId}`, "DELETE");
    onDone();
  });

  // Подтверждение: промах по кнопке иначе молча выкидывает человека из
  // группы, отмены нет.
  function confirmThenSubmit(e: FormEvent) {
    e.preventDefault();
    if (window.confirm(`Убрать ${name} из группы?`)) void submit(e);
  }

  return (
    <form onSubmit={confirmThenSubmit} className={styles.rowAction}>
      <Button
        type="submit"
        variant="danger-ghost"
        size="sm"
        className={styles.rowButton}
        disabled={busy}
        busy={busy}
        aria-label={`Убрать ${name} из группы`}
      >
        {busy ? "Убираем…" : "Убрать"}
      </Button>
      <FormStatus error={error} done={false} doneText="" />
    </form>
  );
}

