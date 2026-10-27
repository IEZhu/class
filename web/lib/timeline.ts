// Время в плеерах с текстовой дорожкой: транскрипт урока (S2-4) и
// субтитры видео-практики (S5-5). Время — миллисекунды от начала записи.

// Строка дорожки: номер и начало
export interface Cue {
  idx: number;
  start_ms: number;
}

// activeAt — строка, звучащая в момент ms: последняя начавшаяся. В паузе
// между строками подсвечена прошедшая — глаз не теряет место. timeline
// упорядочен по start_ms.
export function activeAt(timeline: readonly Cue[], ms: number): number | null {
  let lo = 0;
  let hi = timeline.length - 1;
  let found: number | null = null;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (timeline[mid].start_ms <= ms) {
      found = timeline[mid].idx;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return found;
}

// clock — метка времени для списка: 4:05, а с часа — 1:02:03.
export function clock(ms: number): string {
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}
