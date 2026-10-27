// Серверный клиент к api: страницы (Server Components) ходят на api напрямую
// внутри compose-сети (INTERNAL_API_URL), пробрасывая cookie запроса.
// Клиентские компоненты используют относительный /api (через caddy).
import { cookies } from "next/headers";

import type { Role } from "./roles";
import type { TermStatus } from "./term-status";

const API_BASE = process.env.INTERNAL_API_URL ?? "http://api:8080";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

export interface User {
  id: number;
  email: string;
  // admin заводит людей и тасует группы, teacher владеет уроками (ADR-007)
  role: Role;
  name: string;
}

// Строка списка людей в админ-кабинете: учётка + названия групп.
export interface UserWithGroups extends User {
  groups: string[];
  // null у действующей учётки, момент архивации — у архивной (ADR-024)
  deactivated_at: string | null;
}

export interface Group {
  id: number;
  name: string;
  level: string;
  // archived — учётка в архиве: из группы не убрана, в новые уроки не попадает
  members: { user_id: number; email: string; name: string; role: Role; archived: boolean }[];
}

// Приглашение (ADR-008). url приходит только в ответе на создание —
// в БД лежит лишь хэш токена, показать ссылку повторно нельзя.
export interface Invite {
  id: number;
  email: string;
  name: string;
  role: Role;
  group_name: string;
  inviter_name: string;
  expires_at: string;
  url?: string;
}

export interface Lesson {
  id: number;
  // null у открытого события — группы у него нет (ADR-010)
  group_id: number | null;
  // Название события; у обычного урока пусто, подпись берётся из группы
  title: string;
  teacher_id: number;
  starts_at: string;
  ends_at: string;
  status: "scheduled" | "live" | "processing" | "done";
  // в GET /lessons и GET /lessons/{id} заполнен всегда (NOT NULL join)
  group_name: string;
}

// Файл материала в хранилище (ADR-024). ready=false — загрузка не
// подтверждена; такой файл api показывает только преподавателю урока.
export interface MaterialFile {
  name: string;
  content_type: string;
  size: number;
  ready: boolean;
}

export interface Material {
  id: number;
  lesson_id: number;
  kind: "material" | "homework";
  title: string;
  body_md: string;
  created_at: string;
  // null у текстового материала
  file: MaterialFile | null;
}

export interface LessonDetail extends Lesson {
  teacher_name: string;
  materials: Material[];
  // archived — учётка в архиве (ADR-024): в снапшоте урока остаётся
  participants: { user_id: number; email: string; name: string; role: string; archived: boolean }[];
  // Только организатору события: выпущена ли гостевая ссылка (сама она
  // не восстанавливается — в БД лишь хэш). Остальным поле не приходит.
  has_guest_link?: boolean;
}

// Реплика транскрипта (S2-2…S2-5); время — миллисекунды от начала записи.
export interface Utterance {
  idx: number;
  // метка спикера от ASR: A, B, C…
  speaker_label: string;
  // null, пока преподаватель не сопоставил метку с человеком урока (S2-5)
  user_id: number | null;
  speaker_name: string | null;
  start_ms: number;
  end_ms: number;
  text: string;
  // null — текст как его распознал ASR; иначе время правки (S2-4)
  edited_at: string | null;
}

// GET /lessons/{id}/transcript. queued | submitted | fetching — готовится,
// error — транскрипта не будет (ADR-016); vtt_url — presigned на 30 мин.
export interface Transcript {
  status: "queued" | "submitted" | "fetching" | "completed" | "error";
  utterances: Utterance[];
  vtt_url: string | null;
  vtt_expires_at: string | null;
}

// Google Calendar преподавателя (S1-1, ADR-023): available — ключи Google
// на стенде заданы, connected — токен сохранён и читается текущим ключом.
export interface GoogleCalendarStatus {
  available: boolean;
  connected: boolean;
}

// Ответ DELETE /auth/google: revoked=false — токен у нас стёрт, но в Google
// не отозван, доступ преподаватель снимает в аккаунте Google сам.
export interface GoogleDisconnectResult {
  revoked: boolean;
}

