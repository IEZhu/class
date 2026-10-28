"use client";

import { cloneElement, createContext, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { CSSProperties, KeyboardEvent, MouseEvent, ReactElement, ReactNode } from "react";
import { createPortal } from "react-dom";

import type { TermClick, TermExample, TermExplanation } from "../lib/api";
import { placePopover, POPUP_GAP } from "../lib/popover-place";
import type { PopoverPlace } from "../lib/popover-place";
import { isTextSelection } from "../lib/selection";
import type { TermStatus } from "../lib/term-status";
import { sentenceAround, tokenize, uniqueKeys } from "../lib/tokenize";
import type { Token } from "../lib/tokenize";
import { closeFocusTarget, isActivationKey, isRussianPart, langOf, moveWord, wordIndexes } from "../lib/word-nav";
import styles from "./clickable-text.module.css";
import { RequestError, requestJson } from "./forms";
import TermStatusControl, { termStatuses } from "./term-status-control";
import { Alert } from "./ui/alert";
import type { AlertTone } from "./ui/alert";
import { Button } from "./ui/button";
import { cx } from "./ui/cx";
import { Icon } from "./ui/icons";
import { Skeleton } from "./ui/skeleton";

// <ClickableText> — единственный рендер текста в системе (инвариант,
// S3-1, ADR-026): материалы, домашка, транскрипт, субтитры, Tatoeba,
// сгенерированные тексты. Режет текст на слова (lib/tokenize), красит
// их по статусу в словаре зрителя и по клику открывает попап слова.
//
// Клавиатура (DESIGN §5.2): слова — role="button", у текста одна
// остановка Tab (слово с tabIndex 0, остальные -1), ←/→ и Home/End
// переходят между словами, Enter и пробел открывают попап; Escape
// закрывает его и возвращает фокус на слово, а открытый мышью в сессии
// практики — на сессию (lib/word-nav closeFocusTarget). Признак
// слова в DOM — атрибут data-i (номер токена): по нему обработчики
// клавиш карточек и cloze узнают нажатие на слове. Корень — lang="en"
// (DESIGN §5.1).

interface Scope {
  lessonId: number | null;
  studentId: number | null;
}

const ScopeContext = createContext<Scope>({ lessonId: null, studentId: null });

// ClickableTextScope — что текстам страницы известно о ней: урок (клик
// уходит с lesson_id — по нему S3-7 запишет кандидатов словаря урока,
// а «в мой словарь» — источник слова) и чей личный словарь красит текст:
// id зрителя-студента (раскраска и статус — только у студента, 04-api.md),
// null — у зрителя словаря нет. По id кэш статусов вкладки не покажет
// следующему вошедшему чужие цвета. Без обёртки текст кликается, но не
// красится.
export function ClickableTextScope({
  lessonId = null,
  studentId,
  children,
}: {
  lessonId?: number | null;
  studentId: number | null;
  children: ReactNode;
}) {
  const value = useMemo(() => ({ lessonId, studentId }), [lessonId, studentId]);
  return <ScopeContext.Provider value={value}>{children}</ScopeContext.Provider>;
}

// Раскраска (DESIGN §3.10): чем ниже статус, тем ярче фон, и у каждого
// статуса свой узор подчёркивания — не только цвет (WCAG 1.4.1); «знаю»
// и слова вне словаря — без выделения, иначе текст новичка стал бы сплошь
// цветным (ADR-026). Номер — статус, 0 — слова нет в словаре.
const WORD_CLASS: readonly string[] = ["w", "w w1", "w w2", "w w3", "w w4", "w"];

// Клик по слову: форма из текста, ключ (wordKey), предложение вокруг
interface WordClick {
  surface: string;
  key: string;
  context: string;
}

interface OpenPopup extends WordClick {
  // Номер клика: новый клик в том же тексте пересоздаёт попап
  seq: number;
  // Токен открытого слова: подсветка и возврат фокуса
  i: number;
  // Открыт с клавиатуры: фокус в попапе прокручивает страницу к нему, а
  // закрытие возвращает фокус на слово
  keyboard: boolean;
  left: number;
  // Рамка открытого слова — его прямоугольник в координатах документа;
  // scrollY — прокрутка страницы в момент открытия (слово в окне — ring
  // минус scrollY)
  ring: { top: number; left: number; width: number; height: number };
  scrollY: number;
}

// Готовые элементы слов текста и их классы по номеру токена
interface WordCache {
  tokens: Token[];
  cls: string[];
  els: ReactElement<{ tabIndex: number; className: string; "aria-expanded"?: boolean }>[];
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
  const words = useMemo(() => wordIndexes(tokens), [tokens]);
  // Куски между словами — один раз на текст; с кириллицей — в lang="ru"
  const parts = useMemo(
    () =>
      tokens.map((t, i) =>
        t.key ? null : isRussianPart(t.text) ? (
          <span key={i} lang="ru">
            {t.text}
          </span>
        ) : (
          t.text
        ),
      ),
    [tokens],
  );
  const wordCache = useRef<WordCache | null>(null);
  useSyncExternalStore(termStatuses.subscribe, termStatuses.getVersion, serverVersion);
  const [popup, setPopup] = useState<OpenPopup | null>(null);
  // Слово, на котором стоит остановка Tab текста; null — первое слово
  const [current, setCurrent] = useState<number | null>(null);
  const root = useRef<HTMLSpanElement>(null);
  const close = useRef(() => setPopup(null)).current;
  // Закрытие из попапа (inside — Escape и «Закрыть»): фокус — по
  // closeFocusTarget (lib/word-nav): на слово, а у попапа, открытого
  // мышью или касанием в сессии практики, — на сессию. Клик мимо попапа
  // фокус не трогает: он уходит туда, куда кликнули (на странице практики
  // щелчок по пустому месту отдаёт его сессии — practice/session-focus.ts)
  const dismiss = useRef((inside: boolean, keyboard: boolean) => {
    if (!inside) {
      setPopup(null);
      return;
    }
    const word = root.current?.querySelector<HTMLElement>(':scope > [aria-expanded="true"]') ?? null;
    const target = closeFocusTarget(word, keyboard) as HTMLElement | null;
    setPopup(null);
    if (target) target.focus({ preventScroll: !keyboard });
    else if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  }).current;

  useEffect(() => {
    if (scope.studentId !== null) void termStatuses.request(scope.studentId, uniqueKeys(tokens));
  }, [scope.studentId, tokens]);

  useEffect(
    () => () => {
      if (closeOpenPopup === close) closeOpenPopup = null;
    },
    [close],
  );

  // Слово под событием — только своё: попап в портале, и его события
  // всплывают сюда по дереву React, но его слова — не наши
  function wordAt(target: EventTarget, container: HTMLElement): { el: HTMLElement; i: number; t: Token } | null {
    const el = (target as HTMLElement).closest<HTMLElement>("[data-i]");
    if (!el || !container.contains(el)) return null;
    const i = Number(el.dataset.i);
    const t = tokens[i];
    return t?.key ? { el, i, t } : null;
  }

  function open(el: HTMLElement, i: number, t: Token, keyboard: boolean) {
    setCurrent(i);
    const word = { surface: t.text, key: t.key ?? "", context: sentenceAround(text, t.start, t.start + t.text.length) };
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
      i,
      keyboard,
      left: rect.left + window.scrollX,
      ring: { top: rect.top + window.scrollY, left: rect.left + window.scrollX, width: rect.width, height: rect.height },
      scrollY: window.scrollY,
    }));
  }

  // Один обработчик на текст вместо тысяч у слов: в транскрипте урока
  // десятки тысяч токенов
  function handleClick(e: MouseEvent<HTMLSpanElement>) {
    // Выделение текста мышью — не клик: ни попапа, ни перемотки реплики
    // вокруг текста
    if (isTextSelection(window.getSelection())) {
      e.stopPropagation();
      return;
    }
    const hit = wordAt(e.target, e.currentTarget);
    if (!hit) return;
    // Клик по слову — этого текста: строка вокруг (перемотка реплики
    // транскрипта, S2-4) его не получает
    e.stopPropagation();
    open(hit.el, hit.i, hit.t, false);
  }

  // Клавиши слова — тоже один обработчик на текст. Обработанные клавиши
  // дальше не всплывают: пробел на слове не листает страницу и не
  // переворачивает карточку
  function handleKeyDown(e: KeyboardEvent<HTMLSpanElement>) {
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    const hit = wordAt(e.target, e.currentTarget);
    if (!hit) return;
    if (isActivationKey(e.key)) {
      e.preventDefault();
      e.stopPropagation();
      if (!e.repeat) open(hit.el, hit.i, hit.t, true);
      return;
    }
    const next = moveWord(words, hit.i, e.key);
    if (next === null) return;
    e.preventDefault();
    e.stopPropagation();
    setCurrent(next);
    e.currentTarget.querySelector<HTMLElement>(`:scope > [data-i="${next}"]`)?.focus();
  }

  const tabStop = current !== null && tokens[current]?.key ? current : words[0];
  const openIndex = popup?.i ?? -1;
  const studentId = scope.studentId;
  // Элементы слов живут между перерисовками, пока у слова тот же класс:
  // React пропускает тот же элемент, не сравнивая свойства. Тогда ←/→,
  // открытие попапа и смена статуса трогают в DOM только свои слова —
  // в длинном тексте это сотни пропущенных span на каждое действие.
  // Кэш пишется в ref во время рендера намеренно: элемент — неизменяемое
  // описание, одно и то же для слова с тем же классом, и брошенный React
  // рендер ничего в нём не портит.
  const cache = wordCache.current?.tokens === tokens ? wordCache.current : (wordCache.current = { tokens, cls: [], els: [] });

  return (
    <span ref={root} lang="en" className={styles.text} onClick={handleClick} onKeyDown={handleKeyDown}>
      {tokens.map((t, i) => {
        if (!t.key) return parts[i];
        const cls = WORD_CLASS[(studentId !== null && termStatuses.statusOf(studentId, t.key)) || 0];
        let el = cache.cls[i] === cls ? cache.els[i] : undefined;
        if (!el) {
          el = (
            <span key={i} data-i={i} role="button" tabIndex={-1} className={cls}>
              {t.text}
            </span>
          );
          cache.cls[i] = cls;
          cache.els[i] = el;
        }
        if (i !== tabStop && i !== openIndex) return el;
        return cloneElement(el, {
          tabIndex: i === tabStop ? 0 : -1,
          "aria-expanded": i === openIndex || undefined,
        });
      })}
      {popup && createPortal(<TermPopup key={popup.seq} start={popup} onClose={dismiss} />, document.body)}
    </span>
  );
}

