import assert from "node:assert/strict";
import { test } from "node:test";

import { emailParts } from "./email-parts.ts";

test("адрес режется после «@» и точек, склеенный — тот же адрес", () => {
  const long = "konstantin.konstantinopolsky.long@example-university.edu";
  assert.deepEqual(emailParts(long), ["konstantin.", "konstantinopolsky.", "long@", "example-university.", "edu"]);
  assert.equal(emailParts(long).join(""), long);
  assert.deepEqual(emailParts("viktor@example.com"), ["viktor@", "example.", "com"]);
});

test("без точек и «@», пустой и с разделителями подряд — без потерь", () => {
  assert.deepEqual(emailParts("plain"), ["plain"]);
  assert.deepEqual(emailParts(""), [""]);
  assert.deepEqual(emailParts("a..b@@c"), ["a.", ".", "b@", "@", "c"]);
  assert.deepEqual(emailParts("@x."), ["@", "x."]);
});
