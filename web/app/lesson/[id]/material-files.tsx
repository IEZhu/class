"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { FormEvent } from "react";

import type { Material, MaterialFile } from "../../../lib/api";
import { FormStatus, request, requestJson, useSubmit } from "../../forms";

// Файлы материалов (долг D-4, ADR-024). Файл идёт браузер ↔ хранилище
// напрямую по presigned-ссылкам, минуя VPS: api выдаёт ссылку на загрузку,
// потом сам сверяет объект (confirm) и подписывает скачивание.

type CreatedMaterial = Material & {
  upload: { url: string; method: string; headers: Record<string, string>; expires_at: string };
};

const rowStyle = { display: "flex", gap: "0.5rem", flexWrap: "wrap", alignItems: "center" } as const;
const fieldStyle = { padding: "0.5rem" };

// Подсказка для диалога выбора файла; решает всё равно api (тип и размер
// он проверяет сам и отвечает понятной ошибкой).
const ACCEPT = ".pdf,.docx,.pptx,.txt,.png,.jpg,.jpeg,.webp,.mp3,.m4a,.ogg,.mp4";

const sizeFmt = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 1 });

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} Б`;
  if (bytes < 1024 * 1024) return `${sizeFmt.format(bytes / 1024)} КБ`;
  return `${sizeFmt.format(bytes / (1024 * 1024))} МБ`;
}

export function MaterialUpload({ lessonId }: { lessonId: number }) {
  const router = useRouter();
  const [title, setTitle] = useState("");
  const [kind, setKind] = useState<Material["kind"]>("material");
  const [file, setFile] = useState<File | null>(null);
  // Сброс <input type=file>: его значение React не контролирует
  const [inputKey, setInputKey] = useState(0);

  const { busy, error, done, submit } = useSubmit(async () => {
    if (!file) throw new Error("выберите файл");
    const created = await requestJson<CreatedMaterial>(
      `/api/lessons/${lessonId}/${kind === "homework" ? "homework" : "materials"}`,
      "POST",
      { title, file: { name: file.name, content_type: file.type, size: file.size } },
    );
    try {
      await putToStorage(created.upload, file);
      await request(`/api/materials/${created.id}/confirm`, "POST");
    } catch (e) {
      // Загрузка сорвалась — заготовку убираем, чтобы не висела в списке.
      // Если api уже отклонил файл при сверке, он удалил её сам (404).
      await request(`/api/materials/${created.id}`, "DELETE").catch(() => undefined);
      throw e;
    }
    setTitle("");
    setFile(null);
    setInputKey((k) => k + 1);
    router.refresh();
  });

  return (
    <form onSubmit={submit} style={{ ...rowStyle, margin: "0.5rem 0 1rem" }}>
      <input
        key={inputKey}
        type="file"
        accept={ACCEPT}
        required
        onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        style={fieldStyle}
      />
      <input
        placeholder="Название (по умолчанию — имя файла)"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        style={{ ...fieldStyle, minWidth: "16rem" }}
      />
      <select value={kind} onChange={(e) => setKind(e.target.value as Material["kind"])} style={fieldStyle}>
        <option value="material">в материалы</option>
        <option value="homework">в домашку</option>
      </select>
      <button type="submit" disabled={busy}>
        {busy ? "Загружаем…" : "Прикрепить файл"}
      </button>
      <FormStatus error={error} done={done} doneText="Файл прикреплён." />
    </form>
  );
}

async function putToStorage(upload: CreatedMaterial["upload"], file: File): Promise<void> {
  let res: Response;
  try {
    // Заголовки — ровно те, что вошли в подпись: другой тип или имя
    // хранилище отвергнет (403 SignatureDoesNotMatch)
    res = await fetch(upload.url, { method: upload.method, headers: upload.headers, body: file });
  } catch {
    throw new Error("хранилище недоступно — попробуйте ещё раз");
  }
  if (!res.ok) throw new Error(`хранилище не приняло файл (${res.status})`);
}

// Скачать файл: свежая presigned-ссылка на каждый клик (живёт 30 минут),
// имя файла хранилище отдаёт само из Content-Disposition.
export function FileDownload({ materialId, file }: { materialId: number; file: MaterialFile }) {
  const { busy, error, submit } = useSubmit(async () => {
    const { url } = await requestJson<{ url: string }>(`/api/materials/${materialId}/url`, "GET");
    window.location.assign(url);
  });

  return (
    <form onSubmit={submit} style={rowStyle}>
      <span>
        📎 {file.name} · {formatSize(file.size)}
      </span>
      {file.ready ? (
        <button type="submit" disabled={busy}>
          {busy ? "…" : "Скачать"}
        </button>
      ) : (
        <span style={{ color: "#a60" }}>загрузка не завершена</span>
      )}
      {error && <span style={{ color: "crimson" }}>{error}</span>}
    </form>
  );
}

export function DeleteMaterial({ materialId, title }: { materialId: number; title: string }) {
  const router = useRouter();
  const { busy, error, submit } = useSubmit(async () => {
    await request(`/api/materials/${materialId}`, "DELETE");
    router.refresh();
  });

  // Удаление не отменить: файл уходит из хранилища сразу
  function confirmThenSubmit(e: FormEvent) {
    e.preventDefault();
    if (window.confirm(`Удалить «${title}»? Файл удалится из хранилища.`)) void submit(e);
  }

  return (
    <form onSubmit={confirmThenSubmit} style={{ display: "inline" }}>
      <button type="submit" disabled={busy}>
        {busy ? "…" : "Удалить"}
      </button>
      {error && <span style={{ color: "crimson" }}> {error}</span>}
    </form>
  );
}
