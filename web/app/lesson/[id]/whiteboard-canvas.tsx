"use client";

import "@excalidraw/excalidraw/index.css";

import {
  CaptureUpdateAction,
  Excalidraw,
  isInvisiblySmallElement,
  reconcileElements,
  restoreElements,
} from "@excalidraw/excalidraw";
import type { RemoteExcalidrawElement } from "@excalidraw/excalidraw/data/reconcile";
import type { OrderedExcalidrawElement } from "@excalidraw/excalidraw/element/types";
import type { AppState, Collaborator, ExcalidrawImperativeAPI, SocketId } from "@excalidraw/excalidraw/types";
import { useEffect, useState } from "react";
import { io } from "socket.io-client";
import type { Socket } from "socket.io-client";

import { requestJson } from "../../forms";
import { decryptMessage, encryptMessage, importRoomKey, MAX_RELAY_BYTES } from "./whiteboard-sync";

declare global {
  interface Window {
    EXCALIDRAW_ASSET_PATH?: string | string[];
  }
}

// Шрифты — со своего домена, их кладёт в public web/scripts/excalidraw-assets.mjs.
// Модуль грузится только в браузере (next/dynamic с ssr: false в whiteboard.tsx).
window.EXCALIDRAW_ASSET_PATH = "/excalidraw/";

type Session = { room: string; key: string; socket_path: string };
type SceneFile = { elements: OrderedExcalidrawElement[] };
type Pointer = NonNullable<Collaborator["pointer"]>;
type Message =
  | { type: "SCENE_INIT" | "SCENE_UPDATE"; payload: { elements: readonly OrderedExcalidrawElement[] } }
  | {
      type: "MOUSE_LOCATION";
      payload: {
        socketId: string;
        pointer: Pointer;
        button: "down" | "up";
        selectedElementIds: AppState["selectedElementIds"];
        username: string;
      };
    };
type Status = { text: string; error?: boolean };

// Свои изменения уходят пачкой раз в 60 мс; раз в 20 с — вся сцена, чтобы
// догнать потерянное (так же делает excalidraw.com). Сохранение в api —
// через 2 с тишины после своего изменения.
const BROADCAST_THROTTLE_MS = 60;
const FULL_SYNC_MS = 20_000;
const SAVE_DEBOUNCE_MS = 2_000;
const SAVE_RETRY_MS = 10_000;
const POINTER_THROTTLE_MS = 50;
// Новичку отвечаем всей сценой (SCENE_INIT), но не на каждый new-user:
// ретранслятор шлёт его на любой join-room, в том числе повторный от того
// же сокета. Одному сокету — не чаще раза в 10 с; новичкам, пришедшим
// разом, — одна рассылка через 1 с. Сохранённую сцену новичок уже взял
// из api, ответ лишь досылает несохранённое.
const SCENE_INIT_PER_PEER_MS = 10_000;
const SCENE_INIT_DELAY_MS = 1_000;
// keepalive-запрос при уходе со страницы браузер ограничивает 64 КБ
const KEEPALIVE_MAX_BYTES = 60_000;

// Невидимо маленькие элементы (случайный клик) не рассылаем и не храним —
// как excalidraw.com. Удалённые — рассылаем: удаление тоже версия элемента.
function syncable(elements: readonly OrderedExcalidrawElement[]): OrderedExcalidrawElement[] {
  return elements.filter((e) => !isInvisiblySmallElement(e));
}

// Синхронизация одной доски: ретранслятор, сохранение в api, курсоры.
class BoardSync {
  private socket: Socket;
  // id → версия, которую участники уже знают (разослали мы или прислали они)
  private known = new Map<string, number>();
  // id → версия удалённого элемента, которая уже есть в api: надгробие
  // уходит в сохранение один раз, иначе очищенная доска весила бы в каждом
  // сохранении, как нарисованная (ADR-020)
  private savedDeleted = new Map<string, number>();
  private collaborators = new Map<SocketId, Collaborator>();
  private peers = 1;
  private broadcastTimer: ReturnType<typeof setTimeout> | null = null;
  private saveTimer: ReturnType<typeof setTimeout> | null = null;
  private initTimer: ReturnType<typeof setTimeout> | null = null;
  // Сокет новичка → когда ему последний раз ответили всей сценой
  private initSentAt = new Map<string, number>();
  private fullSync: ReturnType<typeof setInterval>;
  private lastPointerAt = 0;
  private dirty = false;
  // Обычные (не keepalive) сохранения в полёте: уход со страницы их оборвёт
  private inFlight = 0;
  // Срок сохранения пришёл, пока предыдущее в полёте: следующее уйдёт сразу за ним
  private again = false;
  private stopped = false;

