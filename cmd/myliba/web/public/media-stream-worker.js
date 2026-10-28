/* A seekable HTTP view backed by bounded, authenticated range requests from the page. */
const PREFIX = "/__myliba_media/";
const CHUNK_SIZE = 512 * 1024;
self.addEventListener("install", event => event.waitUntil(self.skipWaiting()));
self.addEventListener("activate", event => event.waitUntil(self.clients.claim()));

function readChunk(client, token, start, end, signal) {
  return new Promise((resolve, reject) => {
    const channel = new MessageChannel();
    const timer = setTimeout(() => { channel.port1.postMessage({ cancel: true }); finish(new Error("视频分段读取超时")); }, 30000);
    const abort = () => { channel.port1.postMessage({ cancel: true }); finish(new Error("视频读取已取消")); };
    const finish = (error, result) => {
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      channel.port1.close();
      if (error) reject(error); else resolve(result);
    };
    channel.port1.onmessage = event => event.data?.error ? finish(new Error(event.data.error)) : finish(null, event.data);
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) { abort(); return; }
    client.postMessage({ type: "myliba-media-read", token, start, end }, [channel.port2]);
  });
}

self.addEventListener("fetch", event => {
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin || !url.pathname.startsWith(PREFIX)) return;
  event.respondWith((async () => {
    if (event.request.method !== "GET") return new Response(null, { status: 405 });
    const token = url.pathname.slice(PREFIX.length);
    if (!/^[0-9a-f-]{36}$/i.test(token)) return new Response(null, { status: 404 });
    const client = await self.clients.get(event.clientId);
    if (!client) return new Response(null, { status: 404 });
    const stop = new AbortController();
    event.request.signal.addEventListener("abort", () => stop.abort(), { once: true });
    try {
      const probe = await readChunk(client, token, 0, 0, stop.signal);
      const match = /^bytes 0-0\/(\d+)$/.exec(probe.range);
      const total = match ? Number(match[1]) : 0;
      if (!Number.isSafeInteger(total) || total <= 0) throw new Error("视频长度无效");
      let start = 0, end = total - 1;
      const requested = event.request.headers.get("Range");
      if (requested) {
        const range = /^bytes=(\d*)-(\d*)$/.exec(requested);
        if (!range || (!range[1] && !range[2])) return new Response(null, { status: 416, headers: { "Content-Range": `bytes */${total}` } });
        if (!range[1]) { const suffix = Number(range[2]); if (!Number.isSafeInteger(suffix) || suffix <= 0) return new Response(null, { status: 416 }); start = Math.max(0, total - suffix); }
        else { start = Number(range[1]); if (range[2]) end = Math.min(Number(range[2]), end); }
        if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start >= total || end < start) return new Response(null, { status: 416, headers: { "Content-Range": `bytes */${total}` } });
      }
      let offset = start;
      const body = new ReadableStream({
        async pull(controller) {
          if (offset > end) { controller.close(); return; }
          try {
            const last = Math.min(offset + CHUNK_SIZE - 1, end);
            const part = await readChunk(client, token, offset, last, stop.signal);
            const parsed = /^bytes (\d+)-(\d+)\/(\d+)$/.exec(part.range);
            if (!parsed || Number(parsed[1]) !== offset || Number(parsed[2]) !== last || Number(parsed[3]) !== total || part.bytes.byteLength !== last - offset + 1) throw new Error("视频分段内容发生变化");
            offset = last + 1;
            controller.enqueue(new Uint8Array(part.bytes));
          } catch (error) { controller.error(error); }
        },
        cancel() { stop.abort(); },
      });
      const headers = { "Content-Type": probe.contentType, "Accept-Ranges": "bytes", "Content-Length": String(end - start + 1), "Cache-Control": "no-store" };
      if (requested) headers["Content-Range"] = `bytes ${start}-${end}/${total}`;
      return new Response(body, { status: requested ? 206 : 200, headers });
    } catch { return new Response(null, { status: 502 }); }
  })());
});
