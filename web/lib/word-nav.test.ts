import assert from "node:assert/strict";
import { test } from "node:test";

import { tokenize } from "./tokenize.ts";
import { isActivationKey, isRussianPart, moveWord, wordIndexes } from "./word-nav.ts";

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