const POPUP_WIDTH = 360;

// hasTerm — у слова есть термин: term_id 0 api отдаёт незнакомому слову,
// термина не заводя, — в словарь его не добавить и статуса у него нет
function hasTerm(d: TermClick): boolean {
  return d.term_id > 0;
}

// Перебор Tab внутри попапа: пока он открыт, фокус не уходит в конец
// страницы (попап — последний в body) — клик мимо попапа его закрывает,
// и для клавиатуры он такой же модальный
const FOCUSABLE = 'a[href], button:not(:disabled), [tabindex="0"]';

function TermPopup({ start, onClose }: { start: OpenPopup; onClose: (inside: boolean, keyboard: boolean) => void }) {
  const scope = useContext(ScopeContext);
  useSyncExternalStore(termStatuses.subscribe, termStatuses.getVersion, serverVersion);
  const [word, setWord] = useState<WordClick>(start);
  const [data, setData] = useState<TermClick | null>(null);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [place, setPlace] = useState<PopoverPlace | null>(null);

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
        if (scope.studentId !== null && hasTerm(d)) termStatuses.setStatus(scope.studentId, d.lemma, d.status);
        setData(d);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "не удалось загрузить слово");
      });
    return () => {
      cancelled = true;
    };
  }, [word, scope.lessonId, scope.studentId]);

  // Фокус — на заголовок попапа (DESIGN §4.2): при открытии и при переходе
  // к другому слову — слово, на котором был фокус, пропадает на время
  // загрузки. Открытый мышью попап страницу не прокручивает, как и раньше.
  // Фокус ставится в начале следующего кадра: клик не ждёт раскладки
  // страницы, которую focus() иначе потребовал бы сразу, — кадр
  // раскладывает её один раз, до отрисовки попапа.
  useEffect(() => {
    const frame = requestAnimationFrame(() => heading.current?.focus({ preventScroll: !start.keyboard }));
    return () => cancelAnimationFrame(frame);
  }, [word, start.keyboard]);

  // Место у слова (lib/popover-place): после раскладки, до отрисовки, и
  // заново при каждом изменении высоты попапа (пришли данные слова,
  // объяснение). Наибольшая высота — из CSS .popover: её читаем до того,
  // как место её ограничит
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const cap = parseFloat(getComputedStyle(el).maxHeight) || window.innerHeight;
    const word = { top: start.ring.top - start.scrollY, bottom: start.ring.top + start.ring.height - start.scrollY };
    function update() {
      if (!el) return;
      const next = placePopover(word, el.offsetHeight, cap, window.innerHeight);
      setPlace((prev) => (prev && prev.top === next.top && prev.maxHeight === next.maxHeight ? prev : next));
    }
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => observer.disconnect();
  }, [start]);

  useEffect(() => {
    function onKey(e: globalThis.KeyboardEvent) {
      if (e.key === "Escape") onClose(true, start.keyboard);
    }
    function onDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose(false, start.keyboard);
    }
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onDown);
    };
  }, [onClose, start.keyboard]);

  function keepTab(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== "Tab") return;
    const items = [...e.currentTarget.querySelectorAll<HTMLElement>(FOCUSABLE)];
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === heading.current)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  // Попап не вылезает за правый край окна; место — CSS-переменными, на
  // телефоне .popover их не читает и становится листом снизу. До расчёта
  // места — под словом
  const left = Math.max(window.scrollX + 8, Math.min(start.left, window.scrollX + window.innerWidth - POPUP_WIDTH - 8));
  const top = place ? place.top + start.scrollY : start.ring.top + start.ring.height + POPUP_GAP;
  const position = {
    "--pop-top": `${top}px`,
    "--pop-left": `${left}px`,
    "--pop-max-h": place && `${place.maxHeight}px`,
  } as CSSProperties;
  const r = start.ring;
  const ring = { "--ring-top": `${r.top}px`, "--ring-left": `${r.left}px`, "--ring-w": `${r.width}px`, "--ring-h": `${r.height}px` } as CSSProperties;
  const status = data && scope.studentId !== null ? (termStatuses.statusOf(scope.studentId, word.key) ?? null) : null;

  return (
    <>
      {/* Подложка листа на телефоне: касание по ней — «мимо попапа» */}
      <div className="backdrop backdrop--phone" aria-hidden="true" />
      {/* Рамка открытого слова (DESIGN §3.10) — отдельным слоем над ним, а
          не outline у самого слова: outline у слова без фона заново
          раскладывал бы весь абзац, в длинном тексте — сотни слов */}
      <span className={styles.ring} style={ring} aria-hidden="true" />
      {/* Клики попапа по дереву React всплыли бы в обработчики вокруг
          текста (перемотка реплики): попап — не часть строки, в которой открыт */}
      <div
        ref={ref}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={keepTab}
        role="dialog"
        aria-modal="true"
        aria-label={`Слово ${word.surface}`}
        className={cx("popover", place && styles.placed)}
        style={position}
      >
        <div className="sheet-handle" aria-hidden="true" />
        <div className={styles.head}>
          <div className={styles.titles}>
            <h2 ref={heading} tabIndex={-1} lang="en" className={styles.lemma}>
              {data?.lemma ?? word.surface}
            </h2>
            {data && data.lemma !== word.key && (
              <p lang="en" className={styles.form}>
                {word.surface}
              </p>
            )}
          </div>
          <button type="button" className="icon-btn icon-btn--sm" onClick={() => onClose(true, start.keyboard)} aria-label="Закрыть">
            <Icon name="x" />
          </button>
        </div>
        {!data && (
          <div className={styles.body}>
            {error ? <Alert tone="danger">{error}</Alert> : <Skeleton label="Загружаем…" />}
          </div>
        )}
        {data && (
          <>
            <p className={styles.tr}>{data.translation ? data.translation.text : <span className="muted">перевода пока нет</span>}</p>
            <div className={styles.body}>
              <WordActions key={`${data.term_id}:${word.context}`} data={data} word={word} status={status} onWord={setWord} />
              {data.definitions.length > 0 && (
                <section className={styles.section}>
                  <h3 className={styles.sectionTitle}>Значения</h3>
                  <ol className={styles.defs}>
                    {data.definitions.map((d, i) => (
                      <li key={i}>
                        <span lang="en" className={styles.pos}>
                          {d.pos}
                        </span>{" "}
                        {d.word !== data.lemma && <b lang="en">{d.word} </b>}
                        {d.glosses.map((g, j) => (
                          <span key={j}>
                            {j > 0 && "; "}
                            <ClickableText text={g} onWord={setWord} />
                          </span>
                        ))}
                        {d.translations_ru.length > 0 && <span className="muted"> — {d.translations_ru.join(", ")}</span>}
                      </li>
                    ))}
                  </ol>
                  {/* CC BY-SA требует атрибуции (ADR-019) */}
                  <p className="attr">
                    <a href={`https://en.wiktionary.org/wiki/${encodeURIComponent(data.definitions[0].word)}`} target="_blank" rel="noreferrer">
                      Wiktionary
                    </a>
                    ,{" "}
                    <a href="https://creativecommons.org/licenses/by-sa/4.0/" target="_blank" rel="noreferrer">
                      CC BY-SA
                    </a>
                  </p>
                </section>
              )}
              {data.examples.length > 0 && (
                <section className={styles.section}>
                  <h3 className={styles.sectionTitle}>Примеры</h3>
                  <ul className={styles.examples}>
                    {data.examples.map((ex, i) => (
                      <li key={i} className={styles.example}>
                        <ClickableText text={ex.text} onWord={setWord} />
                        <span className="muted">{ex.translation}</span>
                        <ExampleAuthors example={ex} />
                      </li>
                    ))}
                  </ul>
                  {/* CC BY 2.0 FR требует подписи: авторы — у каждого примера (ADR-028) */}
                  <p className="attr">
                    <a href="https://tatoeba.org" target="_blank" rel="noreferrer">
                      Tatoeba
                    </a>
                    ,{" "}
                    <a href={data.examples[0].attribution.license_url} target="_blank" rel="noreferrer">
                      {data.examples[0].attribution.license}
                    </a>
                  </p>
                </section>
              )}
            </div>
          </>
        )}
      </div>
    </>
  );
}

