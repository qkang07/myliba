import { waitForWebRTCConnection, webRTCConnected, webRTCFetch } from "./webrtc";

let remoteSession = false;
let activeFolderAccessID = "";
const folderAccessKey = (id: string) => `myliba-folder-access:${id}`;

export function setActiveFolderAccess(id: string | null) {
  activeFolderAccessID = id || "";
}
const contentPath = /^\/api\/(?:resources\/[^/]+\/(?:file|thumbnail|video-preview|video\/[^/]+\/(?:init|segments\/\d+))|cloud\/files\/[^/]+\/(?:file|versions\/[^/]+\/file|assets\/[^/]+\/parts\/[^/]+|video\/[^/]+\/(?:init|segments\/\d+)))(?:\?|$)/;

export interface Resource {
  id: string;
  name: string;
  path: string;
  size: number;
  contentSize: number;
  modified: string;
  kind: string;
  available: boolean;
  versionId: string;
  folderId: string;
  tags: string[];
  attributeTags: AttributeTag[];
}

export interface FileShare {
  id: string;
  resourceId: string;
  createdAt: string;
  expiresAt: string;
  revokedAt?: string;
}

export function listFileShares(resourceId: string) {
  return request<FileShare[]>(`/api/resources/${encodeURIComponent(resourceId)}/shares`);
}

export function createFileShare(resourceId: string, expiresInHours: number) {
  return request<{ share: FileShare; url: string }>(`/api/resources/${encodeURIComponent(resourceId)}/shares`, {
    method: "POST", body: JSON.stringify({ expiresInHours }),
  });
}

