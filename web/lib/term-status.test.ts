import assert from "node:assert/strict";
import { test } from "node:test";
import { setImmediate as tick } from "node:timers/promises";

import { TermStatuses } from "./term-status.ts";
import type { LookupResult } from "./term-status.ts";

// A, B — id студентов, по очереди вошедших в одну вкладку
const A = 1;
const B = 2;

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
  void store.request(A, ["running", "the"]);
  void store.request(A, ["ran", "the", "house"]);
  await store.request(A, ["running"]);
  assert.deepEqual(api.calls, [["running", "the", "ran", "house"]]);
  assert.equal(store.statusOf(A, "running"), 2);
  assert.equal(store.statusOf(A, "ran"), 2);
  assert.equal(store.statusOf(A, "house"), 5);
  assert.equal(store.statusOf(A, "the"), undefined);

  await store.request(A, ["the", "house", "tree"]);
  assert.deepEqual(api.calls[1], ["tree"]);
});

test("пачки режутся по потолку api", async () => {
  const api = fakeLookup({}, {});
  const store = new TermStatuses(api.lookup, 3);
  await store.request(A, ["a", "b", "c", "d", "e", "f", "g"]);
  assert.deepEqual(api.calls, [["a", "b", "c"], ["d", "e", "f"], ["g"]]);
});

test("смена статуса перекрашивает все формы леммы и будит подписчиков", async () => {
  const api = fakeLookup({ running: "run", ran: "run" }, {});
  const store = new TermStatuses(api.lookup);
  let notified = 0;
  const unsubscribe = store.subscribe(() => notified++);
  await store.request(A, ["running", "ran", "run"]);
  assert.equal(notified, 1);
  const before = store.getVersion();

  store.setStatus(A, "run", 1);
  assert.deepEqual(["running", "ran", "run"].map((w) => store.statusOf(A, w)), [1, 1, 1]);
  store.setStatus(A, "run", 1); // тот же статус — без перерисовки
  assert.equal(store.getVersion(), before + 1);
  store.setStatus(A, "run", null);
  assert.equal(store.statusOf(A, "ran"), undefined);
  assert.equal(notified, 3);

  // Лемма из клика: слово, до которого lookup не дошёл
  store.learn("runs", "run");
  store.setStatus(A, "run", 4);
  assert.equal(store.statusOf(A, "runs"), 4);
  unsubscribe();
  store.setStatus(A, "run", 5);
  assert.equal(notified, 5);
});

test("сбой api: без раскраски, слова спросятся снова", async () => {
  const api = fakeLookup({}, { run: 3 });
  const store = new TermStatuses(api.lookup);
  api.setFail(true);
  await store.request(A, ["run"]);
  assert.equal(store.statusOf(A, "run"), undefined);
  api.setFail(false);
  await store.request(A, ["run"]);
  assert.equal(store.statusOf(A, "run"), 3);
  assert.equal(api.calls.length, 2);
});

test("статус вне 1..5 из ответа не принимается", async () => {
  const store = new TermStatuses(async () => ({ lemmas: {}, statuses: { run: 7, walk: 2 } }));
  await store.request(A, ["run", "walk"]);
  assert.equal(store.statusOf(A, "run"), undefined);
  assert.equal(store.statusOf(A, "walk"), 2);
});

test("другой студент в той же вкладке не видит раскраску прежнего", async () => {
  // api отвечает о словаре того, чья сессия сейчас в cookie
  const dictionaries: Record<number, Record<string, number>> = { [A]: { run: 1 }, [B]: { run: 4 } };
  let session = A;
  const calls: string[] = [];
  const store = new TermStatuses(async (words) => {
    calls.push(`${session}:${words.join(",")}`);
    const own = dictionaries[session];
    return { lemmas: {}, statuses: Object.fromEntries(words.filter((w) => own[w]).map((w) => [w, own[w]])) };
  });
  let notified = 0;
  store.subscribe(() => notified++);

  await store.request(A, ["run"]);
  assert.equal(store.statusOf(A, "run"), 1);

  // Выход и вход B — без перезагрузки: до его запроса цвета A ему не видны
  session = B;
  assert.equal(store.statusOf(B, "run"), undefined);
  const before = notified;
  await store.request(B, ["run"]);
  assert.ok(notified > before, "сброс перерисовывает тексты, раскрашенные по A");
  assert.deepEqual(calls, [`${A}:run`, `${B}:run`]);
  assert.equal(store.statusOf(B, "run"), 4);
  assert.equal(store.statusOf(A, "run"), undefined);

  // Сохранение статуса другим студентом тоже сбрасывает чужой кэш
  store.setStatus(A, "walk", 2);
  assert.equal(store.statusOf(A, "run"), undefined);
  assert.equal(store.statusOf(B, "run"), undefined);
});

test("ответ lookup, спрошенный для прежнего студента, отбрасывается", async () => {
  const answers: ((statuses: Record<string, number>) => void)[] = [];
  const store = new TermStatuses(
    () => new Promise<LookupResult>((resolve) => answers.push((statuses) => resolve({ lemmas: {}, statuses }))),
  );
  const done = store.request(A, ["run"]);
  await tick();
  assert.equal(answers.length, 1);

  // A вышел, B вошёл, пока ответ о словаре A был в пути
  void store.request(B, ["run"]);
  answers[0]({ run: 1 });
  await tick();
  assert.equal(store.statusOf(B, "run"), undefined, "статус из словаря A не принят");
  assert.equal(answers.length, 2, "слова B спрошены заново");
  answers[1]({ run: 4 });
  await done;
  assert.equal(store.statusOf(B, "run"), 4);
});