  constructor(
    private api: ExcalidrawImperativeAPI,
    private key: CryptoKey,
    private session: Session,
    private lessonId: number,
    private userName: string,
    initial: readonly OrderedExcalidrawElement[],
    private report: (status: Status, peers: number) => void,
  ) {
    for (const e of initial) {
      this.known.set(e.id, e.version);
      if (e.isDeleted) this.savedDeleted.set(e.id, e.version);
    }
    // Тот же origin: cookie сессии уходит и caddy пускает к ретранслятору
    // только участников урока (forward_auth в api)
    this.socket = io({ path: session.socket_path });
    this.socket.on("init-room", () => {
      this.socket.emit("join-room", session.room);
      // После переподключения догоняем остальных тем, что нарисовали без связи
      void this.broadcastAll("SCENE_UPDATE", true);
    });
    this.socket.on("new-user", (peer: string) => this.onNewUser(peer));
    this.socket.on("room-user-change", (ids: string[]) => this.onPeers(ids));
    this.socket.on("client-broadcast", (data: ArrayBuffer, iv: ArrayBuffer) => void this.onMessage(data, iv));
    this.socket.on("connect", () => this.status({ text: "на связи" }));
    this.socket.on("disconnect", () => this.status({ text: "нет связи с доской, переподключаемся…", error: true }));
    this.socket.on("connect_error", () => this.status({ text: "нет связи с доской, переподключаемся…", error: true }));
    this.fullSync = setInterval(() => void this.broadcastAll("SCENE_UPDATE", false), FULL_SYNC_MS);
    // Закрытие вкладки, перезагрузка, уход в фон на телефоне: очистка
    // эффекта React тогда не выполняется — сохраняемся по событиям страницы
    window.addEventListener("pagehide", this.onPageHide);
    document.addEventListener("visibilitychange", this.onVisibilityChange);
  }

  stop() {
    this.stopped = true;
    clearInterval(this.fullSync);
    if (this.broadcastTimer) clearTimeout(this.broadcastTimer);
    if (this.initTimer) clearTimeout(this.initTimer);
    window.removeEventListener("pagehide", this.onPageHide);
    document.removeEventListener("visibilitychange", this.onVisibilityChange);
    this.leave();
    this.socket.disconnect();
  }

  private onPageHide = () => this.leave();

  private onVisibilityChange = () => {
    if (document.visibilityState === "hidden") this.leave();
  };

  // Уход со страницы или с доски: несохранённое уходит сразу запросом
  // keepalive — его браузер доводит и после закрытия вкладки. Обычное
  // сохранение в полёте выгрузка оборвёт, поэтому его сцену шлём ещё раз
  // вместе со всем, что нарисовано после.
  private leave() {
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = null;
    if (this.inFlight > 0) this.dirty = true;
    void this.save(true);
  }

  // onChange Excalidraw: зовётся и на движение мыши, поэтому сначала —
  // есть ли вообще элементы новее известных участникам
  onLocalChange(elements: readonly OrderedExcalidrawElement[]) {
    if (!syncable(elements).some((e) => e.version > (this.known.get(e.id) ?? -1))) return;
    if (!this.broadcastTimer) {
      this.broadcastTimer = setTimeout(() => {
        this.broadcastTimer = null;
        void this.broadcastChanged();
      }, BROADCAST_THROTTLE_MS);
    }
    this.dirty = true;
    this.scheduleSave(SAVE_DEBOUNCE_MS);
  }

  onPointer(pointer: Pointer, button: "down" | "up") {
    const now = Date.now();
    if (!this.socket.connected || this.peers < 2 || now - this.lastPointerAt < POINTER_THROTTLE_MS) return;
    this.lastPointerAt = now;
    void this.send(
      {
        type: "MOUSE_LOCATION",
        payload: {
          socketId: this.socket.id ?? "",
          pointer,
          button,
          selectedElementIds: this.api.getAppState().selectedElementIds,
          username: this.userName,
        },
      },
      true,
    );
  }

  private status(status: Status) {
    if (!this.stopped) this.report(status, this.peers);
  }

  private elements() {
    return syncable(this.api.getSceneElementsIncludingDeleted());
  }

