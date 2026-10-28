// Язык частей <ClickableText> (DESIGN §5.1). Чистые функции без DOM и
// React: их проверяют тесты lib/word-nav.test.ts (npm test).

const CYRILLIC = /\p{Script=Cyrillic}/u;

// isRussianPart — кусок между словами с кириллицей. Корень текста помечен
// lang="en" (WCAG 3.1.2), но через <ClickableText> идут и русские тексты:
// переводы на обороте карточки, объяснение «объясни». Такие куски
// получают свой lang="ru", чтобы экранный диктор не читал их английским
// голосом. Латинские слова кириллицы не содержат — им проверка не нужна.
export function isRussianPart(text: string): boolean {
  return CYRILLIC.test(text);
}
