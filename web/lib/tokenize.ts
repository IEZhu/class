// Токенизация текста для <ClickableText> (S3-1, ADR-026). Чистые функции
// без React: их проверяют тесты lib/tokenize.test.ts (npm test).

export interface Token {
  text: string;
  // Смещение токена в исходном тексте (UTF-16), для предложения вокруг слова
  start: number;
  // key — ключ слова для статуса и клика (wordKey); null — пробелы,
  // пунктуация, цифры, кириллица и слишком длинные «слова»
  key: string | null;
}

// Слово — как lemma.IsWord в api: латинская буква, дальше буквы и
// диакритика, внутри — одиночные апостроф или дефис. Апостроф в конце —
// только после s (students'), иначе закрывающая кавычка 'no' прилипла
// бы к слову. Только латиница: кириллические пояснения в материалах не
// английские слова, клик по ним бесполезен.
const WORD = /\p{Script=Latin}[\p{Script=Latin}\p{M}]*(?:['’ʼ-][\p{Script=Latin}\p{M}]+)*(?:(?<=[sS])['’ʼ])?/gu;

// MAX_WORD_BYTES — потолок lemma.MaxWordBytes: длиннее api не примет
export const MAX_WORD_BYTES = 64;

// MAX_CONTEXT — потолок context_sentence у POST /terms/click (в рунах;
// UTF-16-единиц не меньше, чем рун, поэтому срез по ним безопасен)
export const MAX_CONTEXT = 1000;

function utf8Length(s: string): number {
  let n = 0;
  for (const ch of s) {
    const cp = ch.codePointAt(0) ?? 0;
    n += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
  }
  return n;
}

// wordKey — ключ слова: нижний регистр и ASCII-апостроф. Притяжательные
// и леммы — забота api (lemma.Lemma), web их не повторяет.
export function wordKey(word: string): string {
  return word.toLowerCase().replace(/[’ʼ]/g, "'");
}

// tokenize режет текст на слова и всё между ними; склейка text всех
// токенов даёт исходный текст без потерь (пробелы, переводы строк,
// пунктуация остаются на месте).
export function tokenize(text: string): Token[] {
  const tokens: Token[] = [];
  let last = 0;
  for (const m of text.matchAll(WORD)) {
    const start = m.index;
    if (start > last) tokens.push({ text: text.slice(last, start), start: last, key: null });
    let word = m[0];
    // 'cats' в кавычках: закрывающий апостроф — кавычка, а не s'
    if (/['’ʼ]$/.test(word) && /['‘’ʼ]/.test(text[start - 1] ?? "")) word = word.slice(0, -1);
    tokens.push({ text: word, start, key: utf8Length(word) <= MAX_WORD_BYTES ? wordKey(word) : null });
    last = start + word.length;
  }
  if (last < text.length) tokens.push({ text: text.slice(last), start: last, key: null });
  return tokens;
}

// uniqueKeys — разные ключи слов текста: столько и уходит в lookup статусов.
export function uniqueKeys(tokens: Token[]): string[] {
  const keys = new Set<string>();
  for (const t of tokens) if (t.key) keys.add(t.key);
  return [...keys];
}

// Конец предложения: . ! ? … (с закрывающими кавычками и скобками) перед
// пробелом, либо перевод строки.
const SENTENCE_END = /[.!?…]+["'”’)\]]*(?=\s)|\n/g;

// sentenceAround — предложение вокруг слова [start, end) для context_sentence.
// Границы грубые (Mr. Smith разрежется), этого хватает: контекст нужен
// «объясни» (S5-8) и не влияет на перевод. Длиннее MAX_CONTEXT — окно
// вокруг слова.
export function sentenceAround(text: string, start: number, end: number): string {
  let from = 0;
  let to = text.length;
  for (const m of text.matchAll(SENTENCE_END)) {
    const stop = m.index + m[0].length;
    if (stop <= start) from = stop;
    else if (m.index >= end) {
      to = m[0] === "\n" ? m.index : stop;
      break;
    }
  }
  let sentence = text.slice(from, to);
  if (sentence.length > MAX_CONTEXT) {
    const half = Math.floor((MAX_CONTEXT - (end - start)) / 2);
    const cut = Math.min(Math.max(0, start - from - half), sentence.length - MAX_CONTEXT);
    sentence = sentence.slice(cut, cut + MAX_CONTEXT);
  }
  return sentence.trim();
}
