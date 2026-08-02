"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { FormEvent } from "react";

import type { Group, Invite, User, UserWithGroups } from "../../lib/api";
import { roleLabels } from "../../lib/roles";
import { FormStatus, request, requestJson, useSubmit } from "../forms";

const CEFR_LEVELS = ["A1", "A2", "B1", "B2", "C1", "C2"] as const;

const fieldStyle = { padding: "0.5rem" };
const sectionStyle = {
  border: "1px solid #ddd",
  borderRadius: 8,
  padding: "1rem",
  margin: "1rem 0",
} as const;
const rowStyle = { display: "flex", gap: "0.5rem", flexWrap: "wrap", alignItems: "center" } as const;

export default function AdminPanel({
  me,
  users,
  groups,
  invites,
}: {
  me: User;
  users: UserWithGroups[];
  groups: Group[];
  invites: Invite[];
}) {
  const router = useRouter();
  const reload = () => router.refresh();
  const isAdmin = me.role === "admin";
  // Преподаватель заводит только студентов — api вернёт 403 на остальное
  const assignableRoles: User["role"][] = isAdmin ? ["student", "teacher", "admin"] : ["student"];

  return (
    <>
      <InviteSection
        roles={assignableRoles}
        groups={isAdmin ? groups : []}
        invites={invites}
        onDone={reload}
      />
      <PeopleSection me={me} users={users} roles={assignableRoles} onDone={reload} />
      <GroupsSection groups={groups} isAdmin={isAdmin} onDone={reload} />
    </>
  );
}

function InviteSection({
  roles,
  groups,
  invites,
  onDone,
}: {
  roles: User["role"][];
  groups: Group[];
  invites: Invite[];
  onDone: () => void;
}) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<User["role"]>(roles[0]);
  const [groupId, setGroupId] = useState("");
  // Ссылка показывается один раз: в БД лежит только хэш токена (ADR-008)
  const [link, setLink] = useState<string | null>(null);

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
    onDone();
  });

  return (
    <section style={sectionStyle}>
      <h2 style={{ marginTop: 0 }}>Позвать по ссылке</h2>
      <form onSubmit={submit} style={rowStyle}>
        <input placeholder="Имя" value={name} onChange={(e) => setName(e.target.value)} required style={fieldStyle} />
        <input
          type="email"
          placeholder="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          required
          style={fieldStyle}
        />
        <select value={role} onChange={(e) => setRole(e.target.value as User["role"])} style={fieldStyle}>
          {roles.map((r) => (
            <option key={r} value={r}>
              {roleLabels[r]}
            </option>
          ))}
        </select>
        {groups.length > 0 && (
          <select value={groupId} onChange={(e) => setGroupId(e.target.value)} style={fieldStyle}>
            <option value="">без группы</option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </select>
        )}
        <button type="submit" disabled={busy}>
          {busy ? "Готовим…" : "Выпустить ссылку"}
        </button>
        <FormStatus error={error} done={done && !link} doneText="Ссылка выпущена." />
      </form>
      {link && <IssuedLink link={link} onHide={() => setLink(null)} />}
      <p style={{ color: "#666", margin: "0.5rem 0 0" }}>
        Ссылка одноразовая и живёт неделю. Человек сам придумает пароль — вы его не увидите.
      </p>
      <PendingInvites invites={invites} onDone={onDone} />
    </section>
  );
}

function IssuedLink({ link, onHide }: { link: string; onHide: () => void }) {
  const [copied, setCopied] = useState(false);

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
    <div style={{ ...rowStyle, marginTop: "0.75rem" }}>
      <input readOnly value={link} onFocus={(e) => e.target.select()} style={{ ...fieldStyle, minWidth: "22rem" }} />
      <button type="button" onClick={copy}>
        {copied ? "Скопировано" : "Скопировать"}
      </button>
      <button type="button" onClick={onHide}>
        Скрыть
      </button>
    </div>
  );
}

