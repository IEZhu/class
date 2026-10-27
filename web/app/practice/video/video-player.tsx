"use client";

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { PracticeVideo, VideoSegment } from "../../../lib/api";
import { activeAt, clock } from "../../../lib/timeline";
import ClickableText from "../../clickable-text";

// Плеер видео-практики (S5-5, ADR-038): ролик встраивается плеером
// YouTube в режиме повышенной конфиденциальности (youtube-nocookie.com) —
// видео идёт с YouTube в браузер, мимо VPS. Рядом своя панель субтитров:
// звучащая строка подсвечена, клик по времени или мимо слов мотает ролик,
// строки — через <ClickableText>, строки с целевыми словами отмечены.

// Минимум IFrame Player API, который нужен странице
// (developers.google.com/youtube/iframe_api_reference)
interface YTPlayer {
  seekTo(seconds: number, allowSeekAhead: boolean): void;
  getCurrentTime(): number;
  playVideo(): void;
  destroy(): void;
}

interface YTNamespace {
  Player: new (
    el: HTMLIFrameElement,
    opts: { events: { onReady: () => void; onError: (e: { data: number }) => void } },
  ) => YTPlayer;
}

declare global {
  interface Window {
    YT?: YTNamespace;
    onYouTubeIframeAPIReady?: () => void;
  }
}

const IFRAME_API = "https://www.youtube.com/iframe_api";
const EMBED_HOST = "https://www.youtube-nocookie.com";
// Как часто панель спрашивает у плеера время
const POLL_MS = 250;

let apiLoading: Promise<YTNamespace> | null = null;

// loadYouTubeApi — скрипт IFrame API один на вкладку, грузится только
// на этой странице; сбой загрузки можно повторить следующим монтированием.
function loadYouTubeApi(): Promise<YTNamespace> {
  if (window.YT?.Player) return Promise.resolve(window.YT);
  if (apiLoading) return apiLoading;
  apiLoading = new Promise<YTNamespace>((resolve, reject) => {
    const previous = window.onYouTubeIframeAPIReady;
    window.onYouTubeIframeAPIReady = () => {
      previous?.();
      if (window.YT) resolve(window.YT);
    };
    const script = document.createElement("script");
    script.src = IFRAME_API;
    script.async = true;
    script.onerror = () => {
      apiLoading = null;
      script.remove();
      reject(new Error("не загрузился YouTube IFrame API"));
    };
    document.head.appendChild(script);
  });
  return apiLoading;
}

// Коды onError: 101 и 150 — владелец запретил встраивание
const embedForbidden = new Set([101, 150]);

const mutedStyle = { color: "#666", margin: 0 } as const;

