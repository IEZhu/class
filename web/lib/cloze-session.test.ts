import assert from "node:assert/strict";
import { test } from "node:test";

import {
  clozeKeyAction,
  isAnswerOption,
  MAX_RESPONSE_MS,
  responseMs,
  scheduleText,
  sourceLabel,
  verdictText,
} from "./cloze-session.ts";

test("цифры выбирают вариант только до ответа и в режиме вариантов", () => {
  assert.deepEqual(clozeKeyAction("1", false, "choice", 4), { kind: "choose", index: 0 });
  assert.deepEqual(clozeKeyAction("4", false, "choice", 4), { kind: "choose", index: 3 });
  assert.equal(clozeKeyAction("5", false, "choice", 4), null, "вариантов четыре");
  assert.equal(clozeKeyAction("0", false, "choice", 4), null);
  assert.equal(clozeKeyAction("12", false, "choice", 4), null);
  assert.equal(clozeKeyAction("2", false, "typing", 4), null, "при вводе цифры — не выбор");
  assert.equal(clozeKeyAction("Enter", false, "choice", 4), null, "до ответа дальше не идём");
  assert.equal(clozeKeyAction("1", true, "choice", 4), null, "после ответа вариант не меняется");
});

test("после ответа Enter и пробел — следующий пункт", () => {
  for (const mode of ["choice", "typing"] as const) {
    assert.deepEqual(clozeKeyAction("Enter", true, mode, 4), { kind: "next" });
    assert.deepEqual(clozeKeyAction(" ", true, mode, 4), { kind: "next" });
    assert.equal(clozeKeyAction("a", true, mode, 4), null);
  }
});

test("время ответа — целые миллисекунды в границах api", () => {
  assert.equal(responseMs(1000, 2500.4), 1500);
  assert.equal(responseMs(2000, 1000), 0, "часы назад — не отрицательное");
  assert.equal(responseMs(0, 10 * MAX_RESPONSE_MS), MAX_RESPONSE_MS);
});

test("итог ответа и график", () => {
  const base = { answer: "ran", rating: "easy", counted: true } as const;
  assert.equal(verdictText({ ...base, correct: true, exact: true }), "Верно!");
  assert.equal(verdictText({ ...base, correct: true, exact: false }), "Верно — в предложении «ran»");
  assert.equal(verdictText({ ...base, correct: false, exact: false, rating: "again" }), "Неверно — в предложении «ran»");
  assert.equal(scheduleText({ ...base, correct: true, exact: true }), "в график повторений: легко");
  assert.match(scheduleText({ ...base, correct: true, exact: true, counted: false }), /не засчитан/);
  assert.equal(sourceLabel("material"), "из материалов урока");
});

test("ответ среди вариантов: форма у вариантов LLM, лемма у вариантов словаря", () => {
  const result = { answer: "Ran", lemma: "run" };
  assert.ok(isAnswerOption("ran", result));
  assert.ok(isAnswerOption("run", result));
  assert.ok(!isAnswerOption("walk", result));
});
