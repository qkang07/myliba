export interface Resource {
  id: string;
  name: string;
  path: string;
  size: number;
  modified: string;
  kind: string;
  available: boolean;
  versionId: string;
  tags: string[];
}

export interface ResourcePage {
  items: Resource[];
  total: number;
  limit: number;
  offset: number;
}

export interface RootSummary {
  id: string;
  path: string;
  addedAt: string;
  lastScanAt: string;
  count: number;
}

export interface TagSummary {
  id: string;
  name: string;
  count: number;
}

export interface TagSuggestion {
  name: string;
  reason: string;
}

export interface BatchTagSuggestion {
  name: string;
  reason: string;
  count: number;
  examples: string[];
  ids: string[];
}

export interface DirectoryItem {
  name: string;
  path: string;
}

export interface DirectoryListing {
  current: string;
  parent: string;
  roots: DirectoryItem[];
  directories: DirectoryItem[];
}

export interface SessionInfo {
  local?: boolean;
  authenticated: boolean;
  nodeOnline: boolean;
}

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, {
    credentials: "same-origin",
    ...init,
    headers: {
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });
  const body = response.status === 204
    ? undefined
    : await response.json().catch(() => undefined);

  if (response.status === 401) {
    window.dispatchEvent(new CustomEvent("myliba:unauthorized"));
  }
  if (!response.ok) {
    throw new ApiError(response.status, body?.error || `请求失败 (${response.status})`);
  }
  return body as T;
}

export async function getSession(): Promise<SessionInfo | null> {
  const response = await fetch("/api/session", { credentials: "same-origin" });
  if (response.status === 404) return null;
  if (response.status === 401) return { authenticated: false, nodeOnline: false };
  if (!response.ok) throw new ApiError(response.status, "无法读取登录状态");
  const session = await response.json() as SessionInfo;
  return session.local ? null : session;
}

export function login(password: string) {
  return request<SessionInfo>("/api/session", { method: "POST", body: JSON.stringify({ password }) });
}

export function logout() {
  return request<{ authenticated: boolean }>("/api/session", { method: "DELETE" });
}

export function getResources(params: { query: string; tag: string; unorganized: boolean; limit: number; offset: number }) {
  const search = new URLSearchParams({
    q: params.query,
    tag: params.tag,
    unorganized: params.unorganized ? "1" : "",
    limit: String(params.limit),
    offset: String(params.offset),
  });
  return request<ResourcePage>(`/api/resources?${search}`);
}

export function getRoots() {
  return request<RootSummary[]>("/api/roots");
}

export function getTags() {
  return request<TagSummary[]>("/api/tags");
}

export async function getTagSuggestions(id: string) {
  const result = await request<{ suggestions: TagSuggestion[] }>(`/api/resources/${id}/tag-suggestions`);
  return result.suggestions;
}

export async function getBatchTagSuggestions() {
  const result = await request<{ suggestions: BatchTagSuggestion[]; scanned: number }>("/api/tag-batch-suggestions");
  return result;
}

export function getDirectories(path = "") {
  const search = new URLSearchParams();
  if (path) search.set("path", path);
  return request<DirectoryListing>(`/api/directories${search.size ? `?${search}` : ""}`);
}

export function addRoot(path: string) {
  return request<{ id: string; path: string; existing?: boolean }>("/api/roots", {
    method: "PUT",
    body: JSON.stringify({ path }),
  });
}

export function scanRoot(id: string) {
  return request<{ indexed: number }>(`/api/roots/${id}/scan`, { method: "POST" });
}

export function addResourceTag(id: string, name: string) {
  return request<{ ok: boolean }>(`/api/resources/${id}/tags`, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
}

export function addBatchTag(ids: string[], name: string) {
  return request<{ ok: boolean; updated: number; tag: string }>("/api/resources/tags", {
    method: "POST",
    body: JSON.stringify({ ids, name }),
  });
}

export function fileURL(id: string, download = false) {
	return `/api/resources/${id}/file${download ? "?download=1" : ""}`;
}
