import assert from "node:assert/strict";
import { test } from "node:test";

import { tokenize } from "./tokenize.ts";
import { closeFocusTarget, isActivationKey, isRussianPart, langOf, moveWord, wordIndexes } from "./word-nav.ts";
import type { FocusNode } from "./word-nav.ts";

test("слова текста — только токены со словом, по порядку", () => {
  // токены: I · " " · was · " " · reluctant · ", 42 " · times · "."
  assert.deepEqual(wordIndexes(tokenize("I was reluctant, 42 times.")), [0, 2, 4, 6]);
  assert.deepEqual(wordIndexes(tokenize(" — 42 —")), []);
  assert.deepEqual(wordIndexes(tokenize("")), []);
});

test("←/→ — соседнее слово, на краю текста фокус стоит", () => {
  const words = [0, 2, 4, 6];
  assert.equal(moveWord(words, 2, "ArrowRight"), 4);
  assert.equal(moveWord(words, 2, "ArrowLeft"), 0);
  assert.equal(moveWord(words, 6, "ArrowRight"), 6);
  assert.equal(moveWord(words, 0, "ArrowLeft"), 0);
  // одно слово — переходить некуда
  assert.equal(moveWord([3], 3, "ArrowRight"), 3);
  assert.equal(moveWord([3], 3, "ArrowLeft"), 3);
});

test("Home/End — первое и последнее слово", () => {
  assert.equal(moveWord([1, 5, 9], 5, "Home"), 1);
  assert.equal(moveWord([1, 5, 9], 5, "End"), 9);
});

test("из не-слова — к ближайшему слову в сторону стрелки", () => {
  const words = [1, 5, 9];
  assert.equal(moveWord(words, 3, "ArrowRight"), 5);
  assert.equal(moveWord(words, 3, "ArrowLeft"), 1);
  assert.equal(moveWord(words, -1, "ArrowRight"), 1);
  assert.equal(moveWord(words, 99, "ArrowLeft"), 9);
});

test("другие клавиши и текст без слов — не переход", () => {
  for (const key of ["ArrowUp", "ArrowDown", "Tab", "Enter", " ", "1", "Escape"]) {
    assert.equal(moveWord([0, 2], 0, key), null, key);
  }
  assert.equal(moveWord([], 0, "ArrowRight"), null);
  assert.equal(moveWord([], 0, "Home"), null);
});

test("Enter и пробел открывают попап, остальное — нет", () => {
  assert.equal(isActivationKey("Enter"), true);
  assert.equal(isActivationKey(" "), true);
  for (const key of ["Spacebar", "Space", "Escape", "ArrowRight", "1", "e"]) assert.equal(isActivationKey(key), false, key);
});

test("кусок с кириллицей — русский, латиница и знаки — нет", () => {
  assert.equal(isRussianPart("неохотный, нежелающий"), true);
  assert.equal(isRussianPart(" — значит «"), true);
  assert.equal(isRussianPart("Ёж"), true);
  assert.equal(isRussianPart(", 42 — "), false);
  assert.equal(isRussianPart("reluctant"), false);
  assert.equal(isRussianPart(""), false);
});

test("язык строки вне ClickableText: без кириллицы — en, с ней — язык страницы", () => {
  assert.equal(langOf("Airport English: checking in and boarding"), "en");
  assert.equal(langOf("BBC Learning English"), "en");
  assert.equal(langOf("Airport English: в аэропорту"), undefined);
  assert.equal(langOf("Урок 3"), undefined);
});

// node — узел-заглушка: селекторы, которым он отвечает сам, и родитель;
// closest — как у DOM: сам узел или ближайший предок
function node(own: string[], parentElement: FocusNode | null = null): FocusNode {
  const self: FocusNode = {
    parentElement,
    closest(selectors: string) {
      return own.includes(selectors) ? self : (parentElement?.closest(selectors) ?? null);
    },
  };
  return self;
}

test("попап закрыт изнутри: с клавиатуры фокус — слову, мышью — сессии вокруг текста или никому", () => {
  const session = node(["[tabindex]"], node(["main"]));
  const word = node(["[tabindex]", "[data-i]"], node(["span"], node(["article"], session)));
  assert.equal(closeFocusTarget(word, true), word, "открыт с клавиатуры");
  assert.equal(closeFocusTarget(word, false), session, "открыт мышью: не слово, а сессия практики");
  const lessonWord = node(["[tabindex]", "[data-i]"], node(["span"], node(["main"])));
  assert.equal(closeFocusTarget(lessonWord, false), null, "вокруг текста фокусируемого контейнера нет — фокус снимается");
  assert.equal(closeFocusTarget(null, true), null, "открытого слова нет");
});
