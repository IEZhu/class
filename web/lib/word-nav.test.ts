import assert from "node:assert/strict";
import { test } from "node:test";

import { isRussianPart } from "./word-nav.ts";

test("кусок с кириллицей — русский, латиница и знаки — нет", () => {
  assert.equal(isRussianPart("неохотный, нежелающий"), true);
  assert.equal(isRussianPart(" — значит «"), true);
  assert.equal(isRussianPart("Ёж"), true);
  assert.equal(isRussianPart(", 42 — "), false);
  assert.equal(isRussianPart("reluctant"), false);
  assert.equal(isRussianPart(""), false);
});