function PendingInvites({ invites, onDone }: { invites: Invite[]; onDone: () => void }) {
  if (invites.length === 0) return null;

  return (
    <div style={{ marginTop: "1rem" }}>
      <h3 style={{ marginBottom: "0.5rem" }}>Ждут перехода ({invites.length})</h3>
      <ul style={{ listStyle: "none", padding: 0, margin: 0, display: "grid", gap: "0.35rem" }}>
        {invites.map((inv) => (
          <li key={inv.id} style={rowStyle}>
            {inv.name} · {inv.email} · {roleLabels[inv.role]}
            {inv.group_name && <> · {inv.group_name}</>}
            <span style={{ color: "#666" }}>до {new Date(inv.expires_at).toLocaleDateString("ru-RU")}</span>
            <RevokeInviteButton invite={inv} onDone={onDone} />
          </li>
        ))}
      </ul>
    </div>
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
    <form onSubmit={confirmThenSubmit} style={{ display: "inline" }}>
      <button type="submit" disabled={busy} aria-label={`Отозвать приглашение для ${invite.name}`}>
        ×
      </button>
      {error && <span style={{ color: "crimson" }}> {error}</span>}
    </form>
  );
}

function CreateUserForm({ roles, onDone }: { roles: User["role"][]; onDone: () => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<User["role"]>(roles[0]);
  const [password, setPassword] = useState("");

  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/users", "POST", { name, email, role, password });
    setName("");
    setEmail("");
    setPassword("");
    onDone();
  });

  return (
    <form onSubmit={submit}>
      <div style={rowStyle}>
        <input placeholder="Имя" value={name} onChange={(e) => setName(e.target.value)} required style={fieldStyle} />
        <input
          type="email"
          placeholder="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          required
          style={fieldStyle}
        />
        <select value={role} onChange={(e) => setRole(e.target.value as User["role"])} style={fieldStyle}>
          {roles.map((r) => (
            <option key={r} value={r}>
              {roleLabels[r]}
            </option>
          ))}
        </select>
        <input
          type="text"
          placeholder="стартовый пароль"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
          minLength={8}
          style={fieldStyle}
        />
        <button type="submit" disabled={busy}>
          {busy ? "Заводим…" : "Завести"}
        </button>
      </div>
      {/* Запасной путь: пароль знает и пригласивший. Основной — ссылка,
          где человек задаёт пароль сам (ADR-008) */}
      <p style={{ color: "#666", margin: "0.5rem 0 0" }}>
        Пароль передайте лично — письма система не шлёт. Человек сменит его в своём кабинете.
      </p>
      <FormStatus error={error} done={done} doneText="Учётка заведена." />
    </form>
  );
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
  return (
    <section style={sectionStyle}>
      <h2 style={{ marginTop: 0 }}>Люди ({users.length})</h2>
      <details style={{ marginBottom: "0.75rem" }}>
        <summary style={{ cursor: "pointer", color: "#666" }}>Завести сразу с паролем, без ссылки</summary>
        <div style={{ padding: "0.75rem 0 0" }}>
          <CreateUserForm roles={roles} onDone={onDone} />
        </div>
      </details>
      {users.length === 0 && <p style={{ color: "#666" }}>Пока никого.</p>}
      {users.map((u) => (
        <details key={u.id} style={{ borderTop: "1px solid #eee", padding: "0.5rem 0" }}>
          <summary style={{ cursor: "pointer" }}>
            <strong>{u.name}</strong> · {u.email} · {roleLabels[u.role]}
            {u.groups.length > 0 && <span style={{ color: "#666" }}> · {u.groups.join(", ")}</span>}
          </summary>
          <div style={{ padding: "0.75rem 0 0 1rem", display: "grid", gap: "0.75rem" }}>
            <ResetPasswordForm userId={u.id} />
            {me.role === "admin" && u.id !== me.id && (
              <ChangeRoleForm userId={u.id} current={u.role} roles={roles} onDone={onDone} />
            )}
          </div>
        </details>
      ))}
    </section>
  );
}

