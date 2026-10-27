// Статусы слов зрителя для раскраски <ClickableText> (S3-5, ADR-026).
// Чистый TS без React и fetch: запрос к api передаётся снаружи, поэтому
// кэш проверяют тесты lib/term-status.test.ts (npm test).

// Статус слова в личном словаре: 1 — новое … 5 — знаю (flows/word-click.md)
export type TermStatus = 1 | 2 | 3 | 4 | 5;

export const TERM_STATUSES: readonly TermStatus[] = [1, 2, 3, 4, 5];

export const STATUS_LABELS: Record<TermStatus, string> = {
  1: "новое",
  2: "узнаю",
  3: "учу",
  4: "почти знаю",
  5: "знаю",
};

// Ответ POST /user-terms/lookup: lemmas — лемма слова, только если она
// отличается от него; statuses — статус по лемме, только слова из словаря.
export interface LookupResult {
  lemmas: Record<string, string>;
  statuses: Record<string, number>;
}

export type LookupFn = (words: string[]) => Promise<LookupResult>;

// LOOKUP_CHUNK — maxLookupWords api: больше слов за запрос он не примет
export const LOOKUP_CHUNK = 2000;

function isStatus(n: number): n is TermStatus {
  return Number.isInteger(n) && n >= 1 && n <= 5;
}

// TermStatuses — кэш статусов на вкладку браузера. Все тексты страницы
// просят свои слова, кэш собирает их за один такт в общую пачку и
// спрашивает api только о новых словах; сохранение статуса в попапе
// перекрашивает все формы леммы без запроса.
//
// Кэш — словарь одного студента: каждый вызов называет его id (student).
// Выход и вход — клиентская навигация, модуль её переживает, и без этого
// следующий в той же вкладке видел бы раскраску чужого словаря. Другой
// студент сбрасывает кэш, ответы, спрошенные до сброса, отбрасываются.
export class TermStatuses {
  // Чей словарь в кэше; null — ещё ничей
  private student: number | null = null;
  // Номер сброса: ответ lookup с другим номером — о прежнем студенте
  private epoch = 0;
  private readonly lemmas = new Map<string, string>();
  private readonly statuses = new Map<string, TermStatus>();
  // Слова, о которых уже спросили или спрашивают
  private readonly requested = new Set<string>();
  private queue: string[] = [];
  private flushing: Promise<void> | null = null;
  private readonly listeners = new Set<() => void>();
  private version = 0;
  private readonly lookup: LookupFn;
  private readonly chunk: number;

  // Без параметров-свойств: тесты запускает node со снятием типов, а оно
  // понимает только стираемый синтаксис TypeScript
  constructor(lookup: LookupFn, chunk = LOOKUP_CHUNK) {
    this.lookup = lookup;
    this.chunk = chunk;
  }

  // request ставит в очередь слова, о которых ещё не спрашивали. Эффекты
  // всех текстов одного рендера отрабатывают синхронно подряд, поэтому
  // отправка после микрозадачи собирает их в одну пачку.
  request(student: number, keys: Iterable<string>): Promise<void> {
    this.useStudent(student);
    for (const key of keys) {
      if (this.requested.has(key)) continue;
      this.requested.add(key);
      this.queue.push(key);
    }
    if (!this.flushing && this.queue.length > 0) {
      this.flushing = Promise.resolve().then(() => this.flush());
    }
    return this.flushing ?? Promise.resolve();
  }

  private async flush(): Promise<void> {
    while (this.queue.length > 0) {
      const epoch = this.epoch;
      const batch = this.queue.splice(0, this.chunk);
      try {
        const res = await this.lookup(batch);
        if (epoch !== this.epoch) continue;
        for (const [word, lemma] of Object.entries(res.lemmas)) this.lemmas.set(word, lemma);
        for (const [lemma, status] of Object.entries(res.statuses)) {
          if (isStatus(status)) this.statuses.set(lemma, status);
        }
        this.changed();
      } catch {
        // api или сеть недоступны: текст остаётся без раскраски, клик
        // работает; слова забываем, чтобы следующий текст спросил их снова
        if (epoch === this.epoch) for (const word of batch) this.requested.delete(word);
      }
    }
    this.flushing = null;
  }

  // statusOf — статус слова по его ключу (wordKey) в словаре студента;
  // undefined — не в словаре или кэш ещё чужой
  statusOf(student: number, key: string): TermStatus | undefined {
    if (student !== this.student) return undefined;
    return this.statuses.get(this.lemmas.get(key) ?? key);
  }

  // learn запоминает лемму слова из ответа клика: слово перекрашивается
  // вместе с остальными формами, даже если lookup до него не дошёл
  learn(key: string, lemma: string): void {
    if (key === lemma || this.lemmas.get(key) === lemma) return;
    this.lemmas.set(key, lemma);
    this.changed();
  }

  // setStatus — статус леммы в словаре студента после клика или
  // сохранения; null — слова нет в словаре
  setStatus(student: number, lemma: string, status: TermStatus | null): void {
    this.useStudent(student);
    if ((this.statuses.get(lemma) ?? null) === status) return;
    if (status === null) this.statuses.delete(lemma);
    else this.statuses.set(lemma, status);
    this.changed();
  }

  // subscribe и getVersion — для useSyncExternalStore
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getVersion = (): number => this.version;

  // useStudent делает кэш кэшем студента student: чужой сбрасывается,
  // а тексты, ещё раскрашенные по нему, перерисовываются
  private useStudent(student: number): void {
    if (student === this.student) return;
    const previous = this.student;
    this.student = student;
    this.epoch++;
    this.lemmas.clear();
    this.statuses.clear();
    this.requested.clear();
    this.queue = [];
    if (previous !== null) this.changed();
  }

  private changed(): void {
    this.version++;
    for (const listener of this.listeners) listener();
  }
}
