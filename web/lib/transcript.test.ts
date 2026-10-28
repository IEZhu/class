import assert from "node:assert/strict";
import { test } from "node:test";

import type { Transcript, Utterance } from "./api";
import { moreReplicas, replicas, replicasOf, withLocalChanges } from "./transcript.ts";
import type { LocalChanges } from "./transcript.ts";

test("число реплик: 1 реплика, 2 реплики, 5 реплик, 11–14 — реплик", () => {
  assert.deepEqual([1, 2, 4, 5, 11, 12, 14, 21, 22, 25, 101, 111].map(replicas), [
    "1 реплика",
    "2 реплики",
    "4 реплики",
    "5 реплик",
    "11 реплик",
    "12 реплик",
    "14 реплик",
    "21 реплика",
    "22 реплики",
    "25 реплик",
    "101 реплика",
    "111 реплик",
  ]);
});

test("начало транскрипта: «40 из N реплик» и «ещё N реплик»", () => {
  assert.equal(replicasOf(40, 70), "40 из 70 реплик");
  assert.equal(replicasOf(40, 41), "40 из 41 реплики");
  assert.equal(replicasOf(40, 42), "40 из 42 реплик");
  assert.equal(replicasOf(40, 111), "40 из 111 реплик");
  assert.equal(moreReplicas(1), "ещё 1 реплику");
  assert.equal(moreReplicas(2), "ещё 2 реплики");
  assert.equal(moreReplicas(30), "ещё 30 реплик");
  assert.equal(moreReplicas(651), "ещё 651 реплику");
  assert.equal(moreReplicas(12), "ещё 12 реплик");
});

function utt(idx: number, label: string, text: string): Utterance {
  return { idx, speaker_label: label, user_id: null, speaker_name: null, start_ms: idx * 1000, end_ms: idx * 1000 + 900, text, edited_at: null };
}

function transcript(...utterances: Utterance[]): Transcript {
  return { status: "completed", utterances, vtt_url: "https://s3/new.vtt", vtt_expires_at: null };
}

function changes(): LocalChanges {
  return { utterances: new Map(), speakers: new Map() };
}

test("перечитанный транскрипт без правок — тот же объект", () => {
  const t = transcript(utt(0, "A", "Hi"));
  assert.equal(withLocalChanges(t, changes()), t);
});

test("правка текста и метка, сохранённые до ответа перечитывания, не пропадают", () => {
  // перечитывание ушло из api до правок: старый текст и метка без человека
  const fetched = transcript(utt(0, "A", "Hello"), utt(1, "B", "Good morning"), utt(2, "A", "How are you"), utt(45, "B", "Bye"));
  const local = changes();
  local.utterances.set(1, { ...utt(1, "B", "Good morning, everyone"), edited_at: "2026-10-28T10:00:00Z" });
  local.speakers.set("B", { speaker_label: "B", user_id: 7, speaker_name: "Анна" });
  const got = withLocalChanges(fetched, local);
  assert.equal(got.vtt_url, "https://s3/new.vtt", "остальное — из перечитанного");
  assert.equal(got.utterances.length, 4, "все реплики, и дальше начала");
  assert.equal(got.utterances[0], fetched.utterances[0], "чужие реплики не трогаются");
  assert.equal(got.utterances[1].text, "Good morning, everyone");
  assert.equal(got.utterances[1].edited_at, "2026-10-28T10:00:00Z");
  assert.equal(got.utterances[1].user_id, 7, "метка поверх правки текста, сохранённой до неё");
  assert.deepEqual([got.utterances[3].user_id, got.utterances[3].speaker_name], [7, "Анна"], "метка — у всех её реплик");
  assert.equal(got.utterances[2].user_id, null);
  assert.equal(fetched.utterances[1].text, "Good morning", "перечитанный не меняется");
});

test("снятое сопоставление метки (user_id null) тоже переживает перечитывание", () => {
  const fetched = transcript({ ...utt(0, "A", "Hi"), user_id: 3, speaker_name: "Иван" });
  const local = changes();
  local.speakers.set("A", { speaker_label: "A", user_id: null, speaker_name: null });
  assert.deepEqual(withLocalChanges(fetched, local).utterances[0], utt(0, "A", "Hi"));
});
