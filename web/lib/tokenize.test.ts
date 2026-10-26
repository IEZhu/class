import assert from "node:assert/strict";
import { test } from "node:test";

import { MAX_CONTEXT, sentenceAround, tokenize, uniqueKeys, wordKey } from "./tokenize.ts";

const words = (text: string) => tokenize(text).filter((t) => t.key !== null).map((t) => t.text);

test("склейка токенов возвращает текст без потерь", () => {
  const text = "  Hello,\tworld!\n\n«Цитата» — 42 apples… (really?) ";
  assert.equal(tokenize(text).map((t) => t.text).join(""), text);
  for (const t of tokenize(text)) assert.equal(text.slice(t.start, t.start + t.text.length), t.text);
  assert.deepEqual(tokenize(""), []);
});

test("слова: апостроф и дефис внутри, пунктуация и цифры снаружи", () => {
  assert.deepEqual(words("Don't stop—it's a well-known fact, isn't it?"), [
    "Don't", "stop", "it's", "a", "well-known", "fact", "isn't", "it",
  ]);
  assert.deepEqual(words("The students' books and 'yes' or 'no'"), [
    "The", "students'", "books", "and", "yes", "or", "no",
  ]);
  assert.deepEqual(words("rock--roll -run trail- 3D x2 a1b"), ["rock", "roll", "run", "trail", "D", "x", "a", "b"]);
  // типографский и модифицирующий апострофы — тоже апострофы
  assert.deepEqual(words("don’t donʼt"), ["don’t", "donʼt"]);
  // диакритика — часть слова, кириллица словами не считается
  assert.deepEqual(words("café naïve Привет мир"), ["café", "naïve"]);
});

test("ключ слова: нижний регистр и ASCII-апостроф", () => {
  assert.equal(wordKey("Don’t"), "don't");
  assert.equal(wordKey("USA"), "usa");
  assert.deepEqual(
    tokenize("It’s").map((t) => t.key),
    ["it's"],
  );
  assert.deepEqual(uniqueKeys(tokenize("Run, run, RUN! ran.")), ["run", "ran"]);
});

test("слишком длинное слово не кликается (lemma.MaxWordBytes)", () => {
  const long = "a".repeat(65);
  assert.deepEqual(tokenize(long).map((t) => t.key), [null]);
  assert.deepEqual(tokenize("a".repeat(64)).map((t) => t.key), ["a".repeat(64)]);
  // 32 буквы по 2 байта — ровно 64 байта
  assert.equal(tokenize("é".repeat(32))[0].key, "é".repeat(32));
  assert.equal(tokenize("é".repeat(33))[0].key, null);
});

test("предложение вокруг слова", () => {
  const text = "First one. He said: \"Run home!\" Then he left.\nNext line here";
  const at = (w: string) => {
    const start = text.indexOf(w);
    return sentenceAround(text, start, start + w.length);
  };
  assert.equal(at("First"), "First one.");
  assert.equal(at("home"), 'He said: "Run home!"');
  assert.equal(at("left"), "Then he left.");
  assert.equal(at("Next"), "Next line here");
});

test("длинный абзац без точек режется окном вокруг слова", () => {
  const text = "word ".repeat(400) + "target " + "word ".repeat(400);
  const start = text.indexOf("target");
  const ctx = sentenceAround(text, start, start + "target".length);
  assert.ok(ctx.length <= MAX_CONTEXT);
  assert.ok(ctx.includes("target"));
  const atEnd = sentenceAround(text, text.length - 5, text.length - 1);
  assert.ok(atEnd.length <= MAX_CONTEXT && atEnd.endsWith("word"));
});