// WordActions — «В моём словаре» (статус слова у студента) и «Объяснить».
// Ключ у компонента — термин и предложение: переход попапа к другому
// слову сбрасывает объяснение.
function WordActions({
  data,
  word,
  status,
  onWord,
}: {
  data: TermClick;
  word: WordClick;
  status: TermStatus | null;
  onWord: (word: WordClick) => void;
}) {
  const scope = useContext(ScopeContext);
  const explain = useExplain(data.term_id, word, scope.lessonId);
  // Кнопка «Объяснить» с фокусом уступает место ответу: фокус — на его
  // заголовок, а не в никуда
  const refocus = useRef(false);
  const answerTitle = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (explain.text === null || !refocus.current) return;
    refocus.current = false;
    answerTitle.current?.focus();
  }, [explain.text]);

  const canExplain = hasTerm(data) && data.explain_available && explain.text === null;
  // Пока идёт запрос — aria-disabled, а не disabled: тот снял бы фокус с кнопки
  const explainButton = canExplain && (
    <Button
      type="button"
      variant="secondary"
      size="sm"
      aria-disabled={explain.busy || undefined}
      busy={explain.busy}
      onClick={(e) => {
        refocus.current = e.currentTarget === document.activeElement;
        void explain.run();
      }}
    >
      {explain.busy ? <span className="spinner" aria-hidden="true" /> : <Icon name="spark" />}
      {explain.busy ? "Объясняем…" : "Объяснить"}
    </Button>
  );

  return (
    <>
      {scope.studentId !== null && hasTerm(data) ? (
        <section className={styles.section}>
          {status !== null && <h3 className={styles.sectionTitle}>В моём словаре</h3>}
          <TermStatusControl
            studentId={scope.studentId}
            termId={data.term_id}
            lemma={data.lemma}
            status={status}
            lessonId={scope.lessonId}
            actions={explainButton}
          />
        </section>
      ) : (
        explainButton && <div className="row">{explainButton}</div>
      )}
      {explain.error && <Alert tone={explain.error.tone}>{explain.error.message}</Alert>}
      {explain.text !== null && (
        <section className={styles.explain}>
          <h3 ref={answerTitle} tabIndex={-1} className={styles.sectionTitle}>
            Объяснение
          </h3>
          {explain.text.split("\n\n").map((p, i) => (
            <p key={i}>
              <ClickableText text={p} onWord={onWord} />
            </p>
          ))}
        </section>
      )}
    </>
  );
}

