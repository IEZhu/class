import type { GeneratedText } from "../../../lib/api";
import ClickableText from "../../clickable-text";

// Сгенерированный текст абзацами и итог валидатора покрытия (S5-6).
// Абзацы — через <ClickableText> (S5-7): текст кликается, как материал.

const percent = new Intl.NumberFormat("ru-RU", { style: "percent", maximumFractionDigits: 0 });

const genreLabels: Record<string, string> = {
  story: "рассказ",
  dialogue: "диалог",
  article: "статья",
  letter: "письмо",
  description: "описание",
};

export function genreLabel(genre: string): string {
  return genreLabels[genre] ?? genre;
}

export const genres = Object.keys(genreLabels);

// GeneratedParagraphs — тело текста: абзацы через пустую строку, каждый
// через <ClickableText>. Общий рендер истории (/practice/text) и выдачи
// тексто-практики (/practice/texts).
export function GeneratedParagraphs({ body }: { body: string }) {
  return (
    <>
      {body.split(/\n{2,}/).map((p, i) => (
        <p key={i} style={{ lineHeight: 1.6 }}>
          <ClickableText text={p} />
        </p>
      ))}
    </>
  );
}

export default function GeneratedTextView({ text }: { text: GeneratedText }) {
  return (
    <article style={{ border: "1px solid #ddd", borderRadius: 8, padding: "0.75rem 1rem" }}>
      <p style={{ color: "#666", margin: 0 }}>
        {text.level} · {genreLabel(text.genre)} · {text.word_count} слов
      </p>
      <GeneratedParagraphs body={text.body} />
      <p style={{ color: "#666", fontSize: "0.9rem", margin: 0 }}>
        Покрытие слов: {percent.format(text.coverage)} (
        {text.target_lemmas.map((l) => `${l} ×${text.occurrences[l] ?? 0}`).join(", ")}) · вне словаря:{" "}
        {percent.format(text.unknown_share)} · {text.provider}/{text.model}
        {text.attempts > 1 && " · со второй попытки"}
      </p>
    </article>
  );
}
