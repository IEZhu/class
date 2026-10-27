import ClickableText from "../../clickable-text";

// UtteranceText — единственное место, где рендерится текст реплики
// транскрипта (S2-4): через общий <ClickableText> (S3-1), с уроком из
// ClickableTextScope страницы урока (layout.tsx). Клик по слову открывает
// попап, клик мимо слов и по времени — перемотку (ADR-025, ADR-026).
export default function UtteranceText({ text }: { text: string }) {
  return <ClickableText text={text} />;
}
