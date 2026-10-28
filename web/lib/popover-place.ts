// Место попапа слова от 768px (DESIGN §4.2): под словом; если снизу попап
// целиком не помещается, а сверху помещается — над словом; если не
// помещается ни там, ни там — со стороны, где места больше, и не выше
// этого места (дальше попап прокручивается сам). На телефоне попап —
// лист снизу, место ему не нужно. Чистый TS без DOM — его проверяют
// тесты lib/popover-place.test.ts (npm test).

// Зазор между словом и попапом и наименьший отступ попапа от края окна, px
export const POPUP_GAP = 4;
export const VIEWPORT_MARGIN = 8;

export interface PopoverPlace {
  // Верх попапа от верха окна, px
  top: number;
  // Наибольшая высота попапа, px
  maxHeight: number;
  above: boolean;
}

// placePopover — место попапа у слова word (верх и низ слова от верха
// окна высотой viewport). height — высота попапа сейчас, cap — наибольшая
// по CSS (min(70vh, 560px)). Сторону выбирает cap, а не height: попап
// растёт, когда приходят данные слова, и не перескакивает со стороны на
// сторону; над словом он держится низом у слова.
export function placePopover(word: { top: number; bottom: number }, height: number, cap: number, viewport: number): PopoverPlace {
  const below = viewport - word.bottom - POPUP_GAP - VIEWPORT_MARGIN;
  const above = word.top - POPUP_GAP - VIEWPORT_MARGIN;
  if (below >= cap || below >= above) {
    return { top: word.bottom + POPUP_GAP, maxHeight: Math.max(0, Math.min(cap, below)), above: false };
  }
  const maxHeight = Math.min(cap, above);
  return { top: word.top - POPUP_GAP - Math.min(height, maxHeight), maxHeight, above: true };
}
