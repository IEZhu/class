import type { LessonTerm } from "../../../lib/api";
import ClickableText from "../../clickable-text";

// Словарь урока для его участников (S3-8): слова, которые преподаватель
// подтвердил после урока, — через <ClickableText>, с переводом из кэша.
export default function LessonDictionary({ items }: { items: LessonTerm[] }) {
  if (items.length === 0) return null;
  return (
    <section style={{ margin: "1.5rem 0" }}>
      <h2>Словарь урока</h2>
      <ul style={{ paddingLeft: "1.25rem" }}>
        {items.map((t) => (
          <li key={t.term_id}>
            <strong>
              <ClickableText text={t.lemma} />
            </strong>
            {t.translation && <span style={{ color: "#666" }}> — {t.translation}</span>}
          </li>
        ))}
      </ul>
    </section>
  );
}