export function revokeFileShare(id: string) {
  return request<{ ok: boolean }>(`/api/shares/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export type ResourceSort = "modified_desc" | "modified_asc" | "name_asc" | "name_desc" | "size_desc";

export interface CollectionFilters {
  query: string;
  tags: string[];
  attributes: string[];
  kind: string;
  rootId: string;
  folderId: string;
  unorganized: boolean;
  sort: ResourceSort;
}

export interface SmartCollection {
  id: string;
  name: string;
  filters: CollectionFilters;
  createdAt: string;
}

export interface ResourcePage {
  items: Resource[];
  total: number;
  limit: number;
  offset: number;
}

export interface RootSummary {
  autoPreprocess: boolean;
  id: string;
  path: string;
  addedAt: string;
  lastScanAt: string;
  count: number;
}

export interface MediaStore {
  id: string;
  path: string;
  isDefault: boolean;
  maxBytes: number;
  usedBytes: number;
}

export function getMediaStores() {
  return request<MediaStore[]>("/api/media-stores", { headers: { "X-Myliba-Local": "1" } });
}

export function addMediaStore(path: string, maxBytes: number) {
  return request<{ id: string; path: string; existing?: boolean }>("/api/media-stores", {
    method: "POST", body: JSON.stringify({ path, maxBytes }), headers: { "X-Myliba-Local": "1" },
  });
}

export function setMediaStoreCapacity(id: string, maxBytes: number) {
  return request<void>(`/api/media-stores/${encodeURIComponent(id)}/capacity`, {
    method: "PUT", body: JSON.stringify({ maxBytes }), headers: { "X-Myliba-Local": "1" },
  });
}

export function selectMediaStore(id: string) {
  return request<void>(`/api/media-stores/${encodeURIComponent(id)}/default`, {
    method: "PUT", body: "{}", headers: { "X-Myliba-Local": "1" },
  });
}

export function migrateMediaStore(id: string, targetId: string) {
  return request<void>(`/api/media-stores/${encodeURIComponent(id)}/migrate`, {
    method: "POST", body: JSON.stringify({ targetId }), headers: { "X-Myliba-Local": "1" },
  });
}

export interface VirtualFolder {
  id: string;
  name: string;
  count: number;
  createdAt: string;
  passwordProtected: boolean;
}

export interface AttributeTag {
  key: string;
  category: "type" | "size" | "extension";
  label: string;
  count?: number;
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

export interface TrafficRate {
  downloadBps: number;
  uploadBps: number;
}

export interface SessionInfo {
  local?: boolean;
  authenticated: boolean;
  owner?: boolean;
  nodeOnline: boolean;
  webTraffic?: TrafficRate;
  nodeTraffic?: TrafficRate;
  iceServers?: RTCIceServer[];
  selectedNodeId?: string;
  nodes?: HubResourceNode[];
}

export interface HubResourceNode {
  id: string;
  name: string;
  kind: "desktop" | "android";
  online: boolean;
}

export function selectResourceNode(id: string) {
  return request<{ nodes: HubResourceNode[]; selectedId: string }>("/api/hub/nodes/active", {
    method: "PUT", body: JSON.stringify({ id }),
  });
}

export function removeResourceNode(id: string) {
  return request<void>(`/api/hub/nodes/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export interface TransferEntry { name: string; path: string; dir: boolean; size: number }
export interface TransferDirectory { current: string; parent: string; roots: Array<{ name: string; path: string }>; entries: TransferEntry[] }

async function transferFetch(path: string, init?: RequestInit) {
  const response = await fetch(path, { credentials: "same-origin", ...init });
  if (!response.ok) {
    const body = await response.json().catch(() => undefined);
    throw new ApiError(response.status, body?.error || `传输请求失败 (${response.status})`);
  }
  return response;
}

export async function browseTransferDirectory(path = "") {
  const query = path ? `?path=${encodeURIComponent(path)}` : "";
  return await (await transferFetch(`/api/transfers/browse${query}`)).json() as TransferDirectory;
}

export function readTransferChunk(path: string, offset: number, length: number, signal: AbortSignal) {
  const query = new URLSearchParams({ path, offset: String(offset), length: String(length) });
  return transferFetch(`/api/transfers/file?${query}`, { signal });
}

export function writeTransferChunk(path: string, id: string, offset: number, total: number, chunk: Blob, final: boolean, signal: AbortSignal) {
  const query = new URLSearchParams({ path, id, offset: String(offset), total: String(total) });
  if (final) query.set("final", "1");
  return transferFetch(`/api/transfers/file?${query}`, { method: "PUT", body: chunk, signal, headers: { "Content-Type": "application/octet-stream" } });
}

export function cancelTransferFile(path: string, id: string) {
  const query = new URLSearchParams({ path, id });
  return transferFetch(`/api/transfers/file?${query}`, { method: "DELETE" });
}

export interface HubDevice {
  id: string;
  name: string;
  kind: "android" | "desktop" | "storage";
  canBrowse: boolean;
  canServe: boolean;
  canCloud: boolean;
  createdAt: string;
  revokedAt?: string;
}

export interface HubDevicesView { devices: HubDevice[]; legacyNodeAllowed: boolean }

export function getHubDevices() {
  return request<HubDevicesView>("/api/hub/devices");
}

export function revokeHubDevice(id: string) {
  return request<void>(`/api/hub/devices/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export function setLegacyHubNodes(enabled: boolean) {
  return request<HubDevicesView>("/api/hub/devices/legacy", { method: "PUT", body: JSON.stringify({ enabled }) });
}

export interface HubProfile {
  id: string;
  name: string;
  url: string;
  hasToken: boolean;
}

export interface HubProfilesView {
  profiles: HubProfile[];
  activeId: string;
  status: "disabled" | "connecting" | "connected" | "disconnected" | "auth_required";
}

export function getHubProfiles() {
  return request<HubProfilesView>("/api/hubs");
}

export function saveHubProfile(profile: { id?: string; name: string; url: string; ownerPassword: string }) {
  return request<HubProfilesView>("/api/hubs", { method: "POST", body: JSON.stringify(profile) });
}

export function selectHubProfile(id: string) {
  return request<HubProfilesView>("/api/hubs/active", { method: "PUT", body: JSON.stringify({ id }) });
}

export function deleteHubProfile(id: string) {
  return request<HubProfilesView>(`/api/hubs/${encodeURIComponent(id)}`, { method: "DELETE", body: "{}" });
}

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export async function apiFetch(url: string, init?: RequestInit) {
  if (remoteSession && contentPath.test(url)) await waitForWebRTCConnection();
  if (remoteSession && url.startsWith("/api/") && url !== "/api/session" && !url.startsWith("/api/hub/") && webRTCConnected()) {
    try {
      const response = await webRTCFetch(url, init);
      if (response.status < 500 || !contentPath.test(url) || (init?.method && init.method !== "GET")) return response;
      // Retry server-side content failures through the authenticated Hub relay.
    } catch (error) {
      if (init?.signal?.aborted) throw error;
      if (!contentPath.test(url) || (init?.method && init.method !== "GET")) throw error;
      // A data channel can look connected while the content request itself fails.
      // Retry the safe read through the authenticated Hub relay.
    }
  }
  return fetch(url, init);
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  let folderAccessToken = "";
  if (activeFolderAccessID) {
    try { folderAccessToken = sessionStorage.getItem(folderAccessKey(activeFolderAccessID)) || ""; } catch { /* session storage may be disabled */ }
  }
  const response = await apiFetch(url, {
    credentials: "same-origin",
    ...init,
    headers: {
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(folderAccessToken ? { "X-Myliba-Folder-Access": folderAccessToken } : {}),
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
  if (response.status === 404) { remoteSession = false; return null; }
  remoteSession = true;
  if (response.status === 401) return { authenticated: false, nodeOnline: false, webTraffic: { downloadBps: 0, uploadBps: 0 }, nodeTraffic: { downloadBps: 0, uploadBps: 0 } };
  if (!response.ok) throw new ApiError(response.status, "无法读取登录状态");
  const session = await response.json() as SessionInfo;
  remoteSession = !session.local;
  return session.local ? null : session;
}

export function login(password: string) {
  remoteSession = true;
  return request<SessionInfo>("/api/session", { method: "POST", body: JSON.stringify({ password }) });
}

export interface CloudFile {
  id: string;
  name: string;
  size: number;
  originalSize: number;
  modified: string;
  history: boolean;
  sourceId?: string;
  chunks: number;
  revisionId: string;
  minReplicas: number;
  variants: VideoQuality[];
}

export interface CloudNode {
  id: string;
  endpoint: string;
	lan?: string[];
	links?: Record<string, { state: "checking" | "connected" | "unreachable"; path?: "lan" | "webrtc" | "https" | "relay" | "direct"; checkedAt?: string; checks?: Record<string, { state: "connected" | "unreachable"; rttMillis?: number; connectMillis?: number; speedBps?: number; bytes?: number; durationMillis?: number; httpStatus?: number; selectedPair?: string; error?: string }> }>;
  relayed?: boolean;
  role: "client" | "storage";
  seenAt: string;
}

export interface HubCloudNode extends CloudNode {
  online: boolean;
  statsReady?: boolean;
  backend?: "disk" | "s3" | "webdav" | "openlist";
  blockCount: number;
  blockBytes?: number;
  catalogCount: number;
  capacityBytes?: number;
  availableBytes?: number;
}

export interface HubCloudOverview {
  enabled: boolean;
  nodes: HubCloudNode[];
  online?: number;
  offline?: number;
  reportedBlockCount?: number;
  reportedBlockBytes?: number;
  unknownBlockBytesNodes?: number;
  knownCapacityBytes?: number;
  knownAvailableBytes?: number;
  unknownCapacityNodes?: number;
  catalogRecords?: number;
}

export function getHubCloudOverview() {
  return request<HubCloudOverview>("/api/hub/cloud/overview");
}

export function removeHubCloudNode(id: string) {
  return request<void>(`/api/hub/cloud/nodes/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export interface CloudView {
  files: CloudFile[];
  jobs: Record<string, string>;
  nodeId: string;
  libraryId: string;
  peers: CloudNode[];
	peerStatus?: Record<string, CloudPeerStatus>;
  directEndpoint: string;
	lan?: string[];
}

export interface CloudPathCheck {
  state: "connected" | "unreachable";
  rttMillis?: number;
  connectMillis?: number;
  speedBps?: number;
  bytes?: number;
  durationMillis?: number;
	httpStatus?: number;
	selectedPair?: string;
  error?: string;
}

export interface CloudPeerStatus {
  state: "checking" | "connected" | "unreachable";
  path?: "lan" | "webrtc" | "https" | "relay" | "direct";
  checkedAt?: string;
  checks?: Record<string, CloudPathCheck>;
}

export interface CloudPeerDiagnosis {
  peer: { id: string; role: string; seenAt: string; endpointType: string; lanAddresses: number };
  diagnostics: CloudPeerStatus;
}

export interface CloudSpeedTest {
  peerId: string;
  path: "lan" | "webrtc" | "https" | "relay";
  state: "complete" | "failed";
  bytes?: number;
  durationMillis?: number;
  speedBps?: number;
	connectMillis?: number;
	selectedPair?: string;
  error?: string;
  startedAt: string;
  rounds?: number;
  samples?: Array<{ bytes: number; durationMillis: number; connectMillis: number; speedBps: number }>;
}

export async function getCloudFiles(): Promise<CloudView> {
  const view = await request<CloudView>("/api/cloud/files");
  return { ...view, peers: view.peers ?? [] };
}

export function diagnoseCloudPeer(id: string) {
  return request<CloudPeerDiagnosis>(`/api/cloud/peers/${encodeURIComponent(id)}/diagnose`, { method: "POST", headers: { "X-Myliba-Local": "1" } });
}

export function speedTestCloudPeer(id: string, path: CloudSpeedTest["path"]) {
  return request<CloudSpeedTest>(`/api/cloud/peers/${encodeURIComponent(id)}/speedtest`, { method: "POST", body: JSON.stringify({ path }), headers: { "X-Myliba-Local": "1" } });
}

export function uploadResourceToCloud(id: string, history?: boolean) {
  return request<{ status: string }>(`/api/cloud/resources/${encodeURIComponent(id)}/upload`, { method: "POST", body: JSON.stringify({ history }), headers: { "X-Myliba-Local": "1" } });
}

export function getCloudFile(id: string, revisionId?: string) {
  const base = `/api/cloud/files/${encodeURIComponent(id)}`;
  return apiFetch(revisionId ? `${base}/versions/${encodeURIComponent(revisionId)}/file` : `${base}/file`);
}

export function getCloudVersions(id: string) {
  return request<CloudFile[]>(`/api/cloud/files/${encodeURIComponent(id)}/versions`);
}

export function getCloudRecovery() {
  return request<{ recoveryKey: string; libraryId: string }>("/api/cloud/recovery", { headers: { "X-Myliba-Local": "1" } });
}

export function importCloudRecovery(recoveryKey: string) {
  return request<{ libraryId: string }>("/api/cloud/recovery", { method: "POST", body: JSON.stringify({ recoveryKey }), headers: { "X-Myliba-Local": "1" } });
}

export function logout() {
  return request<{ authenticated: boolean }>("/api/session", { method: "DELETE" });
}

function resourceSearchParams(params: CollectionFilters & { limit: number; offset: number }) {
  const search = new URLSearchParams({
    q: params.query,
    unorganized: params.unorganized ? "1" : "",
    kind: params.kind,
    rootId: params.rootId,
    folderId: params.folderId,
    sort: params.sort,
    limit: String(params.limit),
    offset: String(params.offset),
  });
  for (const tag of params.tags) search.append("tag", tag);
  for (const attribute of params.attributes) search.append("attribute", attribute);
  return search;
}

export function getResources(params: CollectionFilters & { limit: number; offset: number }) {
  return request<ResourcePage>(`/api/resources?${resourceSearchParams(params)}`);
}

export function getResourceSelection(params: CollectionFilters & { limit: number; offset: number }) {
  return request<{ items: Pick<Resource, "id" | "kind" | "available">[]; total: number; limit: number; offset: number }>(`/api/resources/selection?${resourceSearchParams(params)}`);
}

export function getCollections() {
  return request<SmartCollection[]>("/api/collections");
}

export function createCollection(name: string, filters: CollectionFilters) {
  return request<SmartCollection>("/api/collections", { method: "POST", body: JSON.stringify({ name, filters }) });
}

export function updateCollection(id: string, name: string, filters: CollectionFilters) {
  return request<SmartCollection>(`/api/collections/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify({ name, filters }) });
}

export function deleteCollection(id: string) {
  return request<{ ok: boolean }>(`/api/collections/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export function getRoots() {
  return request<RootSummary[]>("/api/roots");
}

export function getTags(folderId = "all") {
  return request<TagSummary[]>("/api/tags?folderId=" + encodeURIComponent(folderId));
}

export function getAttributeTags(filters: CollectionFilters) {
  return request<AttributeTag[]>(`/api/attribute-tags?${resourceSearchParams({ ...filters, limit: 1, offset: 0 })}`);
}

export function getFolders() {
  return request<{ items: VirtualFolder[]; unfiledCount: number }>("/api/folders");
}

export function createFolder(name: string) {
  return request<VirtualFolder>("/api/folders", { method: "POST", body: JSON.stringify({ name }) });
}

export function unlockFolder(id: string, password: string) {
  return request<{ ok: boolean; accessToken: string }>(`/api/folders/${encodeURIComponent(id)}/unlock`, { method: "POST", body: JSON.stringify({ password }) }).then((result) => {
    try { sessionStorage.setItem(folderAccessKey(id), result.accessToken); } catch { /* HttpOnly cookie still authorizes local requests */ }
    activeFolderAccessID = id;
    return result;
  });
}

export function getFolderAccess(id: string) {
  return request<{ protected: boolean; unlocked: boolean }>(`/api/folders/${encodeURIComponent(id)}/access`);
}

export function setFolderPassword(id: string, password: string) {
  return request<{ ok: boolean; passwordProtected: boolean }>(`/api/folders/${encodeURIComponent(id)}/password`, { method: "PUT", body: JSON.stringify({ password }) });
}

export function assignRootToFolder(id: string, rootId: string) {
  return request<{ ok: boolean; updated: number }>(`/api/folders/${encodeURIComponent(id)}/scan`, { method: "POST", body: JSON.stringify({ rootId }) });
}

export function renameFolder(id: string, name: string) {
  return request<VirtualFolder>("/api/folders/" + encodeURIComponent(id), { method: "PUT", body: JSON.stringify({ name }) });
}

export function deleteFolder(id: string) {
  return request<{ ok: boolean }>("/api/folders/" + encodeURIComponent(id), { method: "DELETE" });
}

export function moveResourcesToFolder(ids: string[], folderId: string) {
  return request<{ ok: boolean; updated: number }>("/api/resources/folder", { method: "PUT", body: JSON.stringify({ ids, folderId }) });
}

export async function getTagSuggestions(id: string) {
  const result = await request<{ suggestions: TagSuggestion[] }>(`/api/resources/${id}/tag-suggestions`);
  return result.suggestions;
}

export async function getBatchTagSuggestions(folderId = "all") {
  const result = await request<{ suggestions: BatchTagSuggestion[]; scanned: number }>("/api/tag-batch-suggestions?folderId=" + encodeURIComponent(folderId));
  return result;
}

export function getDirectories(path = "") {
  const search = new URLSearchParams();
  if (path) search.set("path", path);
  return request<DirectoryListing>(`/api/directories${search.size ? `?${search}` : ""}`);
}

export function addRoot(path: string, autoPreprocess: boolean) {
  return request<{ id: string; path: string; existing?: boolean }>("/api/roots", {
    method: "PUT",
    body: JSON.stringify({ path, autoPreprocess }),
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
	return "/api/resources/" + id + "/file" + (download ? "?download=1" : "");
}

export function thumbnailURL(resource: Pick<Resource, "id" | "size" | "modified">, revision = 0) {
  const version = encodeURIComponent(`${resource.size}-${resource.modified}`);
  return `/api/resources/${resource.id}/thumbnail?v=${version}&r=${revision}`;
}

export function getResourceFile(id: string, download = false, signal?: AbortSignal) {
  return apiFetch(fileURL(id, download), { signal });
}

export function getResourceThumbnail(resource: Pick<Resource, "id" | "size" | "modified">, revision = 0) {
  return apiFetch(thumbnailURL(resource, revision));
}
export function createThumbnail(id: string) {
  return request<void>(`/api/resources/${id}/thumbnail`, { method: "POST" });
}
export function getResource(id: string) {
  return request<Resource>(`/api/resources/${encodeURIComponent(id)}`);
}

export function getVideoInfo(id: string) {
  return request<{ duration: number }>(`/api/resources/${encodeURIComponent(id)}/video-info`);
}

export type VideoQuality = "360p" | "720p" | "1080p";
export interface VideoManifest {
  status: "missing" | "processing" | "failed" | "ready";
  mode?: "ondemand";
  error?: string;
  codec?: string;
  duration?: number;
  segments?: number[];
}
export function videoVariantURL(id: string, quality: VideoQuality) {
  return `/api/resources/${encodeURIComponent(id)}/video/${quality}`;
}
export function getVideoVariant(id: string, quality: VideoQuality, onDemand = false, signal?: AbortSignal) {
  return request<VideoManifest>(videoVariantURL(id, quality) + (onDemand ? "?mode=ondemand" : ""), { signal });
}
export function createVideoVariant(id: string, quality: VideoQuality) {
  return request<VideoManifest>(videoVariantURL(id, quality), { method: "POST" });
}
export function getVideoPart(id: string, quality: VideoQuality, part: "init" | number, onDemand = false, signal?: AbortSignal) {
  return apiFetch(`${videoVariantURL(id, quality)}/${part === "init" ? "init" : `segments/${part}`}${onDemand ? "?mode=ondemand" : ""}`, { signal });
}

export function cloudVideoVariantURL(id: string, quality: VideoQuality) {
  return `/api/cloud/files/${encodeURIComponent(id)}/video/${quality}`;
}
export function getCloudVideoVariant(id: string, quality: VideoQuality, onDemand = false, signal?: AbortSignal) {
  return request<VideoManifest>(cloudVideoVariantURL(id, quality) + (onDemand ? "?mode=ondemand" : ""), { signal });
}
export function getCloudVideoPart(id: string, quality: VideoQuality, part: "init" | number, onDemand = false, signal?: AbortSignal) {
  return apiFetch(`${cloudVideoVariantURL(id, quality)}/${part === "init" ? "init" : `segments/${part}`}${onDemand ? "?mode=ondemand" : ""}`, { signal });
}

export function removeResourceTag(id: string, tagID: string) {
  return request<{ ok: boolean }>(`/api/resources/${encodeURIComponent(id)}/tags/${encodeURIComponent(tagID)}`, { method: "DELETE" });
}

export function getResourceThumbnailVariant(resource: Pick<Resource, "id" | "size" | "modified">, size: "small" | "medium" | "large", at = 0, revision = 0) {
  const url = new URL(thumbnailURL(resource, revision), window.location.origin);
  url.searchParams.set("size", size);
  if (at > 0) url.searchParams.set("at", at.toFixed(3));
  return apiFetch(url.pathname + url.search);
}

export function createThumbnailVariant(id: string, size: "small" | "medium" | "large", at = 0) {
  const search = new URLSearchParams({ size });
  if (at > 0) search.set("at", at.toFixed(3));
  return request<void>(`/api/resources/${encodeURIComponent(id)}/thumbnail?${search}`, { method: "POST" });
}

export interface PreprocessJob {
  resourceId: string;
  name: string;
  operation: string;
  status: "pending" | "running" | "done" | "failed";
  priority: number;
  updatedAt: string;
  error: string;
}

export interface PreprocessQueue {
  counts: Record<"pending" | "running" | "done" | "failed", number>;
  jobs: PreprocessJob[];
  visible: Record<string, PreprocessJob["status"]>;
}

export function getPreprocessQueue(ids: string[] = []) {
  const search = ids.length ? "?ids=" + encodeURIComponent(ids.join(",")) : "";
  return request<PreprocessQueue>("/api/preprocess" + search);
}

export function prioritizePreprocessJobs(ids: string[]) {
  return request<void>("/api/preprocess/prioritize", { method: "POST", body: JSON.stringify({ ids }) });
}

export function retryPreprocessJob(id: string) {
  return request<void>(`/api/preprocess/${encodeURIComponent(id)}/retry`, { method: "POST" });
}
export function retryFailedPreprocessJobs() {
  return request<{ retried: number }>("/api/preprocess/retry-failed", { method: "POST" });
}
export interface PreviewClip { start: number; end: number }
export interface PreviewSpec { quality: "360p" | "720p" | "1080p"; clips: PreviewClip[] }
export function getVideoPreview(id: string) {
  return request<{ status: string; error?: string; spec?: PreviewSpec }>(`/api/resources/${encodeURIComponent(id)}/video-preview`);
}
export function createVideoPreview(id: string, spec: PreviewSpec) {
  return request<{ status: string }>(`/api/resources/${encodeURIComponent(id)}/video-preview`, { method: "POST", body: JSON.stringify(spec) });
}
export function getVideoPreviewFile(id: string) {
  return apiFetch(`/api/resources/${encodeURIComponent(id)}/video-preview?file=1`);
}
