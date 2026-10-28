import assert from "node:assert/strict";
import { test } from "node:test";

import { placePopover } from "./popover-place.ts";

// Окно 900px: наибольшая высота попапа — min(70vh, 560px) = 560
const viewport = 900;
const cap = 560;

test("места снизу хватает — попап под словом", () => {
  assert.deepEqual(placePopover({ top: 100, bottom: 120 }, 522, cap, viewport), { top: 124, maxHeight: 560, above: false });
});

test("слово внизу окна — попап над словом, низом у слова", () => {
  // снизу 168px, сверху 688px
  assert.deepEqual(placePopover({ top: 700, bottom: 720 }, 522, cap, viewport), { top: 174, maxHeight: 560, above: true });
  // пока данные слова грузятся, попап ниже — и всё равно над словом
  assert.deepEqual(placePopover({ top: 700, bottom: 720 }, 150, cap, viewport), { top: 546, maxHeight: 560, above: true });
});

test("не помещается ни снизу, ни сверху — где места больше, высота по месту", () => {
  // окно 700px: cap 490; снизу 338px, сверху 318px
  assert.deepEqual(placePopover({ top: 330, bottom: 350 }, 450, 490, 700), { top: 354, maxHeight: 338, above: false });
  // снизу 268px, сверху 388px: над словом, верх попапа — у края окна
  assert.deepEqual(placePopover({ top: 400, bottom: 420 }, 450, 490, 700), { top: 8, maxHeight: 388, above: true });
});

test("попап не выходит за верх окна и не получает отрицательной высоты", () => {
  const p = placePopover({ top: 860, bottom: 880 }, 900, cap, viewport);
  assert.equal(p.above, true);
  assert.ok(p.top >= 8, `верх ${p.top}`);
  const q = placePopover({ top: 950, bottom: 970 }, 100, cap, viewport);
  assert.ok(q.maxHeight >= 0);
});
