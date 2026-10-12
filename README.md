# Myliba

Myliba is a local-first personal resource library. Local mode indexes files in place, keeping originals where they are. Remote mode serves the same responsive web app from a self-hosted VPS and forwards authenticated API requests through an outbound connection from the home node.

## Local use

Build the Vue web app before starting the Go service. The production files are embedded into the executable.

```powershell
pnpm install
pnpm build
go run -tags sqlite_fts5 ./cmd/myliba
```

Open <http://127.0.0.1:4317>. The database is stored under the operating system's user config directory in `myliba/library.db`. SQLite uses CGO and the FTS5 build tag; Windows needs Go and a C compiler such as MinGW-w64.

For frontend development, run the local Go service and `pnpm dev` in separate terminals. Vite serves the Vue app at <http://127.0.0.1:5173> and proxies API calls to the local Go service.

## Remote web access

The same executable runs in two modes. The home node keeps its local HTTP service bound to `127.0.0.1:4317`; it only makes an outbound WebSocket connection to the hub. The hub hosts the embedded web app and proxies a restricted set of `/api/` requests to the node. File responses, including HTTP range requests for video previews, are forwarded in bounded chunks.

### 1. Build on the VPS

Install Go, a C compiler, C build tools, and pnpm. Build the Vue assets and executable on the VPS:

```sh
pnpm install --frozen-lockfile
pnpm build
go build -tags sqlite_fts5 -o myliba ./cmd/myliba
```

The hub needs outbound access to download the Go module dependencies on its first build.

### 2. Configure the hub behind HTTPS

Generate two separate secrets (each at least 32 random bytes), then set them in the hub service environment:

```sh
export MYLIBA_MODE=hub
export MYLIBA_WEB_PASSWORD='use-a-long-random-password-here'
export MYLIBA_NODE_TOKEN='use-a-different-long-random-secret-here'
export MYLIBA_HUB_LISTEN=127.0.0.1:4318
./myliba
```

`MYLIBA_WEB_PASSWORD` must contain at least 16 characters. `MYLIBA_NODE_TOKEN` must contain at least 32 characters and must match on the home node. The hub listens on loopback by default; place it behind a TLS reverse proxy. Example Caddy config (`deploy/Caddyfile.example`):

```caddy
library.example.com {
    reverse_proxy 127.0.0.1:4318
}
```

Caddy terminates HTTPS and proxies WebSocket upgrades automatically. Keep port 4318 private; expose only the HTTPS reverse proxy.

### 3. Connect the home node

On the computer that runs the local Myliba index, set the hub URL and the same node token before starting the app:

```powershell
$env:MYLIBA_HUB_URL = 'wss://library.example.com/node'
$env:MYLIBA_NODE_TOKEN = 'the-same-node-secret-configured-on-the-hub'
go run -tags sqlite_fts5 ./cmd/myliba
```

The home node still opens its local interface at <http://127.0.0.1:4317>. From a phone, open `https://library.example.com` and sign in with the web password. The hub page shows whether the home node is online. The remote page can search, tag, rescan existing indexed folders, and preview files while that node is online. The remote page does not let a phone add a new home filesystem path.

The hub and node token are shared secrets for this first self-hosted version; they are not yet individual paired device identities. Hub web sessions expire after 30 days and are held in memory, so restarting the hub requires users to sign in again. Use a unique, long web password, restrict VPS access to HTTPS, and keep the node token private. Do not set `MYLIBA_HUB_COOKIE_SECURE=0` outside local HTTP development.

All remote API and file traffic passes through the VPS in this version. TLS protects browser-to-hub and node-to-hub connections; the hub can read the proxied content. Direct device connections, end-to-end encryption from browser to home node, synced offline search, and VPS preview caching are not included.

## Implemented features

- Add and scan local folders without moving source files.
- Keep a stable local resource ID across same-volume file moves where the filesystem exposes a stable file ID.
- Track local versions when size or modification time changes.
- Search names, paths, and manual tags with SQLite FTS5 plus substring matching.
- Add tags one at a time or in batches, and use the unorganized queue to find resources without tags.
- Open resources in the browser and download the original file from each resource card; previews and downloads also work through the remote hub.
- Preview browser-supported image and video formats from the local file.
- Serve the responsive app remotely through a password-protected hub and an outbound node connection.

Generated thumbnails and transcodes, saved smart collections, cross-device metadata sync, individual device pairing and revocation, conflict resolution, and offline remote access remain future work. Current version change detection uses file size and modification time; BLAKE3 content verification is planned before remote transfer and deduplication.
