"use client";

import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";

import type { PracticeCard, PracticeQueue, ReviewResult } from "../../../lib/api";
import { isSessionKey, SESSION_PROPS } from "../../../lib/practice-keys";
import { delayLabel, keyAction, RATINGS, reviewId } from "../../../lib/review-session";
import type { Rating } from "../../../lib/review-session";
import ClickableText, { ExampleAuthors } from "../../clickable-text";
import { RequestError, requestJson } from "../../forms";
import { termStatuses } from "../../term-status-control";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import { cx } from "../../ui/cx";
import { EmptyState } from "../../ui/empty";
import { Kbd } from "../../ui/kbd";
import s from "../practice.module.css";
import { useSessionFocus } from "../session-focus";

function newReviewId(): string {
  return reviewId(crypto.getRandomValues(new Uint8Array(16)));
}

// Итог оценки для строки под прогрессом: какое слово и когда покажется снова
interface LastReview {
  lemma: string;
  rating: Rating;
  dueAt: string;
}

// CardsSession — проход выдачи очереди: лицо карточки (лемма), по пробелу
// или кнопке — оборот, оценка кнопкой или клавишей 1–4 (POST /reviews).
// review_id заводится при первой попытке оценки и живёт до следующей
// карточки: повтор после сбоя сети api засчитает один раз. studentId —
// чей словарь перекрашивает оценка (кэш статусов по студенту, ADR-026).
export default function CardsSession({ queue, studentId }: { queue: PracticeQueue; studentId: number }) {
  const router = useRouter();
  const [index, setIndex] = useState(0);
  const [flipped, setFlipped] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [last, setLast] = useState<LastReview | null>(null);
  const [tally, setTally] = useState<Record<Rating, number>>({ again: 0, hard: 0, good: 0, easy: 0 });
  // nextDue — ближайший будущий показ: из выдачи и из оценок этой сессии
  // (карточка на шаге обучения вернётся через минуты)
  const [nextDue, setNextDue] = useState<string | null>(queue.next_due_at);
  const idRef = useRef<string | null>(null);
  const sessionRef = useRef<HTMLElement>(null);
  const card: PracticeCard | undefined = queue.items[index];
  const total = queue.items.length;

  function next(note: string | null) {
    idRef.current = null;
    setIndex((i) => i + 1);
    setFlipped(false);
    setError(note);
  }

  async function rate(rating: Rating) {
    if (!card || busy) return;
    idRef.current ??= newReviewId();
    setBusy(true);
    setError(null);
    try {
      const res = await requestJson<ReviewResult>("/api/reviews", "POST", {
        review_id: idRef.current,
        term_id: card.term_id,
        rating,
      });
      termStatuses.setStatus(studentId, card.lemma, res.status);
      setLast({ lemma: card.lemma, rating, dueAt: res.card.due_at });
      setTally((t) => ({ ...t, [rating]: t[rating] + 1 }));
      setNextDue((d) => (d && Date.parse(d) <= Date.parse(res.card.due_at) ? d : res.card.due_at));
      next(null);
    } catch (e) {
      // 404 — слово убрали из словаря, 409 — карточку уже оценили в другой
      // вкладке: дальше, а не повтор; прочие сбои — повтор с тем же ключом
      if (e instanceof RequestError && (e.status === 404 || e.status === 409)) {
        next(`«${card.lemma}» пропущено: ${e.message}`);
      } else {
        setError(e instanceof Error ? e.message : "не удалось сохранить оценку");
      }
    } finally {
      setBusy(false);
    }
  }

  // Клавиши — только с фокусом в сессии (WCAG 2.1.4). Ввод в поля, попап
  // и слова текста не перехватываются; пробел и Enter на кнопке или
  // ссылке — их собственное нажатие (lib/practice-keys). Обработчик
  // переподписывается на каждый рендер: видит текущую карточку
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!card || e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;
      if (!isSessionKey(e.target instanceof Element ? e.target : null, e.key)) return;
      const action = keyAction(e.key, flipped);
      if (!action) return;
      e.preventDefault();
      if (action.kind === "flip") setFlipped(true);
      else void rate(action.rating);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  // Фокус — в сессии с начала, после переворота и после запроса оценки:
  // нажатая мышью кнопка оценки на время запроса disabled, фокус с неё
  // падает на <body>, и после сбоя клавиши иначе молчали бы
  useSessionFocus(sessionRef, `${index}:${flipped}:${busy}`);

  // Пустая выдача — только итог, без счёта «0 из 0»
  if (total === 0) {
    return <Finished queue={queue} nextDue={nextDue} reviewed={false} onMore={() => router.refresh()} />;
  }

  const done = Math.min(index, total);
  const progress = { "--progress": `${(100 * done) / total}%` } as CSSProperties;
  return (
    <section ref={sessionRef} tabIndex={-1} {...SESSION_PROPS} aria-label="Карточки" className={s.session}>
      <div className={s.bar}>
        <div className={s.barRow}>
          <span>
            <span className={s.count}>
              {done} из {total}
            </span>
            {queue.due_total > total && <span className="small muted"> · всего к повторению {queue.due_total}</span>}
          </span>
          <span className="small muted">
            снова {tally.again} · трудно {tally.hard} · хорошо {tally.good} · легко {tally.easy}
          </span>
        </div>
        <div
          className="progress"
          role="progressbar"
          aria-label="Пройдено карточек"
          aria-valuemin={0}
          aria-valuemax={total}
          aria-valuenow={done}
          style={progress}
        >
          <span />
        </div>
        <p className={s.last} role="status">
          {last && (
            <>
              «{last.lemma}» — {RATINGS.find((o) => o.rating === last.rating)?.label.toLowerCase()}, снова{" "}
              {delayLabel(Date.parse(last.dueAt) - Date.now())}
            </>
          )}
        </p>
      </div>
      {error && (
        <Alert tone="danger" className={s.alert}>
          {error}
        </Alert>
      )}
      {card ? (
        <CardView
          key={card.term_id}
          card={card}
          flipped={flipped}
          busy={busy}
          onFlip={() => setFlipped(true)}
          onRate={rate}
        />
      ) : (
        <Finished queue={queue} nextDue={nextDue} reviewed={done > 0} onMore={() => router.refresh()} />
      )}
    </section>
  );
}

