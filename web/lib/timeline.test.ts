import assert from "node:assert/strict";
import { test } from "node:test";

import { activeAt, clock } from "./timeline.ts";

test("звучит последняя начавшаяся строка, до первой — ничего", () => {
  const timeline = [
    { idx: 4, start_ms: 0 },
    { idx: 7, start_ms: 5000 },
    { idx: 2, start_ms: 9000 },
  ];
  assert.equal(activeAt(timeline, -1), null);
  assert.equal(activeAt(timeline, 0), 4);
  assert.equal(activeAt(timeline, 4999), 4);
  assert.equal(activeAt(timeline, 5000), 7);
  assert.equal(activeAt(timeline, 60000), 2);
  assert.equal(activeAt([], 1000), null);
});

test("метка времени: минуты, а с часа — часы", () => {
  assert.equal(clock(0), "0:00");
  assert.equal(clock(65_999), "1:05");
  assert.equal(clock(3_723_000), "1:02:03");
});
