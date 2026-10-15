# Myliba

Myliba is a local-first personal resource library. Local mode indexes files in place, keeping originals where they are. Remote mode serves the same responsive web app from a self-hosted VPS and forwards authenticated API requests through an outbound connection from the home node.

An Android first version lives in [`android/`](android/README.md). It indexes folders chosen on the phone and connects to an existing HTTPS Hub.

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

The same executable runs in two modes. The home node keeps its local HTTP service bound to `127.0.0.1:4317`; it maintains an outbound WebSocket connection to the hub for signaling and fallback relay. After login, the browser negotiates a WebRTC DataChannel with the home node. API requests and resource data use that direct encrypted channel when available; requests fall back to the existing hub relay when the channel is unavailable. The hub serves the app, authenticates the browser, and relays signaling and fallback traffic.

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

The home node still opens its local interface at <http://127.0.0.1:4317>. From a phone, open `https://library.example.com` and sign in with the web password. The hub page shows whether the home node is online. The remote page can search, tag, organize virtual folders, rescan existing indexed folders, and open or download files while that node is online. Set `MYLIBA_ICE_SERVERS` to the same JSON array of ICE server objects on the hub and home node; when unset, Myliba uses `stun:stun.l.google.com:19302`. Configure TURN on both processes when direct ICE connectivity is blocked. TURN credentials are sent to authenticated browser sessions, so use short-lived credentials. The remote page does not let a phone add a new home filesystem path.

The hub requires a web session for every remote API call, and both the hub and home node enforce the same remote API allowlist. The WebRTC DataChannel is created for an authenticated signaling session and can carry only allowlisted API requests. Adding a new index root and browsing the home filesystem remain local-only operations. Browser-active file types such as HTML, SVG, and JavaScript are downloaded rather than opened inside the authenticated app origin.

The hub and node token are shared secrets for this first self-hosted version; they are not yet individual paired device identities. Hub web sessions expire after 30 days and are held in memory, so restarting the hub requires users to sign in again. Use a unique, long web password, restrict VPS access to HTTPS, and keep the node token private. Do not set `MYLIBA_HUB_COOKIE_SECURE=0` outside local HTTP development.

After the web session is authenticated, the browser negotiates a WebRTC DataChannel directly with the home node through the hub's signaling WebSocket. API calls, thumbnails, file previews, and downloads use that encrypted channel when it opens; until then, or after it disconnects, requests use the existing WSS hub relay. The page reports which transport is active and its current data rate. The hub still serves the page, authenticates the browser, and relays signaling; it does not receive the content of a working WebRTC DataChannel.

The hub and home node use the same optional `MYLIBA_ICE_SERVERS` JSON array. Without it, Myliba uses `stun:stun.l.google.com:19302`. Configure a TURN server on both processes when either network blocks direct ICE connectivity. For example, set `MYLIBA_ICE_SERVERS='[{"urls":["stun:stun.example.net:3478"]},{"urls":["turn:turn.example.net:3478"],"username":"temporary-user","credential":"temporary-password"}]'`. TURN credentials are sent to authenticated browser sessions, so use short-lived credentials. If ICE cannot establish a DataChannel, the app stays usable through the hub relay. Relayed traffic passes through the VPS; synced offline search and VPS preview caching are not included.

## Implemented features

- Add and scan local folders without moving source files.
- Keep a stable local resource ID across same-volume file moves where the filesystem exposes a stable file ID.
- Track local versions when size or modification time changes.
- Search names, paths, and manual tags with SQLite FTS5 plus substring matching. Filter by derived file attribute tags for type, extension, and size range; values within one attribute category are alternatives, while categories and manual tags combine.
- Group indexed resources in virtual top-level folders without moving source files. Existing and newly indexed resources start in “Unfiled”; create, rename, delete, and move resources between folders from the web app. Each folder shows only its own tags, and tags support multiple selection.
- Add tags one at a time or in batches, and use the unorganized queue to find resources without tags.
- Select all resources matching the current folder, search, and tag filters across every result page, then add a tag, move them to a virtual folder, or generate media thumbnails in batches.
- Save smart collections from a search and filters (multiple tags, virtual folder, file type, indexed folder, unorganized state, and sort order). Collections are stored on the home node and update as its index and tags change; the same collections appear through local and remote web access.
- Open a resource detail page from its card or a direct `?resource=<id>` link. The page shows file metadata, a type-specific preview, original-file download, and add/remove/suggested tags; it also works through the remote hub.
- Generate and cache image thumbnails in small (240 × 160), medium (480 × 320), and large (1280 × 960) sizes from the detail page. The resource list still uses the medium thumbnail and does not generate thumbnails merely by loading the list.
- For videos, the detail page divides the duration into six segments and can extract a screenshot at each segment midpoint. The screenshots are cached on the home node. FFmpeg and FFprobe on the node's `PATH` support extraction and duration detection; browsers that can read the video can also supply the duration.
- Serve the responsive app remotely through a password-protected hub and an outbound node connection.
- Prefer direct browser-to-node WebRTC transfers for remote downloads and non-video file opening, with hub relay fallback.

Video thumbnails and segment screenshots require FFmpeg on the home node. Users can generate thumbnails individually or select several resources for a controlled two-at-a-time batch. Without FFmpeg, JPEG, PNG, and GIF images still get thumbnails using Go standard image decoders. Generated JPEG thumbnails are cached under the Myliba config directory and refreshed when file size or modification time changes. Transcodes, cross-device metadata sync, individual device pairing and revocation, conflict resolution, and offline remote access remain future work. Current version change detection uses file size and modification time; BLAKE3 content verification is planned before remote transfer and deduplication.