// useExplain — «объясни» (S5-8, ADR-039): LLM объясняет слово в том
// предложении, где его кликнули. Ответ — простой текст: абзацы идут в
// ClickableText (инвариант единого рендера), английские слова в нём
// кликаются и переводят попап к ним. Запрос — только по явному нажатию:
// вызов стоит денег, повтор того же предложения api берёт из кэша.
// Суточный лимит (429) и недоступный LLM (503) — предупреждение, а не
// ошибка: кнопка остаётся, попробовать можно позже.
function useExplain(termId: number, word: WordClick, lessonId: number | null) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ message: string; tone: AlertTone } | null>(null);
  const [text, setText] = useState<string | null>(null);

  async function run() {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const d = await requestJson<TermExplanation>(`/api/terms/${termId}/explain`, "POST", {
        surface: word.surface,
        context_sentence: word.context,
        lesson_id: lessonId ?? undefined,
      });
      setText(d.explanation);
    } catch (e) {
      const soft = e instanceof RequestError && (e.status === 429 || e.status === 503);
      setError({ message: e instanceof Error ? e.message : "не удалось получить объяснение", tone: soft ? "warning" : "danger" });
    } finally {
      setBusy(false);
    }
  }

  return { busy, error, text, run };
}

// ExampleAuthors — подпись примера Tatoeba: авторы предложения и перевода
// со ссылками на их страницы (условие CC BY 2.0 FR, ADR-028); та же
// подпись — у оборота карточек (S4-3) и предложений тексто-практики (S5-7).
// Имена авторов без кириллицы — lang="en" (WCAG 3.1.2)
export function ExampleAuthors({ example: { attribution: a } }: { example: TermExample }) {
  const author = a.author || "автор не указан";
  const translator = a.translation_author || "автор не указан";
  return (
    <div className="attr">
      <a href={a.url} target="_blank" rel="noreferrer" lang={langOf(author)}>
        {author}
      </a>
      {" · перевод: "}
      <a href={a.translation_url} target="_blank" rel="noreferrer" lang={langOf(translator)}>
        {translator}
      </a>
    </div>
  );
}
