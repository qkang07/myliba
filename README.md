# Myliba

Myliba is a local-first personal resource library. Local mode indexes files in place, keeping originals where they are. Remote mode serves the same responsive web app from a self-hosted VPS and forwards authenticated API requests through an outbound connection from the home node.

The native Android app lives in [`android/`](android/README.md). It indexes folders chosen on the phone, connects to an existing HTTPS Hub, and can register its indexed library as a resource node.

## Local use

Build the Vue web app before starting the Go service. The production files are embedded into the executable.
The generated `cmd/myliba/web/dist/` directory is ignored by Git. On a fresh checkout, run `pnpm build:web` before invoking `go build`, `go run`, or `go test` directly for `cmd/myliba`.

```powershell
pnpm install
pnpm build
go run -tags sqlite_fts5 ./cmd/myliba
```

Open <http://127.0.0.1:4317>. The database is stored under the operating system's user config directory in `myliba/library.db`. SQLite uses CGO and the FTS5 build tag; Windows needs Go and a C compiler such as MinGW-w64.

For frontend development, run the local Go service and `pnpm dev` in separate terminals. Vite serves the Vue app at <http://127.0.0.1:5173> and proxies API calls to the local Go service.

### Windows portable build with video support

On the build computer, get a Windows FFmpeg build that includes `ffmpeg.exe`, `ffprobe.exe`, `LICENSE`, and `README.txt`. The [Essentials build](https://www.gyan.dev/ffmpeg/builds/) is sufficient for Myliba and makes a smaller package. Then run:

```powershell
.\scripts\package-windows.ps1 -CreateZip
```

After `pnpm install --frozen-lockfile`, the script builds the web app and Go service, and writes a portable folder under `.runtime/releases/`. It places FFmpeg and FFprobe in the `ffmpeg/` folder beside `myliba.exe`, so the target computer needs no separate FFmpeg installation or `PATH` change. The script prefers an Essentials build extracted under `.runtime/ffmpeg-essentials/`; pass `-FFmpegBinDir <path>` to select another build; use `-SkipWebBuild` only when `cmd/myliba/web/dist` is already current. Keep the `ffmpeg/` folder with `myliba.exe` when copying the build. The included FFmpeg license and build information are in `ffmpeg/LICENSE` and `ffmpeg/README.txt`; review the [FFmpeg distribution guidance](https://ffmpeg.org/legal.html) before publishing a package.

## Remote web access

The same executable runs as a local client, a Hub, or a storage-only cloud node. The home client keeps its local HTTP service bound to `127.0.0.1:4317`; it maintains an outbound WebSocket connection to the Hub for signaling and metadata requests. Multiple desktop and Android resource nodes can register with the same Hub. Each signed-in web or app session selects a resource node. File bytes and generated media require a direct WebRTC DataChannel; they never fall back to the Hub WebSocket. Metadata requests can still use the Hub relay.

### 1. Build on the VPS

Install Go, a C compiler, C build tools, and pnpm. Build the Vue assets and executable on the VPS:

```sh
pnpm install --frozen-lockfile
pnpm build
```

The hub needs outbound access to download the Go module dependencies on its first build.

### 2. Configure the hub behind HTTPS

For a Chinese deployment, login and migration guide, see [认证与授权操作说明](docs/auth-operations.md).

Set one owner password on the Hub. A new installation pairs each phone or computer with that password once and gives it its own device credential:

```sh
export MYLIBA_MODE=hub
export MYLIBA_WEB_PASSWORD='use-a-long-random-password-here'
export MYLIBA_HUB_LISTEN=127.0.0.1:4318
./myliba
```

`MYLIBA_WEB_PASSWORD` must contain at least 16 characters. Existing installations can keep `MYLIBA_NODE_TOKEN` (at least 32 characters) while old clients migrate. The hub listens on loopback by default; place it behind the existing HTTPS gateway. Proxy `/`, `/api/`, `/node`, and `/signal` to the Hub address without rewriting paths. Preserve the `Host` header and WebSocket upgrades on `/node` and `/signal`, allow long-lived WebSocket connections, and avoid caching login and API responses. Keep the Hub port private and match `MYLIBA_HUB_LISTEN` to the address and port used by the gateway.

### 3. Connect the home node

On the computer that runs the local Myliba index, start the app and open <http://127.0.0.1:4317>. Click **服务端** in the top bar, add the Hub's `wss://.../node` address, and enter the owner password once. The Hub issues a credential for this computer; the password is not saved locally. The local app saves named Hub profiles in its local database. You can switch or disconnect at any time without restarting; only one Hub is connected at a time. Existing `MYLIBA_HUB_URL` and `MYLIBA_NODE_TOKEN` variables are imported once when the local database has no Hub profiles, for compatibility with older startup scripts.

```powershell
go run -tags sqlite_fts5 ./cmd/myliba
```

The home node still opens its local interface at <http://127.0.0.1:4317>. From a phone, open `https://library.example.com` and sign in with the web password. The remote page lists registered resource nodes; select one to search, tag, organize, preview, and download its indexed files. The Android app has the same selector under **家中 → 来源**. Remote clients try a WebRTC DataChannel first, including IPv6 when both networks provide it. When WebRTC cannot connect, authenticated resource content uses the Hub's rate-limited HTTP/WebSocket proxy. Set `MYLIBA_ICE_SERVERS` on the Hub to change the default `stun:stun.l.google.com:19302`; if a TURN server is configured, its relay candidates can also be selected by ICE. Remote clients cannot add an index root to a node.

To expose the phone library, first connect **家中资源库** in the native Android app with the owner password, then index folders under **本机** and start **手机资源节点**. The phone uses its paired device credential for both browsing and serving, so the node does not need another password. The Hub lists the phone as a separate resource node. Hub web sessions and native app sessions can select it and access its indexed library. This resource node is separate from the encrypted cloud storage node. The phone needs a usable WebRTC route to the viewer; Hub registration alone does not confirm media connectivity. The phone's web video preview currently loads an original file of at most 256 MB before playback; larger videos can be downloaded or streamed with ranges in the native app. Public sharing and desktop video variants are not available for phone resources yet.

The hub requires a web session for every remote API call, and both the hub and home node enforce the same remote API allowlist. The WebRTC DataChannel is created for an authenticated signaling session and can carry only allowlisted API requests. Adding a new index root and browsing the home filesystem remain local-only operations. Browser-active file types such as HTML, SVG, and JavaScript are downloaded rather than opened inside the authenticated app origin.

Open **设备授权** on the Hub web page to see paired phones and computers, revoke one device, or disable the old shared node token after all old clients have migrated. Revoking a device terminates its browser sessions and resource-node connection. Hub sessions expire after 30 days and survive Hub restarts. The Hub stores only hashes of session and device tokens in `MYLIBA_HUB_DATA_DIR/auth.json` (default: the Hub user config directory); keep this file private and backed up. Keep `MYLIBA_WEB_PASSWORD` unchanged for this owner account: a different password stops Hub startup. Do not set `MYLIBA_HUB_COOKIE_SECURE=0` outside local HTTP development.

After authentication, the browser negotiates a WebRTC DataChannel through the Hub's signaling WebSocket. File content, thumbnails, previews, and downloads use that channel when it connects. If it fails, the browser and Android app can fetch authenticated content through the Hub. The Hub limits this fallback to 2 MiB/s of response payload across all sessions and eight concurrent content streams. Set `MYLIBA_HUB_RELAY_BYTES_PER_SECOND` to change the global byte-per-second limit; the Hub rejects invalid values at startup. Public share links still require a WebRTC path.

The Hub and home client use the same optional `MYLIBA_ICE_SERVERS` JSON array. For example, set `MYLIBA_ICE_SERVERS='[{"urls":["stun:stun.example.net:3478"]}]'`. On a Go resource node with public IPv4, set `MYLIBA_RESOURCE_UDP_PORT_RANGE=40000-40031` in the node process and forward UDP 40000–40031 from the router to that node. This only helps when the router itself has a reachable public IPv4; carrier NAT upstream cannot be configured by the home router. Open the same UDP range in the node host firewall. Android WebRTC resource nodes do not currently expose a fixed UDP port range. IPv6 needs routable addresses on both ends and a firewall path for ICE UDP. The app diagnostics report address families without logging addresses.

### Single-file public shares

In a resource detail page, create a share link with a 1, 7, or 30-day expiry. Anyone holding the link can open its public Hub page and download that file while the home node is online and WebRTC direct connectivity succeeds. The URL fragment contains a random secret; it is shown only when the link is created and is not sent in the initial HTTP request. The Hub asks the home node to validate it before signaling, and the node checks it again for every file request. Resource paths, the full-library login, and device credentials are never included in the share response. The share list in the resource detail page can revoke a link, after which new file requests fail. If the source file changes, create a new link. This first version does not serve share bytes through the Hub or offline cloud replicas.

The HTTPS gateway must forward `/s/`, `/share/signal`, and `/api/shares/` to the Hub alongside the existing `/`, `/api/`, `/node`, and `/signal` routes. Keep WebSocket upgrades enabled for `/share/signal`. The browser and a future native client can use the same redeem and signaling protocol; the current recipient interface is the Hub web page.

## Encrypted cloud layer

The [global index and virtual node design](docs/cloud-index-and-virtual-nodes.md) defines OpenList as a separately identified virtual node. File and block indexes refer to node IDs and public access methods; access credentials live outside the index, locally on authorized nodes or at the Hub for on-demand lookup. The gateway configuration documented below is the current implementation and has not yet been migrated to that model.

The cloud layer stores each indexed file as immutable 4 MiB plaintext chunks encrypted locally with a fresh AES-256-GCM key for that file revision. Ciphertext hashes address blocks; an encrypted manifest records the filename, key, order, sizes, and optional user-visible history. The client keeps a local block copy and tries to place each block on up to two additional online nodes. Replication is best-effort: one node is enough to upload, and the Cloud panel shows the minimum online replica count per file. Each node keeps opaque catalog records and advertises its actual block inventory. The Hub also keeps the encrypted catalog, but never stores file blocks.

A local client can start as a **one-node cloud** without a Hub, cloud token, public URL, or another storage node. It encrypts and stores blocks, catalogs, and its recovery key in `MYLIBA_CLOUD_DATA_DIR` (or its default cloud data directory), then reads them locally. The Cloud panel reports one of three target replicas and warns about the missing backups without blocking uploads or downloads. Keep a separate backup of this directory and the recovery key while the client is the only node. Adding peers later enables replication; no separate cloud initialization is required.

The Hub's signed-in page has a **节点管理** button. It lists registered client and storage nodes, online/offline status, last heartbeat, direct URL, backend type, block replicas, and catalog counts. Disk nodes report encrypted block bytes and filesystem capacity/free space; Android storage nodes report app-private volume capacity/free space. S3/OSS and WebDAV gateways report block counts but currently have no reliable quota or byte usage, so those fields appear as unknown. The overview sums reports from online nodes: replicated blocks count more than once, and capacity can count the same underlying volume twice if multiple nodes share it. Offline nodes remain visible across Hub restarts, while peer discovery excludes them. The dashboard uses the normal Hub login session.

Cloud access uses each device's credential issued after entering the owner password once. New deployments do not need `MYLIBA_CLOUD_TOKEN` or `MYLIBA_NODE_TOKEN`. The Hub stores encrypted catalog records under `MYLIBA_HUB_DATA_DIR/cloud`, defaulting to its user config directory. Back up the recovery key shown in the local Cloud panel; a new client can import it to decrypt catalog records restored from the Hub or storage peers.

To run a storage-only node, use a separate copy of the executable:

```sh
MYLIBA_MODE=storage \
MYLIBA_HUB_URL=wss://hub.example.com/node \
MYLIBA_OWNER_PASSWORD='the-same-owner-password-used-by-the-hub' \
MYLIBA_CLOUD_DATA_DIR=/srv/myliba/storage-data \
MYLIBA_CLOUD_LISTEN=127.0.0.1:4319 \
MYLIBA_CLOUD_PUBLIC_URL=https://storage-a.example.com \
./myliba
```

The storage node uses `MYLIBA_OWNER_PASSWORD` only for first pairing; remove it from the service environment after a successful start. The issued device credential is saved in `MYLIBA_CLOUD_DATA_DIR/device-credential`. The node exposes `/cloud/blocks/*`, `/cloud/catalog/*`, and `/cloud/inventory` through its **own** HTTPS gateway. Route those paths directly to that node's `MYLIBA_CLOUD_LISTEN` address, not through the Hub. Other nodes transfer encrypted blocks directly to this URL using one-use grants issued by the Hub; long-lived device credentials are not sent to peers. A local client may also set `MYLIBA_CLOUD_PUBLIC_URL` to be an online source for other clients; without one it can still upload to reachable storage nodes. Direct endpoints must use HTTPS outside loopback development. Each direct request is authorized with the Hub, so cloud peers cannot exchange blocks while the Hub is unavailable.

An Android storage node can run without a public HTTPS address or storage relay: configure the Hub HTTPS URL, authorize the phone with the owner password once, and leave both optional addresses blank. It registers a `p2p://<node-id>` endpoint. The local Go client asks the Hub to exchange WebRTC SDP, then sends encrypted block requests over a direct DataChannel to the phone. The Hub handles signaling and encrypted catalog records but never DataChannel block bytes. `MYLIBA_ICE_SERVERS` on the Hub supplies STUN URLs for this path; TURN URLs are excluded. Some NAT or firewall combinations cannot establish a direct route; the client then continues with available replicas and warns about missing copies. This path currently supports Go client to Android storage node; it does not turn a private Go node into a WebRTC responder.

For networks where direct WebRTC fails, a private Go or Android node can use a **public storage node** as a relay. On that public storage node set `MYLIBA_CLOUD_RELAY_ENABLED=1` and expose its `/cloud/tunnel/*` and `/cloud/relay/*` paths through the same HTTPS gateway as the direct cloud paths. On the private Go node set `MYLIBA_CLOUD_RELAY_URL=https://storage-a.example.com` and leave `MYLIBA_CLOUD_PUBLIC_URL` empty. It opens an outbound authenticated WebSocket tunnel and advertises `https://storage-a.example.com/cloud/relay/<node-id>` to the Hub. The Hub still carries only node registration and opaque catalog records; encrypted block bytes flow through the public **storage** node. On Android, enter the relay address in the storage-node settings instead of a phone direct URL. If a public URL is configured, the direct endpoint remains preferred. Both relay and private node use their own authorized device credentials. A relay outage makes the private node temporarily unreachable; its local blocks and index remain intact.

### Storage node platforms and virtual backends

The independent storage binary contains no SQLite or web UI. Build it on Windows with `./scripts/build-storage.ps1 -TargetOS windows -TargetArch amd64`; use `darwin` or `linux` and `arm64` for other targets. On macOS/Linux, run `sh scripts/build-storage.sh linux amd64` (or `darwin arm64`). Output is under `.runtime/storage-build/<os>-<arch>/`. The binary runs with the storage environment variables above; set `MYLIBA_CLOUD_DATA_DIR` to a persistent directory.

The **same Android APK** can be a normal library client or a storage-only node. Its default backend uses **the phone's own app-private storage**: the phone registers with the Hub as a `storage` node, receives encrypted blocks and opaque catalog records from cloud peers, advertises its inventory, and serves stored blocks back to peers. Under **本机 → 来源 → 云层存储节点**, keep **本手机存储（云层节点）** selected, configure the Hub HTTPS URL, and enter the owner password once if the phone is not yet authorized. The phone HTTPS and storage relay addresses are optional; leave both blank for Hub-assisted direct WebRTC. Tap **切换为仅存储模式** to show the storage dashboard without the library navigation; **切换到完整客户端** returns to the normal UI. The foreground storage service continues after closing the screen. **OpenList 云盘** is an optional alternative storage backend for that Android node. If selected, provide your separately running OpenList HTTPS URL, a dedicated writable mount path, and an OpenList account with WebDAV read/manage/upload permissions; set the mount's WebDAV policy to **native proxy**. The APK never holds the cloud-drive provider's own credentials. The issued device credential and OpenList password are encrypted with Android Keystore in app-private preferences.

The Android node accepts the same direct `/cloud` protocol on port 4319. With only a relay address, its local server binds to loopback and makes an outbound TLS tunnel; with a direct URL, provide an HTTPS gateway to the phone's port 4319. OpenList quota and provider limits still apply. Android background execution, device reachability, and OpenList provider behavior need real-device verification.

A virtual storage node runs that same binary as a gateway with `MYLIBA_CLOUD_BACKEND=webdav`, `s3`, or `openlist`. The gateway holds its issued device credential; the backing account only receives encrypted blocks and opaque catalog records. Keep the gateway online for peer access and set its `MYLIBA_CLOUD_PUBLIC_URL` to the gateway's own HTTPS URL, or connect it to a public storage relay.

For WebDAV cloud drives (including services that expose the standard WebDAV interface), set `MYLIBA_WEBDAV_URL` to a dedicated collection, plus `MYLIBA_WEBDAV_USER` and `MYLIBA_WEBDAV_PASSWORD`. The gateway creates `blocks/` and `catalog/` below that collection. For S3-compatible object storage, set `MYLIBA_S3_ENDPOINT`, `MYLIBA_S3_BUCKET`, `MYLIBA_S3_REGION`, `MYLIBA_S3_ACCESS_KEY_ID`, and `MYLIBA_S3_SECRET_ACCESS_KEY`; optional `MYLIBA_S3_PREFIX` and `MYLIBA_S3_SESSION_TOKEN` are supported. The adapter signs requests with AWS Signature V4 and uses path-style addressing by default. Set `MYLIBA_S3_ADDRESSING=virtual` for virtual-hosted addressing, required by Alibaba Cloud OSS's S3-compatible endpoint. Use a dedicated bucket or prefix.

For Aliyun Drive, Baidu Netdisk, Quark, OneDrive, and other writable providers supported by OpenList, mount the provider in your **own OpenList instance** first. Then set `MYLIBA_CLOUD_BACKEND=openlist`, `MYLIBA_OPENLIST_URL=https://openlist.example.com`, `MYLIBA_OPENLIST_MOUNT=your-mount`, `MYLIBA_OPENLIST_USER`, and `MYLIBA_OPENLIST_PASSWORD` on a Myliba storage gateway. The Myliba gateway accesses OpenList's authenticated `/dav/<mount>/` endpoint; OpenList handles each provider's authorization and API. Give that OpenList user WebDAV read/manage and upload/mkdir permissions, and select a writable driver. Configure OpenList's WebDAV policy as **native proxy** so object downloads return bytes from OpenList instead of a redirect; Myliba rejects redirects to avoid leaking credentials. Each gateway targets one mount; run another gateway for another cloud-drive account or provider. This does not embed each vendor SDK in Myliba or bypass provider quotas. Backend inventory scans currently list all objects, so very large collections will need incremental indexing.

The Cloud panel uploads files already indexed in the local library. No-history is the default; selecting history retains revisions for browsing. Older encrypted blocks from no-history files are currently retained on disk pending safe garbage collection, so the setting controls visible history but does not yet reclaim that space. Cloud requests use per-device credentials; revoking a device blocks its Hub and direct-peer requests.

## Implemented features

- Add and scan local folders without moving source files.
- Keep a stable local resource ID across same-volume file moves where the filesystem exposes a stable file ID.
- Track local versions when size or modification time changes.
- Search names, paths, and manual tags with SQLite FTS5 plus substring matching. Filter by derived file attribute tags for type, extension, and size range; values within one attribute category are alternatives, while categories and manual tags combine. Attribute counts follow the current folder, search, manual tags, and other attribute groups; each group ignores its own selected options when counting alternatives.
- Group indexed resources in virtual top-level folders without moving source files. Existing and newly indexed resources start in “Unfiled” (no virtual folder, distinct from “Unorganized” resources without manual tags); create, rename, delete, and move resources between folders from the web app. Each folder shows only its own tags, and tags support multiple selection.
- Add tags one at a time or in batches, and use the unorganized queue to find resources without tags.
- Select all resources matching the current folder, search, and tag filters across every result page, then add a tag, move them to a virtual folder, or generate media thumbnails in batches.
- Save frequently used queries from a search and filters (multiple tags, virtual folder, file type, indexed folder, unorganized state, and sort order). Saved queries are stored on the home node and update as its index and tags change; the same saved queries appear through local and remote web access.
- Open a resource detail page from its card or a direct `?resource=<id>` link. The page shows file metadata, a type-specific preview, original-file download, and add/remove/suggested tags; it also works through the remote hub.
- Generate and cache image thumbnails in small (240 × 160), medium (480 × 320), and large (1280 × 960) sizes from the web detail page. The API also offers a mobile variant bounded by 720 × 960 pixels. Automatic preprocessing prepares both the medium and mobile variants for images and video first frames. The resource list still uses the medium thumbnail and does not generate thumbnails merely by loading the list.
- For videos, the detail page divides the duration into six segments and can extract a screenshot at each segment midpoint. The screenshots are cached on the home node. The Windows portable build includes FFmpeg and FFprobe; development runs can use tools on the node's `PATH`. Browsers that can read the video can also supply the duration.
- Serve the responsive app remotely through a password-protected hub and an outbound node connection.
- Transfer file content only over a direct browser-to-node WebRTC channel; keep metadata available through the Hub.

Video thumbnails and segment screenshots use the bundled FFmpeg in Windows portable builds, or FFmpeg on the home node for other builds. Users can generate thumbnails individually or select several resources for a controlled two-at-a-time batch. Without FFmpeg, JPEG, PNG, and GIF images still get thumbnails using Go standard image decoders. Generated JPEG thumbnails are cached under the Myliba config directory and refreshed when file size or modification time changes. Transcodes, cross-device metadata sync, individual device pairing and revocation, conflict resolution, and offline remote access remain future work. Current version change detection uses file size and modification time; BLAKE3 content verification is planned before remote transfer and deduplication.
