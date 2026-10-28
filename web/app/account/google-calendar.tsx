"use client";

import { useRouter } from "next/navigation";
import { useId } from "react";

import type { GoogleCalendarStatus, GoogleDisconnectResult } from "../../lib/api";
import { FormStatus, requestJson, useSubmit } from "../forms";
import { Alert } from "../ui/alert";
import type { AlertTone } from "../ui/alert";
import { Button } from "../ui/button";
import { cx } from "../ui/cx";
import { Pill } from "../ui/pill";
import styles from "./account.module.css";

// Итоги возврата с экрана согласия Google (?google= от api, ADR-023)
// и отключения (not_revoked ставит сама страница). not_revoked — не сбой:
// календарь отключён, осталось снять доступ в Google вручную.
const results: Record<string, { text: string; tone: AlertTone }> = {
  connected: { text: "Календарь подключён: уроки появятся в нём в течение минуты.", tone: "success" },
  denied: { text: "Доступ не выдан — календарь не подключён.", tone: "danger" },
  scope: {
    text: "Google не дал доступ к событиям календаря: при подключении оставьте отмеченным пункт про календарь.",
    tone: "danger",
  },
  failed: { text: "Не удалось подключить календарь, попробуйте ещё раз.", tone: "danger" },
  unavailable: { text: "Подключение календаря на стенде пока не настроено.", tone: "danger" },
  not_revoked: {
    text:
      "Календарь отключён, но отозвать доступ в Google не удалось. Снимите его вручную: аккаунт Google → " +
      "Безопасность → Сторонние приложения с доступом к аккаунту.",
    tone: "warning",
  },
};

// Google Calendar преподавателя (S1-1): уроки уходят событиями в его
// календарь, а участники получают приглашения от Google.
export default function GoogleCalendar({
  status,
  result,
}: {
  status: GoogleCalendarStatus;
  result?: string;
}) {
  const router = useRouter();
  const titleId = useId();
  // Старт — POST: api кладёт state в cookie и отдаёт адрес экрана согласия
  const connect = useSubmit(async () => {
    const { url } = await requestJson<{ url: string }>("/api/auth/google/start", "POST");
    window.location.assign(url);
  });
  const disconnect = useSubmit(async () => {
    const { revoked } = await requestJson<GoogleDisconnectResult>("/api/auth/google", "DELETE");
    router.replace(revoked ? "/account" : "/account?google=not_revoked");
    router.refresh();
  });
  const shown = result ? results[result] : undefined;

  return (
    <section className="card" aria-labelledby={titleId}>
      <div className="card-head">
        <h2 id={titleId}>Google Календарь</h2>
        {!status.available ? (
          <Pill>Недоступно</Pill>
        ) : status.connected ? (
          <Pill tone="success">Подключён</Pill>
        ) : (
          <Pill>Не подключён</Pill>
        )}
      </div>
      <div className="card-pad stack stack--sm">
        {shown && <Alert tone={shown.tone}>{shown.text}</Alert>}
        {!status.available ? (
          <p className="muted">
            На стенде не настроено приложение Google. Уроки пока видны только здесь, на платформе.
          </p>
        ) : status.connected ? (
          <>
            <p>
              Созданные, перенесённые и отменённые уроки отражаются в вашем календаре, участники получают
              приглашения от Google.
            </p>
            <p className="small muted">
              Отключение не удаляет уже созданные события. Уроки, отменённые без подключения, исчезнут из
              календаря, когда вы снова подключите тот же аккаунт Google.
            </p>
          </>
        ) : (
          <p>Подключите календарь, чтобы уроки появлялись в нём, а участники получали приглашения.</p>
        )}
      </div>
      {status.available && (
        <form
          onSubmit={status.connected ? disconnect.submit : connect.submit}
          className={cx("card-foot", styles.foot)}
        >
          {status.connected ? (
            <>
              <FormStatus error={disconnect.error} done={false} doneText="" />
              <Button
                type="submit"
                blockOnPhone
                disabled={disconnect.busy}
                busy={disconnect.busy}
                className={styles.submit}
              >
                {disconnect.busy ? "Отключаем…" : "Отключить"}
              </Button>
            </>
          ) : (
            <>
              <FormStatus error={connect.error} done={false} doneText="" />
              <Button
                type="submit"
                variant="primary"
                blockOnPhone
                disabled={connect.busy}
                busy={connect.busy}
                className={styles.submit}
              >
                {connect.busy ? "Переходим в Google…" : "Подключить Google Календарь"}
              </Button>
            </>
          )}
        </form>
      )}
    </section>
  );
}
