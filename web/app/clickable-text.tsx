"use client";

import { createContext, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { CSSProperties, MouseEvent, ReactNode } from "react";
import { createPortal } from "react-dom";

import type { TermClick, TermExample } from "../lib/api";
import { isTextSelection } from "../lib/selection";
import type { TermStatus } from "../lib/term-status";
import { sentenceAround, tokenize, uniqueKeys } from "../lib/tokenize";
import { requestJson } from "./forms";
import TermStatusControl, { termStatuses } from "./term-status-control";

// <ClickableText> — единственный рендер текста в системе (инвариант,
// S3-1, ADR-026): материалы, домашка, транскрипт, субтитры, Tatoeba,
// сгенерированные тексты. Режет текст на слова (lib/tokenize), красит
// их по статусу в словаре зрителя и по клику открывает попап слова.

interface Scope {
  lessonId: number | null;
  dictionary: boolean;
}

const ScopeContext = createContext<Scope>({ lessonId: null, dictionary: false });

// ClickableTextScope — что текстам страницы известно о ней: урок (клик
// уходит с lesson_id — по нему S3-7 запишет кандидатов словаря урока,
// а «в мой словарь» — источник слова) и есть ли у зрителя личный словарь
// (раскраска и статус — только у студента, 04-api.md). Без обёртки
// текст кликается, но не красится.
export function ClickableTextScope({
  lessonId = null,
  dictionary,
  children,
}: {
  lessonId?: number | null;
  dictionary: boolean;
  children: ReactNode;
}) {
  const value = useMemo(() => ({ lessonId, dictionary }), [lessonId, dictionary]);
  return <ScopeContext.Provider value={value}>{children}</ScopeContext.Provider>;
}

// Раскраска: чем ниже статус, тем ярче; «знаю» и слова вне словаря — без
// фона, иначе текст новичка стал бы сплошь цветным (ADR-026)
const wordStyle: CSSProperties = { cursor: "pointer", borderRadius: 2 };
const statusStyles: Record<TermStatus, CSSProperties> = {
  1: { ...wordStyle, background: "#ffd43b" },
  2: { ...wordStyle, background: "#ffe066" },
  3: { ...wordStyle, background: "#ffec99" },
  4: { ...wordStyle, background: "#fff6cc" },
  5: wordStyle,
};

// Клик по слову: форма из текста, ключ (wordKey), предложение вокруг
interface WordClick {
  surface: string;
  key: string;
  context: string;
}

interface OpenPopup extends WordClick {
  // Номер клика: новый клик в том же тексте пересоздаёт попап
  seq: number;
  top: number;
  left: number;
}

// Попап на странице один: открывая свой, текст закрывает чужой
let closeOpenPopup: (() => void) | null = null;

const serverVersion = () => 0;

export default function ClickableText({
  text,
  onWord,
}: {
  text: string;
  // onWord — клик внутри попапа (дефиниции, примеры): попап переходит
  // к новому слову, а не открывает второй
  onWord?: (word: WordClick) => void;
}) {
  const scope = useContext(ScopeContext);
  const tokens = useMemo(() => tokenize(text), [text]);
  useSyncExternalStore(termStatuses.subscribe, termStatuses.getVersion, serverVersion);
  const [popup, setPopup] = useState<OpenPopup | null>(null);
  const close = useRef(() => setPopup(null)).current;

  useEffect(() => {
    if (scope.dictionary) void termStatuses.request(uniqueKeys(tokens));
  }, [scope.dictionary, tokens]);

  useEffect(
    () => () => {
      if (closeOpenPopup === close) closeOpenPopup = null;
    },
    [close],
  );

  // Один обработчик на текст вместо тысяч у слов: в транскрипте урока
  // десятки тысяч токенов
  function handleClick(e: MouseEvent<HTMLSpanElement>) {
    const el = (e.target as HTMLElement).closest<HTMLElement>("[data-i]");
    // Клик из попапа всплывает и сюда по дереву React: попап в портале,
    // Выделение текста мышью — не клик: ни попапа, ни перемотки реплики
    // вокруг текста
    if (isTextSelection(window.getSelection())) {
      e.stopPropagation();
      return;
    }
    // его слова — не наши
    if (!el || !e.currentTarget.contains(el)) return;
    const t = tokens[Number(el.dataset.i)];
    if (!t?.key) return;
    // Клик по слову — этого текста: строка вокруг (перемотка реплики
    // транскрипта, S2-4) его не получает
    e.stopPropagation();
    const word = { surface: t.text, key: t.key, context: sentenceAround(text, t.start, t.start + t.text.length) };
    if (onWord) {
      onWord(word);
      return;
    }
    const rect = el.getBoundingClientRect();
    if (closeOpenPopup && closeOpenPopup !== close) closeOpenPopup();
    closeOpenPopup = close;
    setPopup((prev) => ({
      ...word,
      seq: (prev?.seq ?? 0) + 1,
      top: rect.bottom + window.scrollY + 4,
      left: rect.left + window.scrollX,
    }));
  }

  return (
    <span onClick={handleClick} style={{ whiteSpace: "pre-wrap" }}>
      {tokens.map((t, i) => {
        if (!t.key) return t.text;
        const status = scope.dictionary ? termStatuses.statusOf(t.key) : undefined;
        return (
          <span key={i} data-i={i} style={status ? statusStyles[status] : wordStyle}>
            {t.text}
          </span>
        );
      })}
      {popup && createPortal(<TermPopup key={popup.seq} start={popup} onClose={close} />, document.body)}
    </span>
  );
}

const POPUP_WIDTH = 340;

// hasTerm — у слова есть термин: term_id 0 api отдаёт незнакомому слову,
// термина не заводя, — в словарь его не добавить и статуса у него нет
function hasTerm(d: TermClick): boolean {
  return d.term_id > 0;
}

const sectionStyle = { margin: "0.5rem 0 0", fontSize: "0.9em" } as const;
const mutedStyle = { color: "#666" } as const;

function TermPopup({ start, onClose }: { start: OpenPopup; onClose: () => void }) {
  const scope = useContext(ScopeContext);
  useSyncExternalStore(termStatuses.subscribe, termStatuses.getVersion, serverVersion);
  const [word, setWord] = useState<WordClick>(start);
  const [data, setData] = useState<TermClick | null>(null);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    setData(null);
    setError(null);
    requestJson<TermClick>("/api/terms/click", "POST", {
      surface: word.surface,
      context_sentence: word.context,
      lesson_id: scope.lessonId ?? undefined,
    })
      .then((d) => {
        if (cancelled) return;
        termStatuses.learn(word.key, d.lemma);
        if (scope.dictionary && hasTerm(d)) termStatuses.setStatus(d.lemma, d.status);
        setData(d);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "не удалось загрузить слово");
      });
    return () => {
      cancelled = true;
    };
  }, [word, scope.lessonId, scope.dictionary]);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    function onDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    }
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onDown);
    };
  }, [onClose]);

  // Попап не вылезает за правый край окна
  const left = Math.max(window.scrollX + 8, Math.min(start.left, window.scrollX + window.innerWidth - POPUP_WIDTH - 8));
  const status = data ? (termStatuses.statusOf(word.key) ?? null) : null;

  return (
    // Клики попапа по дереву React всплыли бы в обработчики вокруг текста
    // (перемотка реплики): попап — не часть строки, в которой открыт
    <div
      ref={ref}
      onClick={(e) => e.stopPropagation()}
      role="dialog"
      aria-label={`Слово ${word.surface}`}
      style={{
        position: "absolute",
        top: start.top,
        left,
        width: `min(${POPUP_WIDTH}px, calc(100vw - 16px))`,
        maxHeight: "60vh",
        overflowY: "auto",
        boxSizing: "border-box",
        padding: "0.75rem",
        background: "#fff",
        border: "1px solid #ccc",
        borderRadius: 8,
        boxShadow: "0 4px 16px rgba(0,0,0,0.15)",
        fontFamily: "system-ui, sans-serif",
        fontSize: 15,
        lineHeight: 1.4,
        zIndex: 1000,
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between", gap: "0.5rem", alignItems: "baseline" }}>
        <strong>{data?.lemma ?? word.surface}</strong>
        {data && data.lemma !== word.key && <span style={mutedStyle}>{word.surface}</span>}
        <button type="button" onClick={onClose} aria-label="Закрыть" style={{ marginLeft: "auto", border: 0, background: "none", cursor: "pointer" }}>
          ×
        </button>
      </div>
      {!data && !error && <p style={{ ...sectionStyle, ...mutedStyle }}>Загружаем…</p>}
      {error && <p style={{ ...sectionStyle, color: "crimson" }}>{error}</p>}
      {data && (
        <>
          <p style={{ margin: "0.25rem 0 0.5rem" }}>
            {data.translation ? data.translation.text : <span style={mutedStyle}>перевода пока нет</span>}
          </p>
          {scope.dictionary && hasTerm(data) && (
            <TermStatusControl termId={data.term_id} lemma={data.lemma} status={status} lessonId={scope.lessonId} />
          )}
          {data.definitions.length > 0 && (
            <div style={sectionStyle}>
              <ol style={{ margin: 0, paddingLeft: "1.25rem" }}>
                {data.definitions.map((d, i) => (
                  <li key={i} style={{ marginBottom: "0.25rem" }}>
                    <em style={mutedStyle}>{d.pos}</em> {d.word !== data.lemma && <b>{d.word} </b>}
                    {d.glosses.map((g, j) => (
                      <span key={j}>
                        {j > 0 && "; "}
                        <ClickableText text={g} onWord={setWord} />
                      </span>
                    ))}
                    {d.translations_ru.length > 0 && <span style={mutedStyle}> — {d.translations_ru.join(", ")}</span>}
                  </li>
                ))}
              </ol>
              {/* CC BY-SA требует атрибуции (ADR-019) */}
              <p style={{ ...mutedStyle, fontSize: "0.85em", margin: "0.25rem 0 0" }}>
                <a href={`https://en.wiktionary.org/wiki/${encodeURIComponent(data.definitions[0].word)}`} target="_blank" rel="noreferrer">
                  Wiktionary
                </a>
                ,{" "}
                <a href="https://creativecommons.org/licenses/by-sa/4.0/" target="_blank" rel="noreferrer">
                  CC BY-SA
                </a>
              </p>
            </div>
          )}
          {data.examples.length > 0 && (
            <div style={sectionStyle}>
              <b>Примеры</b>
              <ul style={{ margin: 0, paddingLeft: "1.25rem" }}>
                {data.examples.map((ex, i) => (
                  <li key={i}>
                    <ClickableText text={ex.text} onWord={setWord} />
                    <div style={mutedStyle}>{ex.translation}</div>
                    <ExampleAuthors example={ex} />
                  </li>
                ))}
              </ul>
              {/* CC BY 2.0 FR требует подписи: авторы — у каждого примера (ADR-028) */}
              <p style={{ ...mutedStyle, fontSize: "0.85em", margin: "0.25rem 0 0" }}>
                <a href="https://tatoeba.org" target="_blank" rel="noreferrer">
                  Tatoeba
                </a>
                ,{" "}
                <a href={data.examples[0].attribution.license_url} target="_blank" rel="noreferrer">
                  {data.examples[0].attribution.license}
                </a>
              </p>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ExampleAuthors — подпись примера Tatoeba: авторы предложения и перевода
// со ссылками на их страницы (условие CC BY 2.0 FR, ADR-028)
function ExampleAuthors({ example: { attribution: a } }: { example: TermExample }) {
  const small = { ...mutedStyle, fontSize: "0.8em" };
  return (
    <div style={small}>
      <a href={a.url} target="_blank" rel="noreferrer">
        {a.author || "автор не указан"}
      </a>
      {" · перевод: "}
      <a href={a.translation_url} target="_blank" rel="noreferrer">
        {a.translation_author || "автор не указан"}
      </a>
    </div>
  );
}
