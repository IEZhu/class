import assert from "node:assert/strict";
import { test } from "node:test";

import { isSessionKey, ownsKey } from "./practice-keys.ts";
import type { KeyTarget } from "./practice-keys.ts";

// el — элемент-заглушка: каким простым селекторам отвечает он сам и его
// предки (от ближнего к дальнему); closest — как у DOM, по списку через
// запятую
function el(...chain: string[][]): KeyTarget {
  return {
    closest(selectors: string) {
      const wanted = selectors.split(",").map((s) => s.trim());
      return chain.find((own) => own.some((s) => wanted.includes(s))) ?? null;
    },
  };
}

const keys = [" ", "Enter", "1", "4", "a"];

test("слово текста забирает любые клавиши — и в старой разметке, и с role=button", () => {
  const plainWord = el(["[data-i]"], ["main"]);
  const buttonWord = el(["[data-i]", ".w[role=button]"], ["main"]);
  const wordWithoutIndex = el([".w[role=button]"], ["main"]);
  for (const target of [plainWord, buttonWord, wordWithoutIndex]) {
    for (const key of keys) assert.equal(ownsKey(target, key), true, `клавиша ${JSON.stringify(key)}`);
  }
});

test("поля ввода и попап слова забирают любые клавиши", () => {
  for (const own of ["input", "textarea", "select", "[contenteditable]", "[role=dialog]"]) {
    for (const key of keys) assert.equal(ownsKey(el([own]), key), true, `${own} ${JSON.stringify(key)}`);
  }
  assert.equal(ownsKey(el(["button"], ["[role=dialog]"]), "3"), true, "кнопка внутри попапа");
});

test("кнопка и ссылка забирают только пробел и Enter", () => {
  for (const own of ["button", "a"]) {
    assert.equal(ownsKey(el([own]), " "), true);
    assert.equal(ownsKey(el([own]), "Enter"), true);
    assert.equal(ownsKey(el([own]), "2"), false, `${own}: цифра — оценка страницы`);
  }
});

test("прочие цели и нажатие без цели — клавиши страницы", () => {
  for (const key of keys) {
    assert.equal(ownsKey(el(["main"], ["body"]), key), false);
    assert.equal(ownsKey(null, key), false);
  }
  assert.equal(ownsKey(el([".w"], ["main"]), " "), false, "класс .w без role=button — не кнопка-слово");
});

const session = ["[data-practice-session]"];

test("клавиши практики действуют только с фокусом в сессии (WCAG 2.1.4)", () => {
  // фокус на самой сессии (после старта, переворота, оценки, «Дальше»)
  for (const key of keys) assert.equal(isSessionKey(el(session, ["main"]), key), true, `сессия ${JSON.stringify(key)}`);
  // кнопка оценки и ссылка источника внутри сессии: цифра — клавиша практики, пробел и Enter — их
  assert.equal(isSessionKey(el(["button"], ["article"], session), "3"), true);
  assert.equal(isSessionKey(el(["button"], session), " "), false);
  assert.equal(isSessionKey(el(["a"], ["p"], session), "Enter"), false);
  // слово текста и поле ввода внутри сессии забирают всё
  assert.equal(isSessionKey(el(["[data-i]"], ["article"], session), "2"), false);
  assert.equal(isSessionKey(el(["input"], ["form"], session), "1"), false);
});

test("вне сессии клавиши практики не действуют: ссылка меню, тело страницы, попап слова", () => {
  for (const key of keys) {
    assert.equal(isSessionKey(el(["a"], ["nav"], ["body"]), key), false, `ссылка меню ${JSON.stringify(key)}`);
    assert.equal(isSessionKey(el(["body"]), key), false, `без фокуса ${JSON.stringify(key)}`);
    // попап слова — в портале в body, не внутри сессии
    assert.equal(isSessionKey(el(["h2"], ["[role=dialog]"], ["body"]), key), false, `попап ${JSON.stringify(key)}`);
    assert.equal(isSessionKey(null, key), false);
  }
});
