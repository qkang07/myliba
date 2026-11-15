import {
  ApiError,
  getResources,
  searchLibraryFiles,
  type CloudFile,
  type CollectionFilters,
  type LibrarySearchPage,
  type Resource,
  type ResourceSort,
} from "./api";
import { searchOfflineIndex } from "./libraryOffline";

export type LibraryScope = "node" | "cloud" | "all";

export type LibraryBadge = "cloud" | "deviceOnly" | "history" | "unavailable";

export interface LibraryItem {
  key: string;
  source: "node" | "cloud";
  name: string;
  kind: string;
  size: number;
  modified: string;
  available: boolean;
  badges: LibraryBadge[];
  resource?: Resource;
  cloudFile?: CloudFile;
}

export interface LibraryPagination {
  limit: number;
  offset?: number;
  cursor?: string;
}

export type LibraryPage =
  | {
      scope: "node";
      resources: Resource[];
      items: LibraryItem[];
      total: number;
      nextOffset: number;
      hasMore: boolean;
    }
  | {
      scope: "cloud" | "all";
      cloudFiles: CloudFile[];
      items: LibraryItem[];
      total: number;
      nextCursor: string;
      hasMore: boolean;
      nodeId: string;
      libraryId: string;
      offline: boolean;
      facets: LibrarySearchPage["facets"];
      coverage: LibrarySearchPage["coverage"];
      jobs: Record<string, string>;
    };

export interface CloudFilters {
  lifecycle?: "active" | "trashed";
  grouped?: boolean;
  cloudCopy?: boolean;
  readable?: boolean;
}

type LibraryType = "image" | "video" | "audio" | "document" | "other";
type LibrarySort = "name_asc" | "name_desc" | "modified_desc" | "modified_asc" | "ingested_desc" | "ingested_asc";

const KIND_TO_TYPE: Record<string, LibraryType> = {
  image: "image",
  video: "video",
  audio: "audio",
  document: "document",
  other: "other",
};

function librarySort(sort: ResourceSort): LibrarySort {
  switch (sort) {
    case "name_asc": return "name_asc";
    case "name_desc": return "name_desc";
    case "modified_asc": return "modified_asc";
    case "size_desc": return "ingested_desc";
    default: return "modified_desc";
  }
}

function cloudKind(file: CloudFile): string {
  if (file.variants?.length) return "video";
  const ext = file.name.split(".").pop()?.toLocaleLowerCase() || "";
  if (["jpg", "jpeg", "png", "gif", "webp", "bmp", "avif"].includes(ext)) return "image";
  if (["mp3", "m4a", "aac", "ogg", "oga", "wav", "flac"].includes(ext)) return "audio";
  if (["mp4", "m4v", "mov", "webm", "mkv"].includes(ext)) return "video";
  if (["pdf", "txt", "md", "csv"].includes(ext)) return "document";
  return "other";
}

export function mapResource(resource: Resource): LibraryItem {
  const badges: LibraryBadge[] = [];
  if (!resource.available) badges.push("unavailable");
  return {
    key: resource.id,
    source: "node",
    name: resource.name,
    kind: resource.kind,
    size: resource.contentSize || resource.size,
    modified: resource.modified,
    available: resource.available,
    badges,
    resource,
  };
}

export function mapCloudFile(file: CloudFile): LibraryItem {
  const badges: LibraryBadge[] = [];
  if (file.localOnly) badges.push("deviceOnly");
  else badges.push("cloud");
  if (file.history) badges.push("history");
  return {
    key: `${file.fileId || file.id}@${file.revisionId}`,
    source: "cloud",
    name: file.name,
    kind: cloudKind(file),
    size: file.originalSize || file.size,
    modified: file.modified,
    available: file.localOnly ? Boolean(file.sourceId) : (file.availableReplicas ?? 0) > 0,
    badges,
    cloudFile: file,
  };
}

export async function loadLibraryPage(scope: LibraryScope, filters: CollectionFilters, page: LibraryPagination, cloud?: CloudFilters): Promise<LibraryPage> {
  if (scope === "node") {
    const offset = page.offset ?? 0;
    const result = await getResources({ ...filters, limit: page.limit, offset });
    const nextOffset = offset + result.items.length;
    return {
      scope: "node",
      resources: result.items,
      items: result.items.map(mapResource),
      total: result.total,
      nextOffset,
      hasMore: nextOffset < result.total,
    };
  }
  const cursor = page.cursor ?? "";
  const input = {
    query: filters.query,
    scope,
    lifecycle: cloud?.lifecycle ?? "active",
    folderId: filters.folderId && filters.folderId !== "all" ? filters.folderId : "",
    tags: filters.tags,
    type: KIND_TO_TYPE[filters.kind],
    grouped: cloud?.grouped,
    cloudCopy: cloud?.cloudCopy,
    readable: cloud?.readable,
    sort: librarySort(filters.sort),
    cursor,
    limit: page.limit,
  };
  let result: LibrarySearchPage;
  let offline = false;
  if (cursor.startsWith("offline:")) {
    const snapshot = searchOfflineIndex(input);
    if (!snapshot) throw new ApiError(409, "离线索引已变化，请重新检索");
    result = snapshot;
    offline = true;
  } else {
    try {
      result = await searchLibraryFiles(input);
    } catch (cause) {
      const clientError = cause instanceof ApiError && cause.status < 500 && cause.status !== 0;
      if (clientError || cursor) throw cause;
      const snapshot = searchOfflineIndex(input);
      if (!snapshot) throw cause;
      result = snapshot;
      offline = true;
    }
  }
  return {
    scope,
    cloudFiles: result.items,
    items: result.items.map(mapCloudFile),
    total: result.total,
    nextCursor: result.nextCursor,
    hasMore: Boolean(result.nextCursor),
    nodeId: result.nodeId,
    libraryId: result.libraryId,
    offline,
    facets: result.facets,
    coverage: result.coverage,
    jobs: result.jobs,
  };
}