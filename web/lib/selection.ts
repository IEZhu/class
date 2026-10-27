// Клик, которым мышь закончила выделение текста, — не жест: попап слова
// он не открывает, и строка вокруг текста (перемотка реплики транскрипта,
// S2-4) его не получает — человек выделял фразу, чтобы скопировать.
// Чистая функция над Selection: её проверяет npm test.
export function isTextSelection(selection: Pick<Selection, "isCollapsed" | "toString"> | null): boolean {
  return selection !== null && !selection.isCollapsed && selection.toString() !== "";
}
