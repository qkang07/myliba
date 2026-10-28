import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import { MessageChannel } from "node:worker_threads";

test("media bridge reads bounded ranges and seeks without downloading the whole file", async () => {
  const listeners = new Map();
  const reads = [];
  const total = 3 * 1024 * 1024;
  const client = { postMessage(message, ports) {
    reads.push([message.start, message.end]);
    const bytes = new Uint8Array(message.end - message.start + 1).fill(message.start === 0 ? 7 : 9);
    ports[0].postMessage({ bytes: bytes.buffer, range: `bytes ${message.start}-${message.end}/${total}`, contentType: "video/mp4" });
    ports[0].close();
  } };
  const self = { location: { origin: "https://example.test" }, clients: { get: async () => client }, addEventListener: (name, fn) => listeners.set(name, fn) };
  vm.runInNewContext(await readFile(new URL("./public/media-stream-worker.js", import.meta.url), "utf8"), { self, URL, Request, Response, ReadableStream, AbortController, MessageChannel, Uint8Array, setTimeout, clearTimeout });
  const request = async range => {
    let response;
    const event = { request: new Request("https://example.test/__myliba_media/00000000-0000-4000-8000-000000000000", { headers: range ? { Range: range } : {} }), clientId: "page", respondWith(value) { response = value; } };
    listeners.get("fetch")(event);
    return response;
  };
  const first = await request("bytes=0-");
  assert.equal(first.status, 206);
  assert.equal(first.headers.get("Content-Range"), `bytes 0-${total - 1}/${total}`);
  const reader = first.body.getReader();
  const block = await reader.read();
  assert.equal(block.value.byteLength, 512 * 1024);
  await reader.cancel();
  assert.ok(reads.every(([start, end]) => end - start < 512 * 1024));
  assert.ok(reads.length < 6, "starting playback must not fetch the entire file");
  const seek = await request("bytes=2097152-2097199");
  assert.equal((await seek.arrayBuffer()).byteLength, 48);
  assert.ok(reads.some(([start, end]) => start === 2097152 && end === 2097199));
  const invalid = await request(`bytes=${total}-`);
  assert.equal(invalid.status, 416);
});
