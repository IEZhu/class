import assert from "node:assert/strict";
import { test } from "node:test";

import {
  clozeCredit,
  clozeKeyAction,
  distractorRequestIds,
  isAnswerOption,
  itemOptions,
  llmErrorNote,
  MAX_RESPONSE_MS,
  responseMs,
  restartClock,
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

test("время ответа: заново с новым пунктом, с приходом вариантов и с уходом с ожидания во ввод", () => {
  const at = (index: number, mode: "choice" | "typing", ready: boolean) => ({ index, mode, ready });
  assert.equal(restartClock(null, at(0, "choice", true)), true, "первый пункт");
  assert.equal(restartClock(at(0, "choice", true), at(1, "choice", true)), true, "следующий пункт");
  assert.equal(restartClock(at(0, "choice", false), at(0, "choice", true)), true, "кнопки pending-пункта пришли");
  assert.equal(restartClock(at(0, "choice", true), at(0, "choice", true)), false);
  assert.equal(restartClock(at(0, "choice", false), at(0, "choice", false)), false, "ждём вариантов");
  assert.equal(restartClock(at(0, "typing", false), at(0, "typing", true)), false, "вводу варианты не нужны");
  assert.equal(restartClock(at(0, "choice", true), at(0, "typing", true)), false, "смена режима — не новый показ");
  assert.equal(restartClock(at(0, "typing", true), at(0, "choice", true)), false);
  assert.equal(restartClock(at(0, "choice", false), at(0, "typing", false)), true, "ушли с ожидания во ввод");
  assert.equal(restartClock(at(0, "typing", false), at(0, "choice", false)), false, "вернулись ждать вариантов");
  assert.equal(restartClock(at(0, "typing", false), at(0, "typing", false)), false);
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

const list = (id: number, options: string[]) => ({ id, options, distractor_source: "list" as const });
const pending = (id: number) => ({ id, options: [], distractor_source: "pending" as const });

test("без ключа LLM страница не просит варианты; с ключом — только pending", () => {
  const items = [list(1, ["a", "b", "c", "d"]), pending(2), pending(3)];
  assert.deepEqual(distractorRequestIds({ items, llm_available: false }), []);
  assert.deepEqual(distractorRequestIds({ items: [list(1, ["a"]), list(2, ["b"])], llm_available: true }), []);
  assert.deepEqual(distractorRequestIds({ items, llm_available: true }), [2, 3]);
});

test("у пункта один набор вариантов: пришедший позже не заменяет отданный выдачей", () => {
  const llm = { id: 1, options: ["ran", "walked", "drove", "flew"], distractor_source: "llm" as const };
  assert.deepEqual(itemOptions(list(1, ["run", "walk", "swim", "jump"]), { 1: llm }), ["run", "walk", "swim", "jump"]);
  assert.equal(itemOptions(pending(2), {}), null, "pending до ответа POST — без вариантов");
  assert.equal(itemOptions(pending(2), { 2: pending(2) }), null);
  assert.deepEqual(itemOptions(pending(2), { 2: { ...llm, id: 2 } }), llm.options);
});

test("причина вариантов словаря", () => {
  assert.equal(llmErrorNote(null), null);
  assert.match(llmErrorNote("rate_limited") ?? "", /лимит/);
  assert.match(llmErrorNote("llm_unavailable") ?? "", /недоступен/);
});

test("подпись Tatoeba: до ответа без ссылок на предложение, после — со ссылками", () => {
  const before = { author: "fixture_bank", license: "CC BY 2.0 FR", license_url: "https://creativecommons.org/licenses/by/2.0/fr/" };
  const credit = clozeCredit(before, null);
  assert.equal(credit?.author, "fixture_bank");
  assert.equal(credit?.url, null);
  assert.equal(credit?.translation, null);
  assert.equal(clozeCredit(null, null), null, "не Tatoeba — подписи нет");
  const after = clozeCredit(before, {
    ...before,
    url: "https://tatoeba.org/sentences/show/5",
    translation_author: "ru_author",
    translation_url: "https://tatoeba.org/sentences/show/106",
  });
  assert.equal(after?.url, "https://tatoeba.org/sentences/show/5");
  assert.deepEqual(after?.translation, { author: "ru_author", url: "https://tatoeba.org/sentences/show/106" });
  assert.equal(after?.licenseUrl, before.license_url);
});
