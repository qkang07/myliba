import { ref, type Ref } from "vue";
import {
  getResource,
  getLibraryReadCandidate,
  resolveLibraryFile,
  selectResourceNode,
  type CloudFile,
  type HubResourceNode,
} from "./api";

export interface CloudReadOptions {
  remote: Ref<boolean>;
  queryNodeId: Ref<string>;
  nodes: Ref<HubResourceNode[]>;
  onError: (message: string) => void;
  onOpenResource?: (id: string) => void;
}

export interface CloudPreview {
  fileID: string;
  url: string;
  mime: string;
  kind: "image" | "audio" | "document" | "video";
}

export function previewKind(file: CloudFile): "image" | "audio" | "document" | "video" | null {
  if (file.variants?.length) return "video";
  const ext = file.name.split(".").pop()?.toLocaleLowerCase() || "";
  if (["jpg", "jpeg", "png", "gif", "webp", "bmp", "avif"].includes(ext)) return "image";
  if (["mp3", "m4a", "aac", "ogg", "oga", "wav", "flac"].includes(ext)) return "audio";
  if (["mp4", "m4v", "mov", "webm", "mkv"].includes(ext)) return "video";
  if (["pdf", "txt", "md", "csv"].includes(ext)) return "document";
  return null;
}

export function useCloudRead(options: CloudReadOptions) {
  const activeDownload = ref("");
  const activeVideo = ref("");
  const activePreview = ref<CloudPreview | null>(null);

  function nodeLabel(id?: string) {
    if (!id) return "本机";
    const node = options.nodes.value.find((item) => item.id === id);
    if (node) return node.name;
    if (!options.remote.value && id === options.queryNodeId.value) return "本机";
    return id.slice(0, 8);
  }

  function readableLocation(file: CloudFile) {
    return file.locations?.find((location) => location.nodeId && location.resourceRef);
  }

  function closePreview() {
    if (activePreview.value) URL.revokeObjectURL(activePreview.value.url);
    activePreview.value = null;
  }

  async function openDeviceFile(file: CloudFile) {
    const locations = file.locations?.filter((location) => location.nodeId && location.resourceRef) ?? [];
    if (!locations.length) return;
    if (!options.remote.value) {
      options.onError("请从 Hub 资源库打开此文件；来源设备路径不会暴露给本机索引。");
      return;
    }
    activeDownload.value = file.id;
    let failure = "没有可读取的来源";
    for (const location of locations) {
      try {
        await selectResourceNode(location.nodeId);
        const resource = await getResource(location.resourceRef!);
        if (!resource.available) throw new Error("来源暂不可用");
        if (file.revisionId && resource.versionId !== file.revisionId) throw new Error("来源版本与检索结果不一致");
        try { window.localStorage.setItem("myliba:selected-resource-node", location.nodeId); } catch { /* Session state remains authoritative. */ }
        const target = new URL(window.location.href);
        target.searchParams.delete("folder");
        target.searchParams.set("resource", location.resourceRef!);
        window.location.replace(target.href);
        return;
      } catch (cause) {
        failure = cause instanceof Error ? cause.message : "无法连接来源设备";
      }
    }
    options.onError(`无法打开来源：${failure}`);
    activeDownload.value = "";
  }

  async function download(file: CloudFile, name: string, revisionId?: string) {
    if (activeDownload.value) return;
    activeDownload.value = file.id;
    const queryNode = options.queryNodeId.value;
    let switched = false;
    try {
      let data: Blob | undefined;
      const wantedRevision = revisionId || file.revisionId || "";
      const resolved = await resolveLibraryFile(file.fileId || file.id, wantedRevision);
      let failure = resolved.reason || "没有可读取的候选位置";
      for (const candidate of resolved.candidates) {
        try {
          if (candidate.kind === "cloud" && candidate.objectId) {
            if (switched && queryNode) { await selectResourceNode(queryNode); switched = false; }
            const response = await getLibraryReadCandidate(candidate);
            if (!response.ok) throw new Error(`云库下载失败 (HTTP ${response.status})`);
            data = await response.blob();
          } else if (candidate.kind === "device" && candidate.nodeId && candidate.resourceRef && (options.remote.value || candidate.nodeId === queryNode)) {
            if (options.remote.value && candidate.nodeId !== queryNode) { await selectResourceNode(candidate.nodeId); switched = true; }
            const source = await getResource(candidate.resourceRef);
            if (!source.available || source.versionId !== candidate.revisionId) throw new Error("来源版本与检索结果不一致");
            const response = await getLibraryReadCandidate(candidate);
            if (!response.ok) throw new Error(`来源下载失败 (HTTP ${response.status})`);
            const bytes = await response.blob();
            const after = await getResource(candidate.resourceRef);
            if (source.size !== bytes.size || !after.available || after.versionId !== source.versionId || after.size !== source.size) throw new Error("来源在下载期间发生变化，已丢弃此候选");
            data = bytes;
          }
          if (data) break;
        } catch (cause) {
          failure = cause instanceof Error ? cause.message : failure;
        }
      }
      if (!data) throw new Error(failure);
      const url = URL.createObjectURL(data);
      const link = document.createElement("a");
      link.href = url;
      link.download = name;
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (cause) { options.onError(cause instanceof Error ? cause.message : "无法下载文件"); }
    finally {
      if (switched && queryNode) {
        try { await selectResourceNode(queryNode); } catch { /* A later refresh will report query-node availability. */ }
      }
      activeDownload.value = "";
    }
  }

  async function previewVideoClip(file: CloudFile): Promise<boolean> {
    const queryNode = options.queryNodeId.value;
    let selectedNode = queryNode;
    try {
      const resolved = await resolveLibraryFile(file.fileId || file.id, file.revisionId, "preview");
      for (const candidate of resolved.candidates) {
        if (candidate.assetType !== "videoPreview" || !candidate.readPath || !candidate.nodeId || !candidate.resourceRef) continue;
        if (!options.remote.value && candidate.nodeId !== queryNode) continue;
        try {
          if (options.remote.value && candidate.nodeId !== selectedNode) { await selectResourceNode(candidate.nodeId); selectedNode = candidate.nodeId; }
          const source = await getResource(candidate.resourceRef);
          if (!source.available || source.versionId !== candidate.revisionId) continue;
          const response = await getLibraryReadCandidate(candidate);
          if (!response.ok || !(response.headers.get("Content-Type") || "").startsWith("video/")) continue;
          closePreview();
          activePreview.value = { fileID: file.id, url: URL.createObjectURL(await response.blob()), mime: "video/mp4", kind: "video" };
          return true;
        } catch { /* Another candidate or the ordinary video stream may work. */ }
      }
    } catch { /* Fall back to the original video stream. */ }
    finally { if (selectedNode !== queryNode && queryNode) { try { await selectResourceNode(queryNode); } catch { /* Refresh reports node availability. */ } } }
    return false;
  }

  async function preview(file: CloudFile) {
    const kind = previewKind(file);
    if (!kind) return;
    if (file.sourceId) {
      try {
        const source = await getResource(file.sourceId);
        if (source.available && source.versionId === file.revisionId && source.modified === file.modified && source.size === (file.originalSize ?? file.size)) {
          closePreview();
          activeVideo.value = "";
          options.onOpenResource?.(source.id);
          return;
        }
      } catch { /* The source may only exist in the cloud catalog. */ }
    }
    if (kind === "video" && !file.localOnly) {
      if (activePreview.value?.fileID === file.id) { closePreview(); return; }
      if (await previewVideoClip(file)) { activeVideo.value = ""; return; }
      closePreview();
      activeVideo.value = activeVideo.value === file.id ? "" : file.id;
      return;
    }
    activeVideo.value = "";
    if (activePreview.value) URL.revokeObjectURL(activePreview.value.url);
    activePreview.value = null;
    activeDownload.value = file.id;
    const queryNode = options.queryNodeId.value;
    let selectedNode = queryNode;
    try {
      const resolved = await resolveLibraryFile(file.fileId || file.id, file.revisionId, "preview");
      let failure = resolved.reason || "没有可读取的预览候选";
      for (const candidate of resolved.candidates) {
        try {
          if (candidate.kind === "device") {
            if (!candidate.nodeId || !candidate.resourceRef || !options.remote.value && candidate.nodeId !== options.queryNodeId.value) continue;
            if (options.remote.value && candidate.nodeId !== selectedNode) { await selectResourceNode(candidate.nodeId); selectedNode = candidate.nodeId; }
            const source = await getResource(candidate.resourceRef);
            if (!source.available || source.versionId !== candidate.revisionId) throw new Error("来源版本已变化");
          } else if (selectedNode !== queryNode && queryNode) { await selectResourceNode(queryNode); selectedNode = queryNode; }
          const response = await getLibraryReadCandidate(candidate);
          if (!response.ok) throw new Error(`预览候选不可用 (HTTP ${response.status})`);
          const url = URL.createObjectURL(await response.blob());
          activePreview.value = { fileID: file.id, url, mime: response.headers.get("Content-Type") || "application/octet-stream", kind };
          break;
        } catch (cause) { failure = cause instanceof Error ? cause.message : failure; }
      }
      if (!activePreview.value) throw new Error(failure);
    } catch (cause) { options.onError(cause instanceof Error ? cause.message : "无法预览文件"); }
    finally { if (selectedNode !== queryNode && queryNode) { try { await selectResourceNode(queryNode); } catch { /* A later refresh reports the active node. */ } } activeDownload.value = ""; }
  }

  return { activeDownload, activeVideo, activePreview, nodeLabel, readableLocation, closePreview, openDeviceFile, download, preview };
}