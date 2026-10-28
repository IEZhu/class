"use client";

import { useRouter } from "next/navigation";
import { useId, useState } from "react";
import type { FormEvent } from "react";

import type { Material, MaterialFile } from "../../../lib/api";
import { FormStatus, request, requestJson, useSubmit } from "../../forms";
import { Button, buttonClass } from "../../ui/button";
import { cx } from "../../ui/cx";
import { Field } from "../../ui/field";
import { Icon } from "../../ui/icons";
import { Pill } from "../../ui/pill";
import { Spinner } from "../../ui/skeleton";
import styles from "./page.module.css";

// Файлы материалов (долг D-4, ADR-024). Файл идёт браузер ↔ хранилище
// напрямую по presigned-ссылкам, минуя VPS: api выдаёт ссылку на загрузку,
// потом сам сверяет объект (confirm) и подписывает скачивание.

type CreatedMaterial = Material & {
  upload: { url: string; method: string; headers: Record<string, string>; expires_at: string };
};

// Подсказка для диалога выбора файла; решает всё равно api (тип и размер
// он проверяет сам и отвечает понятной ошибкой).
const ACCEPT = ".pdf,.docx,.pptx,.txt,.png,.jpg,.jpeg,.webp,.mp3,.m4a,.ogg,.mp4";

const sizeFmt = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 1 });

// Куда прикрепить файл — тот же kind, что раньше выбирал <select>
const kinds: { kind: Material["kind"]; title: string }[] = [
  { kind: "material", title: "Материалы" },
  { kind: "homework", title: "Домашка" },
];

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
  const id = useId();

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
    <form onSubmit={submit} className={cx("card", styles.upload)} aria-labelledby={`${id}-title`}>
      <h3 id={`${id}-title`}>Добавить материал</h3>
      <div className={styles.uploadFields}>
        <Field label="Название" hint="По умолчанию — имя файла">
          <input className="input" value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <div className="field">
          <span className="label" id={`${id}-kind`}>
            Куда
          </span>
          <div className="segmented" role="group" aria-labelledby={`${id}-kind`}>
            {kinds.map((k) => (
              <button key={k.kind} type="button" aria-pressed={kind === k.kind} onClick={() => setKind(k.kind)}>
                {k.title}
              </button>
            ))}
          </div>
        </div>
        <div className="field">
          <span className="label" aria-hidden="true">
            Файл
          </span>
          {/* Свой вид поля файла (DESIGN §3.5): браузерное «Choose File»
              по-английски. Фокус — на скрытом input, рамка — на подписи */}
          <div className="file-field">
            <input
              key={inputKey}
              id={`${id}-file`}
              type="file"
              accept={ACCEPT}
              required
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
            <label htmlFor={`${id}-file`} className={buttonClass({ variant: "secondary" })}>
              <Icon name="file" size="sm" />
              Выбрать файл
            </label>
            <span className="file-name">{file ? file.name : "Файл не выбран"}</span>
          </div>
        </div>
      </div>
      <div className="row">
        <Button type="submit" variant="primary" disabled={busy} busy={busy}>
          {busy ? "Загружаем…" : "Прикрепить файл"}
        </Button>
        <FormStatus error={error} done={done} doneText="Файл прикреплён." />
      </div>
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
    <form onSubmit={submit} className={styles.fileRow}>
      <span className={styles.fileIc} aria-hidden="true">
        {extension(file.name) ?? <Icon name="file" />}
      </span>
      <span className={styles.fileMeta}>
        <b className={styles.fileName}>{file.name}</b>
        <span className="muted small">{formatSize(file.size)}</span>
      </span>
      {file.ready ? (
        <Button
          type="submit"
          variant="secondary"
          size="sm"
          disabled={busy}
          busy={busy}
          aria-label={`Скачать ${file.name}`}
        >
          {busy ? <Spinner label="Готовим ссылку" /> : <Icon name="download" size="sm" />}
          Скачать
        </Button>
      ) : (
        <Pill tone="processing">загрузка не завершена</Pill>
      )}
      <FormStatus error={error} done={false} doneText="" />
    </form>
  );
}

// Тип файла на значке — расширение имени: PDF, DOCX, MP3
function extension(name: string): string | null {
  const dot = name.lastIndexOf(".");
  const ext = dot > 0 ? name.slice(dot + 1) : "";
  return ext && ext.length <= 4 ? ext.toUpperCase() : null;
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
    <form onSubmit={confirmThenSubmit} className={styles.deleteForm}>
      <Button
        type="submit"
        variant="danger-ghost"
        size="sm"
        disabled={busy}
        busy={busy}
        aria-label={`Удалить «${title}»`}
      >
        {busy && <Spinner label="Удаляем" />}
        Удалить
      </Button>
      <FormStatus error={error} done={false} doneText="" />
    </form>
  );
}