export default function VideoPlayer({ video, startMs }: { video: PracticeVideo; startMs: number }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const playerRef = useRef<YTPlayer | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [timeMs, setTimeMs] = useState(startMs);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // iframe создаётся здесь, а не в разметке React: src нужен с origin
  // страницы, а API плеера и его destroy() владеют элементом сами
  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const params = new URLSearchParams({
      enablejsapi: "1",
      origin: window.location.origin,
      playsinline: "1",
      rel: "0",
      start: String(Math.floor(startMs / 1000)),
    });
    const iframe = document.createElement("iframe");
    iframe.src = `${EMBED_HOST}/embed/${encodeURIComponent(video.youtube_id)}?${params}`;
    iframe.title = video.title;
    iframe.allow = "encrypted-media; picture-in-picture; fullscreen";
    // Без Referer плеер отвечает ошибкой 153
    iframe.referrerPolicy = "strict-origin-when-cross-origin";
    iframe.style.cssText = "position:absolute;inset:0;width:100%;height:100%;border:0";
    host.appendChild(iframe);

    let cancelled = false;
    let player: YTPlayer | null = null;
    loadYouTubeApi()
      .then((YT) => {
        if (cancelled) return;
        player = new YT.Player(iframe, {
          events: {
            onReady: () => {
              if (cancelled) return;
              playerRef.current = player;
              setReady(true);
            },
            onError: (e) =>
              setError(
                embedForbidden.has(e.data)
                  ? "Владелец ролика запретил встраивание — откройте его на YouTube."
                  : `Плеер YouTube не может показать ролик (код ${e.data}).`,
              ),
          },
        });
      })
      .catch((e: unknown) => {
        // Ролик в iframe играет и без API: нет только синхронизации панели
        if (!cancelled) setError(e instanceof Error ? `${e.message}: панель субтитров не следит за видео.` : String(e));
      });
    return () => {
      cancelled = true;
      playerRef.current = null;
      setReady(false);
      if (player) player.destroy();
      else iframe.remove();
    };
  }, [video.youtube_id, video.title, startMs]);

  useEffect(() => {
    if (!ready) return;
    const id = window.setInterval(() => {
      const p = playerRef.current;
      if (p) setTimeMs(Math.round(p.getCurrentTime() * 1000));
    }, POLL_MS);
    return () => window.clearInterval(id);
  }, [ready]);

  const seek = useCallback((ms: number) => {
    const p = playerRef.current;
    if (!p) return;
    p.seekTo(ms / 1000, true);
    p.playVideo();
    setTimeMs(ms);
  }, []);

  const segments = video.segments;
  const timeline = useMemo(() => [...segments].sort((a, b) => a.start_ms - b.start_ms), [segments]);
  const active = activeAt(timeline, timeMs);

  // Звучащая строка держится в видимой части списка; прокручивается
  // только список, не страница (как транскрипт урока, S2-4)
  useEffect(() => {
    const box = listRef.current;
    if (active === null || !box) return;
    const row = box.querySelector<HTMLElement>(`[data-idx="${active}"]`);
    if (!row) return;
    const top = row.offsetTop;
    if (top < box.scrollTop || top + row.offsetHeight > box.scrollTop + box.clientHeight) {
      box.scrollTo({ top: Math.max(0, top - box.clientHeight / 3), behavior: "smooth" });
    }
  }, [active]);

  return (
    <section style={{ display: "grid", gap: "0.75rem" }}>
      <div ref={hostRef} style={{ position: "relative", width: "100%", aspectRatio: "16 / 9", background: "#000" }} />
      <p style={{ ...mutedStyle, fontSize: "0.9rem" }}>
        «{video.title}»{video.channel && ` — ${video.channel}`}, видео и{" "}
        {video.subtitles === "manual" ? "субтитры автора" : "автоматические субтитры"} —{" "}
        <a href={video.url} target="_blank" rel="noopener noreferrer">
          YouTube
        </a>
      </p>
      {error && <p style={{ color: "crimson", margin: 0 }}>{error}</p>}
      {!ready && !error && <p style={mutedStyle}>Плеер загружается…</p>}
      <div
        ref={listRef}
        // position: relative — offsetTop строк отсчитывается от списка
        style={{ position: "relative", maxHeight: "50vh", overflowY: "auto", paddingRight: "0.25rem" }}
      >
        {segments.map((s) => (
          <SubtitleRow key={s.idx} segment={s} active={s.idx === active} onSeek={seek} />
        ))}
      </div>
    </section>
  );
}

// Строка мемоизирована: время плеера меняется четыре раза в секунду,
// а перерисовать нужно только две строки — погасшую и подсвеченную.
const SubtitleRow = memo(function SubtitleRow({
  segment: s,
  active,
  onSeek,
}: {
  segment: VideoSegment;
  active: boolean;
  onSeek: (ms: number) => void;
}) {
  const target = s.words.length > 0;
  return (
    <div
      data-idx={s.idx}
      style={{
        padding: "0.2rem 0.4rem",
        borderLeft: `3px solid ${active ? "#2a7" : target ? "#f0b400" : "transparent"}`,
        background: active ? "#e8f6ef" : "transparent",
        borderRadius: 4,
      }}
    >
      <button
        type="button"
        onClick={() => onSeek(s.start_ms)}
        title="Перейти к этому месту видео"
        style={{ border: "none", background: "none", padding: 0, color: "#888", cursor: "pointer", fontVariantNumeric: "tabular-nums" }}
      >
        {clock(s.start_ms)}
      </button>{" "}
      {/* Клик по слову — попап (ClickableText его не пропускает дальше), мимо слов — перемотка */}
      <span onClick={() => onSeek(s.start_ms)} style={{ cursor: "pointer" }}>
        <ClickableText text={s.text} />
      </span>
      {target && <span style={{ color: "#b07d00", fontSize: "0.8rem" }}> · {s.words.join(", ")}</span>}
    </div>
  );
});
