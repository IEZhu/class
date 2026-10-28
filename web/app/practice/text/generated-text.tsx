import type { GeneratedText } from "../../../lib/api";
import ClickableText from "../../clickable-text";
import { cx } from "../../ui/cx";
import t from "./text.module.css";

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
// через <ClickableText>, кеглем для чтения. Общий рендер истории
// (/practice/text) и выдачи тексто-практики (/practice/texts).
export function GeneratedParagraphs({ body }: { body: string }) {
  return (
    <div className="reading">
      {body.split(/\n{2,}/).map((p, i) => (
        <p key={i}>
          <ClickableText text={p} />
        </p>
      ))}
    </div>
  );
}

// Итог проверки покрытия и модель — в «Подробностях»: студенту они нужны
// редко, текст важнее (DESIGN §6.8)
export default function GeneratedTextView({ text }: { text: GeneratedText }) {
  return (
    <article className={cx("card card-pad", t.item)}>
      <p className="small muted">
        {text.level} · {genreLabel(text.genre)} · {text.word_count} слов
      </p>
      <GeneratedParagraphs body={text.body} />
      <details className={cx("disclosure", t.details)}>
        <summary>Подробности</summary>
        <p className="small muted">
          Покрытие слов: {percent.format(text.coverage)} (
          {text.target_lemmas.map((l) => `${l} ×${text.occurrences[l] ?? 0}`).join(", ")}) · вне словаря:{" "}
          {percent.format(text.unknown_share)} · {text.provider}/{text.model}
          {text.attempts > 1 && " · со второй попытки"}
        </p>
      </details>
    </article>
  );
}
