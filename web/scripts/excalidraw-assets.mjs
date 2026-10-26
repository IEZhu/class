// Шрифты доски Excalidraw (S3-6, ADR-020) отдаём со своего домена, а не
// с esm.sh: копия из пакета в public/excalidraw/ перед dev и build (prebuild,
// predev), путь задаёт window.EXCALIDRAW_ASSET_PATH в whiteboard-canvas.tsx.
// Xiaolai (иероглифы, 12 МБ) не копируем: его Excalidraw возьмёт с esm.sh,
// только если на доске появится китайский текст.
import { cpSync, rmSync } from "node:fs";
import { fileURLToPath } from "node:url";

const from = fileURLToPath(new URL("../node_modules/@excalidraw/excalidraw/dist/prod/fonts", import.meta.url));
const to = fileURLToPath(new URL("../public/excalidraw/fonts", import.meta.url));

rmSync(to, { recursive: true, force: true });
cpSync(from, to, { recursive: true, filter: (path) => !path.includes("Xiaolai") });
