// cx — склейка классов: пропускает пустые и ложные части.
export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}