  private async broadcastChanged() {
    const changed = this.elements().filter((e) => e.version > (this.known.get(e.id) ?? -1));
    if (changed.length === 0) return;
    for (const e of changed) this.known.set(e.id, e.version);
    await this.send({ type: "SCENE_UPDATE", payload: { elements: changed } });
  }

  // Вся сцена: новичку (SCENE_INIT), после переподключения и для периодической
  // сверки. Сверка не нужна, пока на доске никого больше нет.
  private async broadcastAll(type: "SCENE_INIT" | "SCENE_UPDATE", force: boolean) {
    if (!force && this.peers < 2) return;
    const elements = this.elements();
    for (const e of elements) this.known.set(e.id, e.version);
    await this.send({ type, payload: { elements } });
  }

  private async send(message: Message, volatile = false) {
    const { data, iv } = await encryptMessage(this.key, message);
    if (data.byteLength > MAX_RELAY_BYTES) {
      // Ретранслятор оборвал бы соединение; новички всё равно берут сцену из api
      this.status({ text: "сцена слишком большая для живой передачи — остальные увидят её после сохранения", error: true });
      return;
    }
    this.socket.emit(volatile ? "server-volatile-broadcast" : "server-broadcast", this.session.room, data, iv);
  }

  private async onMessage(data: ArrayBuffer, iv: ArrayBuffer) {
    const message = (await decryptMessage(this.key, data, iv)) as Message | null;
    if (!message || this.stopped) return;
    switch (message.type) {
      case "SCENE_INIT":
      case "SCENE_UPDATE":
        this.applyRemote(message.payload.elements);
        break;
      case "MOUSE_LOCATION": {
        const { socketId, pointer, button, selectedElementIds, username } = message.payload;
        const id = socketId as SocketId;
        this.collaborators.set(id, { ...this.collaborators.get(id), pointer, button, selectedElementIds, username });
        this.api.updateScene({ collaborators: new Map(this.collaborators) });
        break;
      }
    }
  }

  // Слияние по правилу Excalidraw: остаётся бо́льшая версия элемента
  private applyRemote(elements: readonly OrderedExcalidrawElement[]) {
    const remote = restoreElements(elements, null) as RemoteExcalidrawElement[];
    const reconciled = reconcileElements(this.api.getSceneElementsIncludingDeleted(), remote, this.api.getAppState());
    this.api.updateScene({ elements: reconciled, captureUpdate: CaptureUpdateAction.NEVER });
    // Известным считаем только то, что пришло: свой неразосланный элемент
    // новее присланного и должен уйти следующей пачкой
    const received = new Map(remote.map((e) => [e.id, e.version]));
    for (const e of reconciled) {
      if (received.get(e.id) === e.version) this.known.set(e.id, Math.max(e.version, this.known.get(e.id) ?? -1));
    }
  }

  private onNewUser(peer: string) {
    const now = Date.now();
    if (now - (this.initSentAt.get(peer) ?? -Infinity) < SCENE_INIT_PER_PEER_MS) return;
    this.initSentAt.set(peer, now);
    // Рассылка уже собирается — она достанется и этому новичку
    if (this.initTimer) return;
    this.initTimer = setTimeout(() => {
      this.initTimer = null;
      void this.broadcastAll("SCENE_INIT", true);
    }, SCENE_INIT_DELAY_MS);
  }

  private onPeers(ids: string[]) {
    this.peers = ids.length;
    for (const id of this.collaborators.keys()) {
      if (!ids.includes(id)) this.collaborators.delete(id);
    }
    for (const id of this.initSentAt.keys()) {
      if (!ids.includes(id)) this.initSentAt.delete(id);
    }
    this.api.updateScene({ collaborators: new Map(this.collaborators) });
    this.status({ text: "на связи" });
  }

