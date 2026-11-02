import { selectResourceNode } from "./api";

type Message = { type: string; id?: string; status?: number; headers?: Record<string, string[]>; data?: string; error?: string };
type Pending = { resolve: (response: Response) => void; reject: (error: Error) => void; controller?: ReadableStreamDefaultController<Uint8Array>; started: boolean; abort?: () => void; cleanup?: () => void };

/** A browser-to-resource-node WebRTC channel used for node-to-node transfers. */
export class TransferNodeConnection {
  private peer?: RTCPeerConnection;
  private channel?: RTCDataChannel;
  private socket?: WebSocket;
  private pending = new Map<string, Pending>();
  private remoteReady = false;
  private earlyCandidates: RTCIceCandidateInit[] = [];

  static async open(nodeId: string, iceServers: RTCIceServer[]) {
    await selectResourceNode(nodeId);
    const connection = new TransferNodeConnection();
    try { await connection.connect(iceServers); return connection; }
    catch (error) { connection.close(); throw error; }
  }

  private async connect(iceServers: RTCIceServer[]) {
    const peer = new RTCPeerConnection({ iceServers });
    this.peer = peer;
    const socketURL = new URL("/signal", window.location.href);
    socketURL.protocol = socketURL.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(socketURL);
    this.socket = socket;
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("节点信令连接超时")), 10000);
      socket.addEventListener("open", () => { clearTimeout(timeout); resolve(); }, { once: true });
      socket.addEventListener("error", () => { clearTimeout(timeout); reject(new Error("无法连接节点信令服务")); }, { once: true });
    });
    const channel = peer.createDataChannel("myliba-transfer", { ordered: true });
    this.channel = channel;
    channel.onmessage = (event) => { if (typeof event.data === "string") this.onMessage(event.data); };
    channel.onclose = () => this.fail(new Error("节点 WebRTC 通道已关闭"));
    peer.onicecandidate = (event) => { if (event.candidate && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "candidate", candidate: event.candidate.toJSON() })); };
    socket.onmessage = (event) => {
      let signal: { type: string; description?: RTCSessionDescriptionInit; candidate?: RTCIceCandidateInit };
      try { signal = JSON.parse(String(event.data)); } catch { return; }
      if (signal.type === "answer" && signal.description) {
        void peer.setRemoteDescription(signal.description).then(async () => {
          this.remoteReady = true;
          for (const candidate of this.earlyCandidates.splice(0)) await peer.addIceCandidate(candidate).catch(() => undefined);
        }).catch(() => this.fail(new Error("节点 WebRTC 协商失败")));
      } else if (signal.type === "candidate" && signal.candidate) {
        if (this.remoteReady) void peer.addIceCandidate(signal.candidate).catch(() => undefined);
        else this.earlyCandidates.push(signal.candidate);
      } else if (signal.type === "error") this.fail(new Error("节点 WebRTC 连接失败"));
    };
    const offer = await peer.createOffer();
    await peer.setLocalDescription(offer);
    socket.send(JSON.stringify({ type: "offer", description: peer.localDescription?.toJSON() ?? offer }));
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("节点 WebRTC 连接超时")), 20000);
      const check = () => {
        if (channel.readyState === "open") { clearTimeout(timeout); resolve(); }
        else if (channel.readyState === "closed" || peer.connectionState === "failed") { clearTimeout(timeout); reject(new Error("节点 WebRTC 连接失败")); }
        else setTimeout(check, 100);
      };
      check();
    });
  }

  private onMessage(raw: string) {
    let message: Message;
    try { message = JSON.parse(raw); } catch { return; }
    if (!message.id) return;
    const pending = this.pending.get(message.id);
    if (!pending) return;
    if (message.type === "start") {
      const headers = new Headers();
      for (const [name, values] of Object.entries(message.headers ?? {})) for (const value of values) headers.append(name, value);
      const status = message.status || 502;
      const body = status === 204 || status === 205 || status === 304 ? null : new ReadableStream<Uint8Array>({
        start: (controller) => { pending.controller = controller; },
        cancel: () => { pending.abort?.(); void this.sendControl({ type: "request-cancel", id: message.id }); this.pending.delete(message.id!); },
      });
      pending.started = true;
      pending.resolve(new Response(body, { status, headers }));
    } else if (message.type === "chunk" && message.data && pending.controller) {
      const binary = atob(message.data), data = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i++) data[i] = binary.charCodeAt(i);
      pending.controller.enqueue(data);
    } else if (message.type === "error") {
      const error = new Error(message.error || "节点传输中断");
      if (pending.started) pending.controller?.error(error); else pending.reject(error);
      pending.cleanup?.();
      this.pending.delete(message.id);
    } else if (message.type === "end") {
      pending.controller?.close(); pending.cleanup?.(); this.pending.delete(message.id);
    }
  }

  private fail(error: Error) {
    for (const request of this.pending.values()) {
      request.cleanup?.();
      if (!request.started) request.reject(error); else request.controller?.error(error);
    }
    this.pending.clear();
  }

  async fetch(path: string, init: RequestInit = {}) {
    const channel = this.channel;
    if (!channel || channel.readyState !== "open") throw new Error("节点 WebRTC 通道未连接");
    const request = new Request(new URL(path, window.location.href), init);
    const id = crypto.randomUUID();
    const body = new Uint8Array(await request.arrayBuffer());
    const headers: Record<string, string> = {};
    request.headers.forEach((value, key) => { headers[key] = value; });
    const response = new Promise<Response>((resolve, reject) => {
      const entry: Pending = { resolve, reject, started: false };
      const signal = request.signal;
      entry.cleanup = () => signal.removeEventListener("abort", entry.abort!);
      entry.abort = () => {
        if (!this.pending.has(id)) return;
        this.pending.delete(id);
        const error = new DOMException("请求已取消", "AbortError");
        if (entry.started) entry.controller?.error(error); else entry.reject(error);
        void this.sendControl({ type: "request-cancel", id });
      };
      signal.addEventListener("abort", entry.abort, { once: true });
      this.pending.set(id, entry);
      if (signal.aborted) entry.abort();
    });
    const send = async (value: unknown) => {
      while (channel.bufferedAmount > 512 * 1024) {
        if (channel.readyState !== "open") throw new Error("节点 WebRTC 通道已关闭");
        await new Promise((resolve) => setTimeout(resolve, 8));
      }
      channel.send(JSON.stringify(value));
    };
    try {
      if (request.signal.aborted) return response;
      await send({ type: "request", id, method: request.method, path: new URL(request.url).pathname + new URL(request.url).search, headers });
      for (let offset = 0; offset < body.length; offset += 24 * 1024) {
        if (request.signal.aborted) return response;
        const chunk = body.subarray(offset, Math.min(offset + 24 * 1024, body.length));
        let binary = ""; for (const byte of chunk) binary += String.fromCharCode(byte);
        await send({ type: "request-chunk", id, data: btoa(binary) });
      }
      await send({ type: "request-end", id });
    } catch (error) { this.pending.get(id)?.cleanup?.(); this.pending.delete(id); throw error; }
    return response;
  }

  private async sendControl(value: unknown) {
    const channel = this.channel;
    if (channel?.readyState === "open") channel.send(JSON.stringify(value));
  }

  close() {
    const socket = this.socket;
    if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "close" }));
    socket?.close(); this.peer?.close(); this.channel?.close(); this.fail(new Error("节点连接已关闭"));
  }
}
