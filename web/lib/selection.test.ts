import assert from "node:assert/strict";
import { test } from "node:test";

import { isTextSelection } from "./selection.ts";

function selection(text: string) {
  return { isCollapsed: text === "", toString: () => text };
}

test("выделение текста — не клик: ни попапа, ни перемотки", () => {
  assert.equal(isTextSelection(selection("ran away")), true);
  assert.equal(isTextSelection(selection(" ")), true);
});

test("обычный клик по слову или мимо слов — жест", () => {
  assert.equal(isTextSelection(null), false);
  assert.equal(isTextSelection(selection("")), false);
});
