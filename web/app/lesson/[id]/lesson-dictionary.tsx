import type { LessonTerm } from "../../../lib/api";
import { STATUS_LABELS } from "../../../lib/term-status";
import ClickableText from "../../clickable-text";
import { cx } from "../../ui/cx";
import styles from "./page.module.css";

// Образцы подсветки слов в тексте урока: статусы 1–4 (5 «знаю» не
// подсвечивается). Классы — те же, что у слов <ClickableText> (DESIGN §3.10)
const legend = [
  { className: "w1", label: STATUS_LABELS[1] },
  { className: "w2", label: STATUS_LABELS[2] },
  { className: "w3", label: STATUS_LABELS[3] },
  { className: "w4", label: STATUS_LABELS[4] },
];

// Словарь урока для его участников (S3-8): слова, которые преподаватель
// подтвердил после урока, — через <ClickableText>, с переводом из кэша.
// legend — подпись к подсветке слов: она есть только у студента.
export default function LessonDictionary({ items, legend: withLegend }: { items: LessonTerm[]; legend: boolean }) {
  if (items.length === 0) return null;
  return (
    <section id="terms" className={cx("card section", styles.flush)} aria-labelledby="terms-title">
      <div className="card-head">
        <h2 id="terms-title">Словарь урока</h2>
      </div>
      <ul className={styles.termGrid}>
        {items.map((t) => (
          <li key={t.term_id}>
            <span className={styles.lemma}>
              <ClickableText text={t.lemma} />
            </span>
            {t.translation && (
              <span className="muted">
                <span className="sr-only"> — </span>
                {t.translation}
              </span>
            )}
          </li>
        ))}
      </ul>
      {withLegend && (
        <p className={cx("legend", styles.legend)}>
          <span className="sr-only">Подсветка слов в текстах урока по статусу в вашем словаре:</span>
          {legend.map((l) => (
            <span key={l.className}>
              <span className={cx("w", l.className)} aria-hidden="true">
                word
              </span>{" "}
              {l.label}
            </span>
          ))}
        </p>
      )}
    </section>
  );
}
