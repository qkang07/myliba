import type { TrafficRate } from "./api";

type SignalMessage = {
  type: "offer" | "answer" | "candidate" | "close" | "error";
  description?: RTCSessionDescriptionInit;
  candidate?: RTCIceCandidateInit;
};

type PendingResponse = {
  resolve: (response: Response) => void;
  reject: (error: Error) => void;
  controller?: ReadableStreamDefaultController<Uint8Array>;
  started: boolean;
};

let peer: RTCPeerConnection | undefined;
let channel: RTCDataChannel | undefined;
let signalSocket: WebSocket | undefined;
let pending = new Map<string, PendingResponse>();
let stateListener: ((state: "connecting" | "webrtc" | "turn" | "relay") => void) | undefined;
let rateListener: ((rate: TrafficRate) => void) | undefined;
let statsTimer: ReturnType<typeof setInterval> | undefined;
let offerSent = false;
let localCandidates: RTCIceCandidateInit[] = [];
let remoteCandidates: RTCIceCandidateInit[] = [];
let remoteDescriptionSet = false;

function emitState(state: "connecting" | "webrtc" | "turn" | "relay") {
  stateListener?.(state);
}

function sendSignal(signal: SignalMessage) {
  if (!signalSocket || signalSocket.readyState !== WebSocket.OPEN) return;
  signalSocket.send(JSON.stringify(signal));
}

function newRequestID() {
  return crypto.randomUUID();
}

async function sendData(channel: RTCDataChannel, message: unknown) {
  while (channel.bufferedAmount > 512 * 1024) {
    if (channel.readyState !== "open") throw new Error("WebRTC data channel is closed");
    await new Promise((resolve) => setTimeout(resolve, 8));
  }
  if (channel.readyState !== "open") throw new Error("WebRTC data channel is closed");
  channel.send(JSON.stringify(message));
}

async function waitForSocket(socket: WebSocket) {
  if (socket.readyState === WebSocket.OPEN) return;
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error("信令连接超时")), 8000);
    socket.addEventListener("open", () => { clearTimeout(timeout); resolve(); }, { once: true });
    socket.addEventListener("error", () => { clearTimeout(timeout); reject(new Error("无法连接 WebRTC 信令服务")); }, { once: true });
  });
}

function decodeBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

function handleDataMessage(raw: string) {
  let message: { type: string; id?: string; status?: number; headers?: Record<string, string[]>; data?: string; error?: string };
  try { message = JSON.parse(raw); } catch { return; }
  if (!message.id) return;
  const request = pending.get(message.id);
  if (!request) return;
  if (message.type === "start") {
    const headers = new Headers();
    for (const [name, values] of Object.entries(message.headers ?? {})) {
      for (const value of values) headers.append(name, value);
    }
    const status = message.status || 502;
    const bodyForbidden = status === 204 || status === 205 || status === 304;
    let stream: ReadableStream<Uint8Array> | null = null;
    if (!bodyForbidden) {
      stream = new ReadableStream<Uint8Array>({
        start(controller) { request.controller = controller; },
        cancel() { pending.delete(message.id!); },
      });
    }
    request.started = true;
    request.resolve(new Response(stream, { status, headers }));
    return;
  }
  if (message.type === "chunk" && message.data && request.controller) {
    request.controller.enqueue(decodeBytes(message.data));
    return;
  }
  if (message.type === "error") {
    const error = new Error(message.error || "WebRTC 文件传输中断");
    if (request.started) request.controller?.error(error);
    else request.reject(error);
    pending.delete(message.id);
    return;
  }
  if (message.type === "end") {
    request.controller?.close();
    pending.delete(message.id);
  }
}

function failPending(error: Error) {
  for (const request of pending.values()) {
    if (!request.started) request.reject(error);
    else request.controller?.error(error);
  }
  pending.clear();
}

function cleanup(sendClose: boolean) {
  if (statsTimer) clearInterval(statsTimer);
  statsTimer = undefined;
  if (sendClose) sendSignal({ type: "close" });
  signalSocket?.close();
  signalSocket = undefined;
  channel?.close();
  channel = undefined;
  peer?.close();
  peer = undefined;
  offerSent = false;
  localCandidates = [];
  remoteCandidates = [];
  remoteDescriptionSet = false;
  failPending(new Error("WebRTC 连接已断开"));
}