function CardView({
  card,
  flipped,
  busy,
  onFlip,
  onRate,
}: {
  card: PracticeCard;
  flipped: boolean;
  busy: boolean;
  onFlip: () => void;
  onRate: (r: Rating) => void;
}) {
  return (
    <>
      <article className={cx("card", s.flashcard, !flipped && s.front)}>
        <p className={s.lemma}>
          <ClickableText text={card.lemma} />
        </p>
        {card.surface.toLowerCase() !== card.lemma && (
          <p className={s.surface}>
            <ClickableText text={card.surface} />
          </p>
        )}
        {flipped && <CardBack card={card} />}
      </article>
      <div className={cx(s.actions, s.sticky)}>
        {flipped ? (
          <div className={s.rate} role="group" aria-label="Оценка">
            {RATINGS.map((o) => (
              <button
                key={o.rating}
                type="button"
                className={cx(s.rateBtn, s[o.rating])}
                disabled={busy}
                onClick={() => onRate(o.rating)}
              >
                {o.label} <Kbd>{o.key}</Kbd>
              </button>
            ))}
          </div>
        ) : (
          <Button type="button" variant="primary" size="lg" blockOnPhone className={cx(s.flip, s.onPrimary)} onClick={onFlip}>
            Показать <Kbd>Пробел</Kbd>
          </Button>
        )}
      </div>
      <p className={s.shortcuts}>
        <span>
          <Kbd>Пробел</Kbd> перевернуть
        </span>
        <span>
          <Kbd>1</Kbd>–<Kbd>4</Kbd> оценить
        </span>
      </p>
    </>
  );
}

// Оборот: перевод, статьи словаря и примеры — как в попапе слова, все
// тексты через ClickableText; подписи Wiktionary и Tatoeba обязательны
// по их лицензиям (ADR-019, ADR-028)
function CardBack({ card }: { card: PracticeCard }) {
  return (
    <div className={s.back}>
      <p className={s.translation}>
        {card.translation ? <ClickableText text={card.translation} /> : <span className="muted">перевода пока нет</span>}
      </p>
      {card.definitions.length > 0 && (
        <div className={s.block}>
          <ol className={s.defs}>
            {card.definitions.map((d, i) => (
              <li key={i}>
                <em lang="en" className={s.pos}>
                  {d.pos}
                </em>{" "}
                <ClickableText text={d.glosses.join("; ")} />
                {d.translations_ru.length > 0 && (
                  <span className="muted">
                    {" — "}
                    <ClickableText text={d.translations_ru.join(", ")} />
                  </span>
                )}
              </li>
            ))}
          </ol>
          <p className="attr">
            <a href={`https://en.wiktionary.org/wiki/${encodeURIComponent(card.definitions[0].word)}`} target="_blank" rel="noreferrer">
              Wiktionary
            </a>
            ,{" "}
            <a href="https://creativecommons.org/licenses/by-sa/4.0/" target="_blank" rel="noreferrer">
              CC BY-SA
            </a>
          </p>
        </div>
      )}
      {card.examples.length > 0 && (
        <div className={s.block}>
          <div className={s.examples}>
            {card.examples.map((ex, i) => (
              <div key={i} className={s.example}>
                <span>
                  <ClickableText text={ex.text} />
                </span>
                <span className="muted">
                  <ClickableText text={ex.translation} />
                </span>
                <ExampleAuthors example={ex} />
              </div>
            ))}
          </div>
          <p className="attr">
            <a href="https://tatoeba.org" target="_blank" rel="noreferrer">
              Tatoeba
            </a>
            ,{" "}
            <a href={card.examples[0].attribution.license_url} target="_blank" rel="noreferrer">
              {card.examples[0].attribution.license}
            </a>
          </p>
        </div>
      )}
    </div>
  );
}

// Finished — выдача пройдена или пуста: когда следующий показ и кнопка
// новой выдачи (карточки на шагах обучения возвращаются через минуты)
function Finished({
  queue,
  nextDue,
  reviewed,
  onMore,
}: {
  queue: PracticeQueue;
  nextDue: string | null;
  reviewed: boolean;
  onMore: () => void;
}) {
  const left = queue.due_total - queue.items.length;
  return (
    <div className="card">
      <EmptyState
        icon={reviewed ? "check" : "cards"}
        title={reviewed ? "Выдача пройдена" : "Сейчас повторять нечего"}
        action={
          <Button type="button" variant="secondary" blockOnPhone onClick={onMore}>
            Проверить очередь
          </Button>
        }
      >
        {left > 0
          ? `К повторению ещё ${left}.`
          : nextDue && `Следующая карточка — ${delayLabel(Date.parse(nextDue) - Date.now())}.`}
      </EmptyState>
    </div>
  );
}
