// Спрайт иконок Plyr (S1-6) отдаём со своего домена, а не с cdn.plyr.io:
// копия из пакета в public/plyr/ перед dev и build (prebuild, predev), путь
// задаёт iconUrl в lesson-player.tsx. Копия при сборке, а не файл в git:
// спрайт всегда той же версии, что и установленный Plyr (ADR-032).
import { copyFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const from = fileURLToPath(new URL("../node_modules/plyr/dist/plyr.svg", import.meta.url));
const dir = fileURLToPath(new URL("../public/plyr", import.meta.url));

mkdirSync(dir, { recursive: true });
copyFileSync(from, `${dir}/plyr.svg`);
