import assert from "node:assert/strict";
import { test } from "node:test";

import { TermStatuses } from "./term-status.ts";
import type { LookupResult } from "./term-status.ts";

// fakeLookup — api с леммами и статусами: отвечает только о спрошенном
function fakeLookup(lemmas: Record<string, string>, statuses: Record<string, number>) {
  const calls: string[][] = [];
  let fail = false;
  const lookup = async (words: string[]): Promise<LookupResult> => {
    calls.push(words);
    if (fail) throw new Error("api: 503");
    const res: LookupResult = { lemmas: {}, statuses: {} };
    for (const w of words) {
      const lemma = lemmas[w] ?? w;
      if (lemma !== w) res.lemmas[w] = lemma;
      if (statuses[lemma] !== undefined) res.statuses[lemma] = statuses[lemma];
    }
    return res;
  };
  return { lookup, calls, setFail: (v: boolean) => (fail = v) };
}

test("слова всех текстов одного такта уходят одной пачкой, повторы — никогда", async () => {
  const api = fakeLookup({ running: "run", ran: "run" }, { run: 2, house: 5 });
  const store = new TermStatuses(api.lookup);
  void store.request(["running", "the"]);
  void store.request(["ran", "the", "house"]);
  await store.request(["running"]);
  assert.deepEqual(api.calls, [["running", "the", "ran", "house"]]);
  assert.equal(store.statusOf("running"), 2);
  assert.equal(store.statusOf("ran"), 2);
  assert.equal(store.statusOf("house"), 5);
  assert.equal(store.statusOf("the"), undefined);

  await store.request(["the", "house", "tree"]);
  assert.deepEqual(api.calls[1], ["tree"]);
});

test("пачки режутся по потолку api", async () => {
  const api = fakeLookup({}, {});
  const store = new TermStatuses(api.lookup, 3);
  await store.request(["a", "b", "c", "d", "e", "f", "g"]);
  assert.deepEqual(api.calls, [["a", "b", "c"], ["d", "e", "f"], ["g"]]);
});

test("смена статуса перекрашивает все формы леммы и будит подписчиков", async () => {
  const api = fakeLookup({ running: "run", ran: "run" }, {});
  const store = new TermStatuses(api.lookup);
  let notified = 0;
  const unsubscribe = store.subscribe(() => notified++);
  await store.request(["running", "ran", "run"]);
  assert.equal(notified, 1);
  const before = store.getVersion();

  store.setStatus("run", 1);
  assert.deepEqual(["running", "ran", "run"].map((w) => store.statusOf(w)), [1, 1, 1]);
  store.setStatus("run", 1); // тот же статус — без перерисовки
  assert.equal(store.getVersion(), before + 1);
  store.setStatus("run", null);
  assert.equal(store.statusOf("ran"), undefined);
  assert.equal(notified, 3);

  // Лемма из клика: слово, до которого lookup не дошёл
  store.learn("runs", "run");
  store.setStatus("run", 4);
  assert.equal(store.statusOf("runs"), 4);
  unsubscribe();
  store.setStatus("run", 5);
  assert.equal(notified, 5);
});

test("сбой api: без раскраски, слова спросятся снова", async () => {
  const api = fakeLookup({}, { run: 3 });
  const store = new TermStatuses(api.lookup);
  api.setFail(true);
  await store.request(["run"]);
  assert.equal(store.statusOf("run"), undefined);
  api.setFail(false);
  await store.request(["run"]);
  assert.equal(store.statusOf("run"), 3);
  assert.equal(api.calls.length, 2);
});

test("статус вне 1..5 из ответа не принимается", async () => {
  const store = new TermStatuses(async () => ({ lemmas: {}, statuses: { run: 7, walk: 2 } }));
  await store.request(["run", "walk"]);
  assert.equal(store.statusOf("run"), undefined);
  assert.equal(store.statusOf("walk"), 2);
});