  private scheduleSave(delay: number) {
    if (this.saveTimer) clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => {
      this.saveTimer = null;
      void this.save(false);
    }, delay);
  }

  // Сцену сохраняет каждый, кто рисовал; api сливает сохранения поэлементно
  private async save(leaving: boolean) {
    if (!this.dirty) return;
    if (this.inFlight > 0 && !leaving) {
      this.again = true;
      return;
    }
    this.dirty = false;
    // api сливает поэлементно и хранит то, чего нет в присланном, поэтому
    // уже сохранённое надгробие слать незачем
    const elements = this.elements().filter((e) => !e.isDeleted || this.savedDeleted.get(e.id) !== e.version);
    const body = JSON.stringify({ elements });
    // При уходе со страницы обычный запрос браузер оборвёт, а keepalive
    // ограничен 64 КБ: сцена больше уходит обычным и может не дойти
    const keepalive = leaving && new TextEncoder().encode(body).byteLength < KEEPALIVE_MAX_BYTES;
    if (!keepalive) this.inFlight++;
    try {
      const res = await fetch(`/api/lessons/${this.lessonId}/whiteboard/scene`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body,
        keepalive,
      });
      if (!res.ok) {
        const parsed = (await res.json().catch(() => null)) as { error?: string } | null;
        // 4xx (урок завершён, сцена велика) повтором не лечится — ждём
        // следующего изменения; 5xx — пробуем ещё раз
        if (res.status >= 500) this.retrySave();
        this.status({ text: `не сохранено: ${parsed?.error ?? `ошибка ${res.status}`}`, error: true });
        return;
      }
      for (const e of elements) {
        if (e.isDeleted) this.savedDeleted.set(e.id, e.version);
      }
      this.status({ text: "сохранено" });
    } catch {
      this.retrySave();
      this.status({ text: "не сохранено: сеть недоступна, повторим", error: true });
    } finally {
      if (!keepalive) {
        this.inFlight--;
        if (this.inFlight === 0 && this.again && !this.stopped) {
          this.again = false;
          void this.save(false);
        }
      }
    }
  }

  private retrySave() {
    this.dirty = true;
    if (!this.stopped) this.scheduleSave(SAVE_RETRY_MS);
  }
}

export default function WhiteboardCanvas({ lessonId, userName }: { lessonId: number; userName: string }) {
  const [initial, setInitial] = useState<{ session: Session; elements: OrderedExcalidrawElement[] } | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [api, setApi] = useState<ExcalidrawImperativeAPI | null>(null);
  const [sync, setSync] = useState<BoardSync | null>(null);
  const [status, setStatus] = useState<Status>({ text: "подключаемся…" });
  const [peers, setPeers] = useState(1);

  // Сначала вход (он же заводит доску), потом сцена
  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const session = await requestJson<Session>(`/api/lessons/${lessonId}/whiteboard`, "GET");
        const scene = await requestJson<SceneFile>(`/api/lessons/${lessonId}/whiteboard/scene`, "GET");
        if (!cancelled) setInitial({ session, elements: scene.elements });
      } catch (e) {
        if (!cancelled) setFailure(e instanceof Error ? e.message : "доска недоступна");
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [lessonId]);

  useEffect(() => {
    if (!api || !initial) return;
    let board: BoardSync | null = null;
    let cancelled = false;
    importRoomKey(initial.session.key)
      .then((key) => {
        if (cancelled) return;
        board = new BoardSync(api, key, initial.session, lessonId, userName, initial.elements, (s, n) => {
          setStatus(s);
          setPeers(n);
        });
        setSync(board);
      })
      .catch(() => setFailure("браузер не дал шифрование: доска работает только по https"));
    return () => {
      cancelled = true;
      board?.stop();
      setSync(null);
    };
  }, [api, initial, lessonId, userName]);

  if (failure) return <p style={{ color: "crimson", margin: 0 }}>Доска: {failure}</p>;
  if (!initial) return <p style={{ color: "#666", margin: 0 }}>Открываем доску…</p>;

  return (
    <div>
      <p style={{ margin: "0 0 0.5rem", fontSize: "0.9rem", color: status.error ? "crimson" : "#666" }} role="status">
        Доска: {status.text} · на доске {peers}
      </p>
      <div style={{ height: "70vh", minHeight: 400, border: "1px solid #ddd", borderRadius: 8, overflow: "hidden" }}>
        <Excalidraw
          excalidrawAPI={setApi}
          initialData={{ elements: initial.elements, scrollToContent: true }}
          isCollaborating
          langCode="ru-RU"
          onChange={(elements) => sync?.onLocalChange(elements)}
          onPointerUpdate={({ pointer, button }) => sync?.onPointer(pointer, button)}
          // Картинки не поддержаны: их файлы не ходят ни через ретранслятор,
          // ни в api (ADR-020). «Открыть» подменил бы сцену локально, мимо
          // слияния, — общая доска так не работает
          UIOptions={{ tools: { image: false }, canvasActions: { loadScene: false } }}
        />
      </div>
    </div>
  );
}
