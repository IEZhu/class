// Обмен доски через ретранслятор excalidraw-room (S3-6, ADR-020): тот
// пересылает сообщение всем в комнате, не глядя внутрь, поэтому каждое
// шифруется AES-GCM ключом комнаты. Ключ api выдаёт только участникам
// урока: чужой, зашедший в комнату, не прочтёт сцену и не подсунет свою —
// подделку отбросит проверка тега GCM. Формат сообщений — как у
// excalidraw.com (excalidraw-app/collab): {type, payload}.

// Предел сообщения socket.io у ретранслятора (maxHttpBufferSize по
// умолчанию — 1 МБ): больше он не примет и оборвёт соединение отправителя.
export const MAX_RELAY_BYTES = 1_000_000 - 4_096;

const IV_BYTES = 12;

// Ключ — JWK "k" (base64url, 128 бит), как его отдаёт GET /lessons/{id}/whiteboard
export function importRoomKey(k: string): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "jwk",
    { alg: "A128GCM", ext: true, k, key_ops: ["encrypt", "decrypt"], kty: "oct" },
    { name: "AES-GCM", length: 128 },
    false,
    ["encrypt", "decrypt"],
  );
}

export async function encryptMessage(key: CryptoKey, message: unknown): Promise<{ data: ArrayBuffer; iv: Uint8Array<ArrayBuffer> }> {
  const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
  const plain = new TextEncoder().encode(JSON.stringify(message));
  return { data: await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, plain), iv };
}

// null — сообщение не нашим ключом или испорчено: его просто игнорируем
export async function decryptMessage(key: CryptoKey, data: ArrayBuffer, iv: ArrayBuffer | Uint8Array): Promise<unknown> {
  try {
    const plain = await crypto.subtle.decrypt({ name: "AES-GCM", iv: new Uint8Array(iv) }, key, data);
    return JSON.parse(new TextDecoder().decode(plain));
  } catch {
    return null;
  }
}
