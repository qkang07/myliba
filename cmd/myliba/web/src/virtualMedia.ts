import { apiFetch, fileURL } from "./api";
import { webRTCFetch } from "./webrtc";

type Source = { read: (range: string, signal: AbortSignal) => Promise<Response>; controllers: Set<AbortController> };
const sources = new Map<string, Source>();
let initialized: Promise<void> | undefined;
let listening = false;

function initialize(): Promise<void> {
  if (initialized) return initialized;
  initialized = (async () => {
    if (!("serviceWorker" in navigator) || !window.isSecureContext) throw new Error("远程视频流需要 HTTPS 或本机访问");
    if (!listening) navigator.serviceWorker.addEventListener("message", (event: MessageEvent) => {
      const data = event.data as { type?: string; token?: string; start?: number; end?: number };
      const port = event.ports[0];
      if (data?.type !== "myliba-media-read" || !port || !data.token) return;
      const source = sources.get(data.token);
      if (!source || !Number.isSafeInteger(data.start) || !Number.isSafeInteger(data.end) || data.start! < 0 || data.end! < data.start! || data.end! - data.start! >= 512 * 1024) {
        port.postMessage({ error: "视频分段请求无效" }); port.close(); return;
      }
      const abort = new AbortController();
      source.controllers.add(abort);
      port.onmessage = () => abort.abort();
      void (async () => {
        try {
          const response = await source.read(`bytes=${data.start}-${data.end}`, abort.signal);
          const range = response.headers.get("Content-Range") || "";
          const match = /^bytes (\d+)-(\d+)\/(\d+)$/.exec(range);
          if (response.status !== 206 || !match || Number(match[1]) !== data.start || Number(match[2]) > data.end!) {
            await response.body?.cancel(); throw new Error("资源节点不支持视频分段读取");
          }
          const bytes = await response.arrayBuffer();
          if (bytes.byteLength !== Number(match[2]) - Number(match[1]) + 1) throw new Error("视频分段传输不完整");
          port.postMessage({ bytes, range, contentType: response.headers.get("Content-Type") || "application/octet-stream" }, [bytes]);
        } catch (error) { port.postMessage({ error: error instanceof Error ? error.message : "视频分段读取失败" }); }
        finally { source.controllers.delete(abort); port.close(); }
      })();
    });
    listening = true;
    await navigator.serviceWorker.register("/media-stream-worker.js", { scope: "/" });
    await navigator.serviceWorker.ready;
    if (!navigator.serviceWorker.controller) await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => { navigator.serviceWorker.removeEventListener("controllerchange", changed); reject(new Error("视频流服务启动超时")); }, 10000);
      const changed = () => { if (navigator.serviceWorker.controller) { clearTimeout(timeout); navigator.serviceWorker.removeEventListener("controllerchange", changed); resolve(); } };
      navigator.serviceWorker.addEventListener("controllerchange", changed);
      changed();
    });
  })();
  initialized.catch(() => { initialized = undefined; });
  return initialized;
}

export async function openVirtualMedia(resourceID: string, options: { cloud?: boolean; shareKey?: string } = {}): Promise<{ url: string; close: () => void }> {
  await initialize();
  const token = crypto.randomUUID();
  const path = options.cloud ? `/api/cloud/files/${encodeURIComponent(resourceID)}/file` : options.shareKey ? `/api/shares/${encodeURIComponent(resourceID)}/file` : fileURL(resourceID);
  const source: Source = { read: (range, signal) => options.shareKey
    ? webRTCFetch(path, { signal, headers: { Range: range, "X-Myliba-Share-Key": options.shareKey } })
    : apiFetch(path, { signal, headers: { Range: range } }), controllers: new Set() };
  sources.set(token, source);
  return { url: `/__myliba_media/${token}`, close: () => { sources.delete(token); for (const abort of source.controllers) abort.abort(); } };
}