function watchRates(connection: RTCPeerConnection, dataChannel: RTCDataChannel) {
  let lastBytesSent = 0;
  let lastBytesReceived = 0;
  let lastSample = 0;
  const sample = async () => {
    if (connection !== peer || dataChannel.readyState !== "open") return;
    try {
      const report = await connection.getStats();
      let bytesSent = 0;
      let bytesReceived = 0;
      let usingTurn = false;
      report.forEach((stat) => {
        if (stat.type === "data-channel") {
          const counters = stat as RTCStats & { bytesSent?: number; bytesReceived?: number };
          bytesSent = Number(counters.bytesSent ?? bytesSent);
          bytesReceived = Number(counters.bytesReceived ?? bytesReceived);
        }
        if (stat.type === "candidate-pair") {
          const pair = stat as RTCStats & { nominated?: boolean; state?: string; localCandidateId?: string; remoteCandidateId?: string };
          if (pair.nominated && pair.state === "succeeded" && pair.localCandidateId && pair.remoteCandidateId) {
            const local = report.get(pair.localCandidateId) as (RTCStats & { candidateType?: string }) | undefined;
            const remote = report.get(pair.remoteCandidateId) as (RTCStats & { candidateType?: string }) | undefined;
            usingTurn = local?.candidateType === "relay" || remote?.candidateType === "relay";
          }
        }
      });
      emitState(usingTurn ? "turn" : "webrtc");
      const now = performance.now();
      if (lastSample && now > lastSample) {
        const seconds = (now - lastSample) / 1000;
        rateListener?.({
          downloadBps: Math.max(0, Math.round((bytesReceived - lastBytesReceived) / seconds)),
          uploadBps: Math.max(0, Math.round((bytesSent - lastBytesSent) / seconds)),
        });
      }
      lastBytesSent = bytesSent;
      lastBytesReceived = bytesReceived;
      lastSample = now;
    } catch { /* The browser may not expose data-channel counters. */ }
  };
  void sample();
  statsTimer = setInterval(() => void sample(), 2000);
}

