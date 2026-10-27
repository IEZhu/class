import assert from "node:assert/strict";
import { test } from "node:test";

import { delayLabel, keyAction, RATINGS, reviewId } from "./review-session.ts";

test("пробел и Enter переворачивают, цифры оценивают только оборот", () => {
  assert.deepEqual(keyAction(" ", false), { kind: "flip" });
  assert.deepEqual(keyAction("Enter", false), { kind: "flip" });
  assert.equal(keyAction("3", false), null, "оценка без оборота");
  assert.equal(keyAction(" ", true), null, "оборот не переворачивается обратно");
  assert.deepEqual(
    ["1", "2", "3", "4"].map((k) => keyAction(k, true)),
    RATINGS.map((o) => ({ kind: "rate", rating: o.rating })),
  );
  assert.deepEqual(RATINGS.map((o) => o.rating), ["again", "hard", "good", "easy"]);
  assert.equal(keyAction("5", true), null);
  assert.equal(keyAction("a", true), null);
});

test("review_id — uuid v4 в каноническом виде из данных байт", () => {
  const zero = reviewId(new Uint8Array(16));
  assert.equal(zero, "00000000-0000-4000-8000-000000000000");
  const ff = reviewId(new Uint8Array(16).fill(0xff));
  assert.equal(ff, "ffffffff-ffff-4fff-bfff-ffffffffffff");
  const bytes = Uint8Array.from({ length: 16 }, (_, i) => i * 17);
  const id = reviewId(bytes);
  assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  assert.equal(bytes[6], 6 * 17, "входные байты не меняются");
  assert.throws(() => reviewId(new Uint8Array(15)));
});

test("подпись интервала: минуты, часы, дни", () => {
  assert.equal(delayLabel(20_000), "меньше чем через минуту");
  assert.equal(delayLabel(60_000), "через 1 мин");
  assert.equal(delayLabel(10 * 60_000), "через 10 мин");
  assert.equal(delayLabel(5 * 3_600_000), "через 5 ч");
  assert.equal(delayLabel(3 * 86_400_000 + 3_600_000), "через 3 дн.");
});
