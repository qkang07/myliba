import { getLibraryOfflineIndex, type CloudFile, type LibrarySearchPage } from "./api";

type OfflineFile = CloudFile & { lifecycle: "active" | "trashed" };
type OfflineSnapshot = { generation: string; libraryId: string; nodeId: string; syncedAt: string; items: OfflineFile[] };
type StoredIndex = { id: string; salt: number[]; nonce: number[]; ciphertext: number[] };

let unlocked: OfflineSnapshot | null = null;

function database(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const open = indexedDB.open("myliba-library-offline", 1);
    open.onupgradeneeded = () => { if (!open.result.objectStoreNames.contains("indexes")) open.result.createObjectStore("indexes", { keyPath: "id" }); };
    open.onsuccess = () => resolve(open.result);
    open.onerror = () => reject(open.error);
  });
}

function storedID(nodeId: string, libraryId: string) { return `${location.origin}\n${nodeId}\n${libraryId}`; }

async function readStored(nodeId: string, libraryId: string): Promise<StoredIndex | undefined> {
  const db = await database();
  try { return await new Promise((resolve, reject) => { const request = db.transaction("indexes").objectStore("indexes").get(storedID(nodeId, libraryId)); request.onsuccess = () => resolve(request.result as StoredIndex | undefined); request.onerror = () => reject(request.error); }); }
  finally { db.close(); }
}

async function writeStored(record: StoredIndex) {
  const db = await database();
  try { await new Promise<void>((resolve, reject) => { const request = db.transaction("indexes", "readwrite").objectStore("indexes").put(record); request.onsuccess = () => resolve(); request.onerror = () => reject(request.error); }); }
  finally { db.close(); }
}

async function derive(passphrase: string, salt: Uint8Array) {
  const base = await crypto.subtle.importKey("raw", new TextEncoder().encode(passphrase), "PBKDF2", false, ["deriveKey"]);
  return crypto.subtle.deriveKey({ name: "PBKDF2", salt: Uint8Array.from(salt).buffer, iterations: 310000, hash: "SHA-256" }, base, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
}

export async function saveOfflineIndex(nodeId: string, passphrase: string): Promise<number> {
  if (!passphrase || !nodeId) throw new Error("请先连接查询节点并设置本地口令");
  let cursor = "", generation = "", libraryId = "", syncedAt = "";
  const items: OfflineFile[] = [];
  do {
    const page = await getLibraryOfflineIndex(cursor);
    if (page.nodeId !== nodeId || (generation && generation !== page.generation) || (libraryId && libraryId !== page.libraryId)) throw new Error("索引代次或查询节点已变化，请重试");
    generation = page.generation; libraryId = page.libraryId; syncedAt = page.syncedAt;
    items.push(...page.items);
    cursor = page.nextCursor;
    if (items.length > 1_000_000) throw new Error("离线索引过大");
  } while (cursor);
  const snapshot: OfflineSnapshot = { generation, libraryId, nodeId, syncedAt, items };
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const key = await derive(passphrase, salt);
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv: Uint8Array.from(nonce).buffer, additionalData: new TextEncoder().encode(storedID(nodeId, libraryId)) }, key, new TextEncoder().encode(JSON.stringify(snapshot))));
  await writeStored({ id: storedID(nodeId, libraryId), salt: Array.from(salt), nonce: Array.from(nonce), ciphertext: Array.from(ciphertext) });
  localStorage.setItem("myliba-offline-index-node", nodeId);
  localStorage.setItem("myliba-offline-index-library", libraryId);
  unlocked = snapshot;
  return items.length;
}

export async function unlockOfflineIndex(passphrase: string, nodeId = localStorage.getItem("myliba-offline-index-node") || "") {
  const libraryId = localStorage.getItem("myliba-offline-index-library") || "";
  const stored = nodeId && libraryId ? await readStored(nodeId, libraryId) : undefined;
  if (!stored) throw new Error("此查询节点没有已保存的离线索引");
  try {
    const key = await derive(passphrase, new Uint8Array(stored.salt));
    const raw = await crypto.subtle.decrypt({ name: "AES-GCM", iv: Uint8Array.from(stored.nonce).buffer, additionalData: new TextEncoder().encode(stored.id) }, key, Uint8Array.from(stored.ciphertext).buffer);
    const snapshot = JSON.parse(new TextDecoder().decode(raw)) as OfflineSnapshot;
    if (snapshot.nodeId !== nodeId || snapshot.libraryId !== libraryId || !Array.isArray(snapshot.items)) throw new Error("索引内容无效");
    unlocked = snapshot;
    return snapshot.items.length;
  } catch { throw new Error("本地口令错误或索引已损坏"); }
}