export async function connectWebRTC(
  iceServers: RTCIceServer[],
  onState: (state: "connecting" | "webrtc" | "turn" | "relay") => void,
  onRate: (rate: TrafficRate) => void,
  share?: { id: string; token: string },
) {
  cleanup(false);
  stateListener = onState;
  rateListener = onRate;
  emitState("connecting");
  try {
    // Content is not allowed through relay candidates, including TURN.
    const directServers = iceServers.flatMap((server) => {
      const urls = (Array.isArray(server.urls) ? server.urls : [server.urls]).filter(
        (url) => typeof url === "string" && !/^(turn|turns):/i.test(url),
      );
      return urls.length ? [{ ...server, urls }] : [];
    });
    const rtc = new RTCPeerConnection({ iceServers: directServers });
    peer = rtc;
    const socketURL = new URL(share ? "/share/signal" : "/signal", window.location.href);
    socketURL.protocol = socketURL.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(socketURL);
    signalSocket = socket;
    await waitForSocket(socket);
    if (share) {
      socket.send(JSON.stringify({ type: "authorize", id: share.id, token: share.token }));
      await new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => { cleanup(false); reject(new Error("分享授权超时")); }, 10000);
        const onMessage = (event: MessageEvent) => {
          try {
            if (JSON.parse(String(event.data)).type === "authorized") {
              clearTimeout(timeout);
              socket.removeEventListener("message", onMessage);
              resolve();
            }
          } catch { /* Ignore malformed signaling messages. */ }
        };
        socket.addEventListener("message", onMessage);
        socket.addEventListener("close", () => { clearTimeout(timeout); reject(new Error("分享授权失败")); }, { once: true });
      });
    }

    const dataChannel = rtc.createDataChannel("myliba-api", { ordered: true });
    channel = dataChannel;
    dataChannel.onmessage = (event) => {
      if (typeof event.data === "string") handleDataMessage(event.data);
    };
    dataChannel.onopen = () => {
      if (peer === rtc && channel === dataChannel) {
        emitState("webrtc");
        watchRates(rtc, dataChannel);
      }
    };
    dataChannel.onclose = () => {
      if (channel === dataChannel) {
        cleanup(false);
        emitState("relay");
      }
    };
    rtc.onicecandidate = (event) => {
      if (!event.candidate) return;
      const candidate = event.candidate.toJSON();
      if (!offerSent) localCandidates.push(candidate);
      else sendSignal({ type: "candidate", candidate });
    };
    rtc.onconnectionstatechange = () => {
      if (rtc.connectionState === "failed" || rtc.connectionState === "closed") {
        if (peer === rtc) {
          cleanup(true);
          emitState("relay");
        }
      }
    };
    socket.onmessage = (event) => {
      let signal: SignalMessage;
      try { signal = JSON.parse(String(event.data)); } catch { return; }
      if (signal.type === "answer" && signal.description) {
        void rtc.setRemoteDescription(signal.description).then(async () => {
          remoteDescriptionSet = true;
          const candidates = remoteCandidates;
          remoteCandidates = [];
          for (const candidate of candidates) await rtc.addIceCandidate(candidate).catch(() => undefined);
        }).catch(() => {
          cleanup(true);
          emitState("relay");
        });
      } else if (signal.type === "candidate" && signal.candidate) {
        if (!remoteDescriptionSet) remoteCandidates.push(signal.candidate);
        else void rtc.addIceCandidate(signal.candidate).catch(() => undefined);
      } else if (signal.type === "error") {
        cleanup(true);
        emitState("relay");
      }
    };
    socket.onerror = () => {
      if (peer === rtc) {
        cleanup(true);
        emitState("relay");
      }
    };
    socket.onclose = () => {
      if (peer === rtc && dataChannel.readyState !== "open") {
        cleanup(false);
        emitState("relay");
      }
    };

    const offer = await rtc.createOffer();
    await rtc.setLocalDescription(offer);
    sendSignal({ type: "offer", description: rtc.localDescription?.toJSON() ?? offer });
    offerSent = true;
    for (const candidate of localCandidates) sendSignal({ type: "candidate", candidate });
    localCandidates = [];

    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("WebRTC 连接超时")), 12000);
      const check = () => {
        if (dataChannel.readyState === "open") { clearTimeout(timeout); resolve(); }
        else if (dataChannel.readyState === "closed" || rtc.connectionState === "failed") { clearTimeout(timeout); reject(new Error("WebRTC 连接失败")); }
        else setTimeout(check, 100);
      };
      check();
    });
    emitState("webrtc");
  } catch {
    cleanup(true);
    emitState("relay");
  }
}

export function closeWebRTC() {
  cleanup(true);
  emitState("relay");
}

export function webRTCConnected() {
  return channel?.readyState === "open";
}

export async function webRTCFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const activeChannel = channel;
  if (!activeChannel || activeChannel.readyState !== "open") throw new Error("WebRTC 数据通道未连接");
  const request = new Request(input, init);
  const id = newRequestID();
  const body = new Uint8Array(await request.arrayBuffer());
  const headers: Record<string, string> = {};
  request.headers.forEach((value, key) => { headers[key] = value; });
  const response = new Promise<Response>((resolve, reject) => pending.set(id, { resolve, reject, started: false }));
  try {
    await sendData(activeChannel, { type: "request", id, method: request.method, path: `${new URL(request.url).pathname}${new URL(request.url).search}`, headers });
    for (let offset = 0; offset < body.length; offset += 24 * 1024) {
      const data = body.subarray(offset, Math.min(offset + 24 * 1024, body.length));
      let binary = "";
      for (const byte of data) binary += String.fromCharCode(byte);
      await sendData(activeChannel, { type: "request-chunk", id, data: btoa(binary) });
    }
    await sendData(activeChannel, { type: "request-end", id });
  } catch (error) {
    pending.delete(id);
    throw error;
  }
  return response;
}
