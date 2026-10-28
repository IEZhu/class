// Инициалы для аватара (DESIGN §3.23): первые буквы двух первых слов имени,
// заглавными. Буква — кодовая точка (Array.from), а не половина суррогатной
// пары. Имя без букв — fallback: «?» у аватара человека, метка ASR («C»)
// у спикера транскрипта.
export function initials(name: string, fallback = "?"): string {
  const letters = name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((w) => Array.from(w)[0] ?? "")
    .join("");
  return letters.toUpperCase() || fallback;
}
