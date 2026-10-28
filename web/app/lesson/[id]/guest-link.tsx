"use client";

import { useState } from "react";
import type { FormEvent } from "react";

import { FormStatus, request, requestJson, useSubmit } from "../../forms";
import { Button } from "../../ui/button";
import { cx } from "../../ui/cx";
import { Icon } from "../../ui/icons";
import styles from "./page.module.css";

// Гостевая ссылка на событие (ADR-010): многоразовая, по ней заходят все,
// представившись именем. Показывается только организатору и только
// у события без группы — api выдаёт ссылку лишь ему.
//
// Сам токен хранится лишь хэшем: после перезагрузки страницы ссылку
// не показать, известно только, что она выпущена (hasLink). Поэтому
// перевыпуск, ломающий уже разосланную ссылку, идёт только с подтверждения,
// а отозвать её можно и без нового выпуска (ADR-032).
export default function GuestLink({ lessonId, hasLink }: { lessonId: number; hasLink: boolean }) {
  const [exists, setExists] = useState(hasLink);
  // Открытый текст ссылки есть только сразу после выпуска в этой вкладке
  const [link, setLink] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const issue = useSubmit(async () => {
    const res = await requestJson<{ url: string }>(`/api/lessons/${lessonId}/guest-link`, "POST");
    setLink(res.url);
    setExists(true);
    setCopied(false);
  });

  const revoke = useSubmit(async () => {
    await request(`/api/lessons/${lessonId}/guest-link`, "DELETE");
    setLink(null);
    setExists(false);
  });

  function confirmIssue(e: FormEvent) {
    e.preventDefault();
    // Первый выпуск ничего не ломает; перевыпуск отключает разосланную ссылку
    if (
      !exists ||
      window.confirm("Выпустить новую ссылку? Старая перестанет работать у всех, кому вы её уже отправили.")
    ) {
      void issue.submit(e);
    }
  }

  function confirmRevoke(e: FormEvent) {
    e.preventDefault();
    // Отзыв ломает уже разосланную ссылку — спрашиваем
    if (window.confirm("Отозвать гостевую ссылку? Кто ещё не зашёл, зайти не сможет.")) void revoke.submit(e);
  }

  async function copy() {
    if (!link) return;
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
    } catch {
      // Буфер недоступен — ссылка и так на экране
      setCopied(false);
    }
  }

  return (
    <section className={cx("card", styles.guest)} aria-labelledby="guest-link-title">
      <h2 id="guest-link-title" className={styles.guestTitle}>
        <Icon name="people" size="sm" />
        Гостевая ссылка
      </h2>
      {link && (
        <div className="copy-field">
          <input
            className="input"
            readOnly
            value={link}
            aria-label="Гостевая ссылка"
            onFocus={(e) => e.target.select()}
          />
          <Button type="button" variant="secondary" onClick={copy}>
            <Icon name={copied ? "check" : "copy"} size="sm" />
            {copied ? "Скопировано" : "Скопировать"}
          </Button>
        </div>
      )}
      {/* Смена надписи кнопки сама не озвучивается — итог копирования здесь */}
      <span className="sr-only" role="status">
        {copied ? "Ссылка скопирована" : ""}
      </span>
      {exists && (
        <p className="hint">
          {link
            ? "Ссылка многоразовая: по ней зайдут все, кому вы её отправите. Перевыпуск отключает предыдущую."
            : "Ссылка уже выпущена и действует. Показать её ещё раз нельзя — хранится только отпечаток; нужна снова — выпустите новую, старая при этом перестанет работать."}
        </p>
      )}
      <div className={styles.guestActions}>
        <form onSubmit={confirmIssue}>
          <Button
            type="submit"
            variant={exists ? "secondary" : "primary"}
            size="sm"
            disabled={issue.busy}
            busy={issue.busy}
          >
            {issue.busy ? "Готовим…" : exists ? "Выпустить новую ссылку" : "Выпустить ссылку"}
          </Button>
        </form>
        {exists && (
          <form onSubmit={confirmRevoke}>
            <Button type="submit" variant="danger-ghost" size="sm" disabled={revoke.busy} busy={revoke.busy}>
              Отозвать
            </Button>
          </form>
        )}
      </div>
      <FormStatus error={issue.error ?? revoke.error} done={false} doneText="" />
    </section>
  );
}
