"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import { FormStatus, request, useSubmit } from "../../forms";
import { genreLabel, genres } from "./generated-text";

const fieldStyle = { width: "100%", padding: "0.5rem" };
const levels = ["A1", "A2", "B1", "B2", "C1", "C2"];

// Форма генерации: слова через пробел или запятую, уровень, жанр, длина.
// Пустые слова — api берёт невыученные слова личного словаря (ADR-030).
// Проверяет всё api (белые списки, одно слово из букв) и отвечает
// понятной ошибкой; здесь — только разбор строки слов. Готовый текст
// api сохраняет, и он появляется первым в истории страницы.
export default function GenerateForm() {
  const router = useRouter();
  const [words, setWords] = useState("");
  const [level, setLevel] = useState("B1");
  const [genre, setGenre] = useState("story");
  const [maxWords, setMaxWords] = useState(150);
  const { busy, error, done, submit } = useSubmit(async () => {
    await request("/api/practice/generate-text", "POST", {
      words: words.split(/[\s,;]+/).filter(Boolean),
      level,
      genre,
      max_words: maxWords,
    });
    router.refresh();
  });

  return (
    <form
      onSubmit={submit}
      style={{ border: "1px solid #ddd", borderRadius: 8, padding: "1rem", display: "grid", gap: "0.75rem" }}
    >
      <label>
        Слова (через пробел или запятую, до 20; пусто — из моего словаря)
        <textarea value={words} onChange={(e) => setWords(e.target.value)} rows={2} style={fieldStyle} />
      </label>
      <div style={{ display: "flex", gap: "0.75rem", flexWrap: "wrap" }}>
        <label>
          Уровень{" "}
          <select value={level} onChange={(e) => setLevel(e.target.value)}>
            {levels.map((l) => (
              <option key={l}>{l}</option>
            ))}
          </select>
        </label>
        <label>
          Жанр{" "}
          <select value={genre} onChange={(e) => setGenre(e.target.value)}>
            {genres.map((g) => (
              <option key={g} value={g}>
                {genreLabel(g)}
              </option>
            ))}
          </select>
        </label>
        <label>
          Слов в тексте{" "}
          <input
            type="number"
            min={50}
            max={400}
            step={10}
            value={maxWords}
            onChange={(e) => setMaxWords(Number(e.target.value))}
            style={{ width: "5rem" }}
          />
        </label>
      </div>
      <button type="submit" disabled={busy} style={{ padding: "0.6rem" }}>
        {busy ? "Генерируем… (до минуты)" : "Сгенерировать текст"}
      </button>
      <FormStatus error={error} done={done} doneText="Текст готов — он первым в списке ниже." />
    </form>
  );
}
