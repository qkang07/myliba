import { createWebRTCAnswer, getWebRTCConfig } from "./api";

export interface TransferredFile {
  blob: Blob;
  name: string;
}

export const maxDirectFileSize = 256 * 1024 * 1024;

interface TransferControl {
  type: "start" | "done" | "error";
  name?: string;
  contentType?: string;
  size?: number;
  message?: string;
}

function waitForIceGathering(peer: RTCPeerConnection, timeoutMs: number) {
  if (peer.iceGatheringState === "complete") return Promise.resolve();
  return new Promise<void>((resolve, reject) => {
    const finish = () => {
      clearTimeout(timer);
      peer.removeEventListener("icegatheringstatechange", changed);
      peer.iceGatheringState === "complete" ? resolve() : reject(new Error("ICE candidate gathering timed out"));
    };
    const changed = () => {
      if (peer.iceGatheringState === "complete") finish();
    };
    const timer = setTimeout(() => {
      peer.removeEventListener("icegatheringstatechange", changed);
      reject(new Error("ICE candidate gathering timed out"));
    }, timeoutMs);
    peer.addEventListener("icegatheringstatechange", changed);
  });
}

function waitForDataChannel(channel: RTCDataChannel, peer: RTCPeerConnection, timeoutMs: number) {
  if (channel.readyState === "open") return Promise.resolve();
  return new Promise<void>((resolve, reject) => {
    const cleanup = () => {
      clearTimeout(timer);
      channel.removeEventListener("open", opened);
      peer.removeEventListener("connectionstatechange", stateChanged);
    };
    const opened = () => {
      cleanup();
      resolve();
    };
    const stateChanged = () => {
      if (peer.connectionState === "failed" || peer.connectionState === "closed") {
        cleanup();
        reject(new Error("WebRTC connection failed"));
      }
    };
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error("WebRTC connection timed out"));
    }, timeoutMs);
    channel.addEventListener("open", opened, { once: true });
    peer.addEventListener("connectionstatechange", stateChanged);
  });
}

export async function transferFileDirectly(resourceId: string): Promise<TransferredFile> {
  const config = await getWebRTCConfig();
  const peer = new RTCPeerConnection({ iceServers: config.iceServers });
  const channel = peer.createDataChannel("myliba-file", { ordered: true });
  channel.binaryType = "arraybuffer";

  try {
    const offer = await peer.createOffer();
    await peer.setLocalDescription(offer);
    await waitForIceGathering(peer, 10_000);

    const answer = await createWebRTCAnswer(peer.localDescription?.sdp || "", resourceId);
    await peer.setRemoteDescription(answer);
    await waitForDataChannel(channel, peer, 15_000);

    return await new Promise<TransferredFile>((resolve, reject) => {
      const chunks: BlobPart[] = [];
      let metadata: TransferControl | undefined;
      let receivedBytes = 0;
      const timer = setTimeout(() => reject(new Error("WebRTC file transfer timed out")), 30 * 60_000);

      const finish = (error?: Error, result?: TransferredFile) => {
        clearTimeout(timer);
        channel.removeEventListener("message", onMessage);
        channel.removeEventListener("error", onError);
        channel.removeEventListener("close", onClose);
        if (error) reject(error);
        else if (result) resolve(result);
      };

      const onError = () => finish(new Error("WebRTC data channel failed"));
      const onClose = () => finish(new Error("WebRTC data channel closed before transfer completed"));
      const onMessage = (event: MessageEvent) => {
        if (typeof event.data === "string") {
          let message: TransferControl;
          try {
            message = JSON.parse(event.data) as TransferControl;
          } catch {
            finish(new Error("Invalid WebRTC file response"));
            return;
          }
          if (message.type === "error") {
            finish(new Error(message.message || "File transfer failed"));
          } else if (message.type === "start") {
            if (!Number.isFinite(message.size) || (message.size ?? 0) > maxDirectFileSize) {
              finish(new Error("File is too large for browser direct transfer"));
              return;
            }
            metadata = message;
          } else if (message.type === "done") {
            if (!metadata || receivedBytes !== metadata.size) {
              finish(new Error("WebRTC file transfer was incomplete"));
              return;
            }
            finish(undefined, {
              blob: new Blob(chunks, { type: metadata.contentType || "application/octet-stream" }),
              name: metadata.name || resourceId,
            });
          }
          return;
        }

        if (!metadata || !(event.data instanceof ArrayBuffer)) {
          finish(new Error("Unexpected WebRTC file data"));
          return;
        }
        receivedBytes += event.data.byteLength;
        if (receivedBytes > maxDirectFileSize || receivedBytes > (metadata.size ?? 0)) {
          finish(new Error("WebRTC file size exceeded the expected limit"));
          return;
        }
        chunks.push(event.data);
      };

      channel.addEventListener("message", onMessage);
      channel.addEventListener("error", onError);
      channel.addEventListener("close", onClose);
      channel.send(JSON.stringify({ type: "get", resourceId }));
    });
  } finally {
    channel.close();
    peer.close();
  }
}