// Ответ POST /terms/click (S3-2) — содержимое попапа <ClickableText>.
export interface TermClick {
  // 0 — слово api не знает и термина не заводил: попап без «в мой словарь»
  term_id: number;
  lemma: string;
  surface: string;
  // null — перевода нет в кэше, а LibreTranslate не настроен или не ответил
  translation: { text: string; source: string } | null;
  definitions: { word: string; pos: string; glosses: string[]; translations_ru: string[] }[];
  // До двух примеров Tatoeba с переводом (S5-4, ADR-028); корпус
  // не импортирован или слово незнакомо — пустой список
  examples: TermExample[];
  // Статус в личном словаре студента (S3-5); null — слова там нет
  status: TermStatus | null;
}

// Пример Tatoeba в ответе клика. CC BY 2.0 FR разрешает показ, только
// назвав автора: у предложения и у перевода — автор и страница на
// tatoeba.org; пустой автор — у предложения нет владельца.
export interface TermExample {
  text: string;
  translation: string;
  source: "tatoeba";
  attribution: {
    author: string;
    url: string;
    translation_author: string;
    translation_url: string;
    license: string;
    license_url: string;
  };
}

// Слово личного словаря (GET/POST /user-terms, S3-5, ADR-027).
export interface UserTerm {
  term_id: number;
  lemma: string;
  surface: string;
  translation: string | null;
  status: TermStatus;
  // Урок, на котором слово добавили; null — вне урока или урок отменён
  source_lesson: (Pick<Lesson, "id" | "title" | "group_id" | "group_name" | "starts_at">) | null;
  added_at: string;
  updated_at: string;
}

export interface UserTermPage {
  items: UserTerm[];
  // null — страница последняя
  next_cursor: string | null;
}

// Текст, сгенерированный LLM по целевым словам (S5-6, ADR-029/030).
// body — абзацы через пустую строку; coverage — доля целевых лемм,
// встреченных не реже двух раз; unknown_share — доля слов вне словаря.
export interface GeneratedText {
  id: number;
  level: string;
  genre: string;
  max_words: number;
  target_lemmas: string[];
  body: string;
  word_count: number;
  coverage: number;
  occurrences: Record<string, number>;
  unknown_share: number;
  attempts: number;
  provider: string;
  model: string;
  created_at: string;
}

// Кандидат словаря урока (S3-7, ADR-033): click — клики участников,
// whiteboard — вхождения на доске при последнем разборе, manual — ввод
// преподавателя. Один термин может прийти из нескольких источников.
export type CandidateSource = "click" | "whiteboard" | "manual";

export interface TermCandidate {
  term_id: number;
  lemma: string;
  // форма, в которой слово встретилось первым
  surface: string;
  translation: string | null;
  source: CandidateSource;
  count: number;
  first_seen_at: string;
  last_seen_at: string;
  // термин уже в словаре урока
  confirmed: boolean;
}

// Ответ POST /lessons/{id}/term-candidates: rejected — не одно слово
// (not_a_word) или слово, которого api не знает (unknown).
export interface ManualCandidatesResult {
  added: { term_id: number; lemma: string; surface: string }[];
  rejected: { word: string; reason: "not_a_word" | "unknown" }[];
}

// Слово подтверждённого словаря урока (S3-8, ADR-034).
export interface LessonTerm {
  term_id: number;
  lemma: string;
  surface: string;
  translation: string | null;
  confirmed_at: string;
}

// Итог назначения словаря урока: added — слов легло в словари впервые,
// kept — у студента уже было, статус не тронут.
export interface AssignResult {
  students: number;
  terms: number;
  added: number;
  kept: number;
}

const API_TIMEOUT_MS = 5000;

export async function apiFetch<T>(path: string): Promise<T> {
  const cookieStore = await cookies();
  // во внутренний api уходит только сессионный cookie (ADR-006),
  // остальные cookie запроса ему не нужны
  const sid = cookieStore.get("sid");
  const cookieHeader = sid ? `sid=${sid.value}` : "";

  // таймаут: зависший api не должен держать рендер страницы бесконечно
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), API_TIMEOUT_MS);
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      headers: cookieHeader ? { cookie: cookieHeader } : undefined,
      cache: "no-store",
      signal: controller.signal,
    });
  } finally {
    clearTimeout(timeoutId);
  }
  if (!res.ok) {
    let message = `api: ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // тело не JSON — оставляем статусное сообщение
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}
