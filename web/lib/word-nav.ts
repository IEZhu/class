// Клавиатура и язык частей <ClickableText> (DESIGN §5.1, §5.2). Чистые
// функции без DOM и React: их проверяют тесты lib/word-nav.test.ts
// (npm test).

import { SESSION } from "./practice-keys.ts";
import type { Token } from "./tokenize";

// wordIndexes — номера токенов-слов текста по порядку: по ним ходит
// фокус, пробелы и пунктуация между словами пропускаются
export function wordIndexes(tokens: readonly Token[]): number[] {
  const words: number[] = [];
  tokens.forEach((t, i) => {
    if (t.key) words.push(i);
  });
  return words;
}

// moveWord — к какому слову перейти фокусу из слова current по клавише
// key: ←/→ — соседнее слово, Home/End — первое и последнее. На краю
// текста фокус остаётся на месте, по кругу не ходит: следующий текст —
// по Tab. null — клавиша не для перехода (или слов нет), обработчик её
// пропускает.
export function moveWord(words: readonly number[], current: number, key: string): number | null {
  if (words.length === 0) return null;
  const last = words[words.length - 1];
  switch (key) {
    case "Home":
      return words[0];
    case "End":
      return last;
    case "ArrowRight":
      return words.find((w) => w > current) ?? last;
    case "ArrowLeft": {
      let prev = words[0];
      for (const w of words) {
        if (w >= current) break;
        prev = w;
      }
      return prev;
    }
    default:
      return null;
  }
}

// isActivationKey — Enter и пробел открывают попап слова, как у кнопки
export function isActivationKey(key: string): boolean {
  return key === "Enter" || key === " ";
}

const CYRILLIC = /\p{Script=Cyrillic}/u;

// isRussianPart — кусок между словами с кириллицей. Корень текста помечен
// lang="en" (WCAG 3.1.2), но через <ClickableText> идут и русские тексты:
// переводы на обороте карточки, объяснение «объясни». Такие куски
// получают свой lang="ru", чтобы экранный диктор не читал их английским
// голосом. Латинские слова кириллицы не содержат — им проверка не нужна.
export function isRussianPart(text: string): boolean {
  return CYRILLIC.test(text);
}

// langOf — lang строки неизвестного языка вне <ClickableText>: названия
// роликов и каналов, имена авторов Tatoeba (WCAG 3.1.2). Без кириллицы —
// "en", с ней — undefined: остаётся язык страницы, русский.
export function langOf(text: string): "en" | undefined {
  return isRussianPart(text) ? undefined : "en";
}

// Минимум узла DOM, нужный closeFocusTarget
export interface FocusNode {
  readonly parentElement: FocusNode | null;
  closest(selectors: string): FocusNode | null;
}

// closeFocusTarget — куда вернуть фокус, когда попап слова закрыт изнутри
// (Escape, «Закрыть»): на слово word — с него и продолжают, следующий Tab
// идёт от него, а не с начала страницы (WCAG 2.4.3). Исключение — попап,
// открытый мышью или касанием в сессии практики: тогда фокус — сессии
// (SESSION), иначе слово забрало бы пробел, Enter и 1–4 карточек и cloze
// (lib/practice-keys). null — открытого слова нет.
export function closeFocusTarget(word: FocusNode | null, keyboard: boolean): FocusNode | null {
  if (!word) return null;
  if (keyboard) return word;
  return word.parentElement?.closest(SESSION) ?? word;
}