function ResetPasswordForm({ userId }: { userId: number }) {
  const [password, setPassword] = useState("");
  const { busy, error, done, submit } = useSubmit(async () => {
    await request(`/api/users/${userId}/password`, "POST", { password });
    setPassword("");
  });

  return (
    <form onSubmit={submit} style={rowStyle}>
      <input
        type="text"
        placeholder="новый пароль"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        required
        minLength={8}
        style={fieldStyle}
      />
      <button type="submit" disabled={busy}>
        {busy ? "…" : "Сбросить пароль"}
      </button>
      <FormStatus error={error} done={done} doneText="Пароль сброшен, сессии закрыты." />
    </form>
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
    <form onSubmit={submit} style={rowStyle}>
      <select value={role} onChange={(e) => setRole(e.target.value as User["role"])} style={fieldStyle}>
        {roles.map((r) => (
          <option key={r} value={r}>
            {roleLabels[r]}
          </option>
        ))}
      </select>
      <button type="submit" disabled={busy || role === current}>
        {busy ? "…" : "Сменить роль"}
      </button>
      <FormStatus error={error} done={done} doneText="Роль обновлена." />
    </form>
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
  return (
    <section style={sectionStyle}>
      <h2 style={{ marginTop: 0 }}>Группы ({groups.length})</h2>
      {/* Состав групп меняет только админ (ADR-007) — преподавателю
          показываем список без форм, api ему всё равно ответит 403 */}
      {isAdmin && <CreateGroupForm onDone={onDone} />}
      {groups.map((g) => (
        <div key={g.id} style={{ borderTop: "1px solid #eee", padding: "0.75rem 0" }}>
          <strong>{g.name}</strong> · {g.level}
          <ul style={{ listStyle: "none", padding: "0.5rem 0 0", margin: 0, display: "grid", gap: "0.35rem" }}>
            {g.members.length === 0 && <li style={{ color: "#666" }}>Пусто.</li>}
            {g.members.map((m) => (
              <li key={m.user_id} style={rowStyle}>
                {m.name} · {m.email}
                {isAdmin && (
                  <RemoveMemberButton groupId={g.id} userId={m.user_id} name={m.name} onDone={onDone} />
                )}
              </li>
            ))}
          </ul>
          {isAdmin && <AddMemberForm groupId={g.id} onDone={onDone} />}
        </div>
      ))}
      {!isAdmin && (
        <p style={{ color: "#666", marginBottom: 0 }}>
          Состав групп меняет админ; вам группы видны для переклички на уроке.
        </p>
      )}
    </section>
  );
}

function CreateGroupForm({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState("");
  const [level, setLevel] = useState<string>(CEFR_LEVELS[1]);
  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/groups", "POST", { name, level });
    setName("");
    onDone();
  });

  return (
    <form onSubmit={submit} style={{ ...rowStyle, paddingBottom: "0.5rem" }}>
      <input
        placeholder="Название группы"
        value={name}
        onChange={(e) => setName(e.target.value)}
        required
        style={fieldStyle}
      />
      <select value={level} onChange={(e) => setLevel(e.target.value)} style={fieldStyle}>
        {CEFR_LEVELS.map((l) => (
          <option key={l} value={l}>
            {l}
          </option>
        ))}
      </select>
      <button type="submit" disabled={busy}>
        {busy ? "…" : "Создать группу"}
      </button>
      <FormStatus error={error} done={done} doneText="Группа создана." />
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
    <form onSubmit={submit} style={{ ...rowStyle, paddingTop: "0.5rem" }}>
      <input
        type="email"
        placeholder="email участника"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        required
        style={fieldStyle}
      />
      <button type="submit" disabled={busy}>
        {busy ? "…" : "Добавить"}
      </button>
      <FormStatus error={error} done={done} doneText="Добавлен." />
    </form>
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

  // Подтверждение: промах по «×» иначе молча выкидывает человека из группы,
  // отмены нет.
  function confirmThenSubmit(e: FormEvent) {
    e.preventDefault();
    if (window.confirm(`Убрать ${name} из группы?`)) void submit(e);
  }

  return (
    <form onSubmit={confirmThenSubmit} style={{ display: "inline" }}>
      <button type="submit" disabled={busy} aria-label={`Убрать ${name} из группы`}>
        ×
      </button>
      {error && <span style={{ color: "crimson" }}> {error}</span>}
    </form>
  );
}
