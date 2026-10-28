import assert from "node:assert/strict";
import { test } from "node:test";

import { initials } from "./initials.ts";

test("инициалы — первые буквы двух первых слов, заглавными", () => {
  assert.equal(initials("Мария Соколова"), "МС");
  assert.equal(initials("алексей иванов"), "АИ");
  assert.equal(initials("Анна Мария Петрова"), "АМ");
  assert.equal(initials("Виктор"), "В");
});

test("лишние пробелы не дают пустых слов", () => {
  assert.equal(initials("  Игорь   Архипов  "), "ИА");
  assert.equal(initials("Гость\tПетров"), "ГП");
});

test("буква — целая кодовая точка, не половина суррогатной пары", () => {
  assert.equal(initials("𝒜lice Smith"), "𝒜S");
});

test("пустое имя — fallback: «?» по умолчанию или метка спикера", () => {
  assert.equal(initials(""), "?");
  assert.equal(initials("   "), "?");
  assert.equal(initials("", "C"), "C");
  assert.equal(initials("Мария", "C"), "М");
});
