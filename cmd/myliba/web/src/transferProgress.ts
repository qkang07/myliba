export async function readBlobWithProgress(
  response: Response,
  onProgress: (percent: number | null, received?: number, total?: number) => void,
  expectedSize = 0,
  signal?: AbortSignal,
): Promise<Blob> {
  if (!response.ok) throw new Error(`文件读取失败 (HTTP ${response.status})`);
  const total = Number(response.headers.get("Content-Length")) || expectedSize;
  const reader = response.body?.getReader();
  if (!reader) return response.blob();
  const parts: Uint8Array[] = [];
  let received = 0;
  onProgress(total > 0 ? 0 : null, 0, total);
  try {
    while (true) {
      if (signal?.aborted) throw new DOMException("已取消", "AbortError");
      const { done, value } = await reader.read();
      if (done) break;
      parts.push(value);
      received += value.byteLength;
      onProgress(total > 0 ? Math.min(100, Math.round(received * 100 / total)) : null, received, total);
    }
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
  onProgress(100, received, total);
  return new Blob(parts as BlobPart[], { type: response.headers.get("Content-Type") || "application/octet-stream" });
}