export function lockedOfflineIndex() { unlocked = null; }
export function offlineIndexStatus() { return unlocked ? { nodeId: unlocked.nodeId, libraryId: unlocked.libraryId, syncedAt: unlocked.syncedAt, count: unlocked.items.length } : null; }

export function searchOfflineIndex(input: { query: string; scope: string; lifecycle: "active" | "trashed"; folderId: string; tags: string[]; type?: string; grouped?: boolean; cloudCopy?: boolean; sort: string; cursor: string; limit: number }): LibrarySearchPage | null {
  const index = unlocked;
  if (!index) return null;
  const needle = input.query.trim().toLocaleLowerCase();
  const matches = index.items.filter(file => {
    if (file.lifecycle !== input.lifecycle || input.scope === "cloud" && file.localOnly) return false;
    if (input.scope.startsWith("device:") && !file.locations?.some(location => location.nodeId === input.scope.slice(7))) return false;
    if (input.scope === "local" && !file.locations?.some(location => location.nodeId === index.nodeId)) return false;
    if (input.type && fileType(file.name) !== input.type) return false;
    if (input.grouped !== undefined && Boolean(file.folderId) !== input.grouped) return false;
    if (input.cloudCopy !== undefined && !file.localOnly !== input.cloudCopy) return false;
    if (input.folderId && input.folderId !== file.folderId && !(input.folderId === "unfiled" && !file.folderId) && !(input.folderId.startsWith("name:") && file.folderName?.toLocaleLowerCase() === input.folderId.slice(5).toLocaleLowerCase())) return false;
    if (input.tags.some(tag => !file.tags?.some(current => current.toLocaleLowerCase() === tag.toLocaleLowerCase()))) return false;
    return !needle || [file.name, file.folderName || "", ...(file.tags || [])].some(value => value.toLocaleLowerCase().includes(needle));
  });
  matches.sort((a, b) => {
    let result = 0;
    switch (input.sort) {
      case "name_desc": result = b.name.localeCompare(a.name); break;
      case "modified_desc": result = b.modified.localeCompare(a.modified); break;
      case "modified_asc": result = a.modified.localeCompare(b.modified); break;
      case "ingested_desc": result = (b.ingestedAt || "").localeCompare(a.ingestedAt || ""); break;
      case "ingested_asc": result = (a.ingestedAt || "").localeCompare(b.ingestedAt || ""); break;
      default: result = a.name.localeCompare(b.name);
    }
    return result || (a.fileId || a.id).localeCompare(b.fileId || b.id);
  });
  const offset = input.cursor ? Number(input.cursor.split(":").at(-1)) : 0;
  if (input.cursor && !input.cursor.startsWith(`offline:${index.generation}:`) || !Number.isSafeInteger(offset) || offset < 0) return null;
  const end = Math.min(matches.length, offset + input.limit);
  const folders = new Map<string, { id: string; name: string; count: number }>();
  const tags = new Map<string, { name: string; count: number }>();
  for (const file of matches) {
    const id = file.folderId || "unfiled", name = file.folderName || "未归组", folder = folders.get(id) || { id, name, count: 0 }; folder.count++; folders.set(id, folder);
    for (const name of new Set(file.tags || [])) { const tag = tags.get(name) || { name, count: 0 }; tag.count++; tags.set(name, tag); }
  }
  return { items: matches.slice(offset, end), total: matches.length, nextCursor: end < matches.length ? `offline:${index.generation}:${end}` : "", generation: index.generation,
    coverage: { scope: input.scope, deviceIndexes: input.scope !== "cloud", generation: index.generation, lastCatalogAt: index.syncedAt, offlineSources: [], sources: [], offline: true, syncedAt: index.syncedAt } as LibrarySearchPage["coverage"],
    facets: { folders: Array.from(folders.values()), tags: Array.from(tags.values()) }, jobs: {}, nodeId: index.nodeId, libraryId: index.libraryId, peers: [], directEndpoint: "" };
}

function fileType(name: string) {
  const extension = name.toLowerCase().split(".").pop() || "";
  if (["jpg", "jpeg", "png", "gif", "webp", "heic", "arw", "dng"].includes(extension)) return "image";
  if (["mp4", "mov", "mkv", "avi", "webm"].includes(extension)) return "video";
  if (["mp3", "wav", "flac", "m4a", "ogg"].includes(extension)) return "audio";
  if (["pdf", "txt", "doc", "docx", "xls", "xlsx", "ppt", "pptx"].includes(extension)) return "document";
  return "other";
}
