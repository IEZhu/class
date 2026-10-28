import type { ReactNode } from "react";

import { buttonClass } from "../ui/button";
import type { ButtonVariant } from "../ui/button";
import { Icon } from "../ui/icons";
import styles from "./words-search.module.css";

// WordsSearch — поиск по словам тексто- и видео-практики (DESIGN §6.8,
// §6.9): GET-форма страницы action, слова — в адресе (?words=). Подпись
// «Слова» над полем, кнопка — в строке поля, подсказка и ошибка api —
// под ним и в доступном описании поля (aria-describedby).
export default function WordsSearch({
  action,
  words,
  hint,
  error,
  variant,
  children,
}: {
  action: string;
  words: string;
  hint: string;
  // error — причина отказа api (400): неверные слова или их слишком много
  error: string | null;
  variant: ButtonVariant;
  // children — надпись кнопки
  children: ReactNode;
}) {
  return (
    <form method="get" action={action} className={styles.search}>
      <label className="label" htmlFor="words">
        Слова
      </label>
      <div className={styles.row}>
        <input
          id="words"
          className="input"
          name="words"
          defaultValue={words}
          aria-describedby={error ? "words-hint words-error" : "words-hint"}
          aria-invalid={error ? true : undefined}
        />
        <button type="submit" className={buttonClass({ variant })}>
          {children}
        </button>
      </div>
      <p id="words-hint" className="hint">
        {hint}
      </p>
      {error && (
        <p id="words-error" className="field-error">
          <Icon name="error" size="sm" />
          <span>{error}</span>
        </p>
      )}
    </form>
  );
}
