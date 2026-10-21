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

### Windows portable build with video support

On the build computer, get a Windows FFmpeg build that includes `ffmpeg.exe`, `ffprobe.exe`, `LICENSE`, and `README.txt`. The [Essentials build](https://www.gyan.dev/ffmpeg/builds/) is sufficient for Myliba and makes a smaller package. Then run:

```powershell
.\scripts\package-windows.ps1 -CreateZip
```

After `pnpm install --frozen-lockfile`, the script builds the web app and Go service, and writes a portable folder under `.runtime/releases/`. It places FFmpeg and FFprobe in the `ffmpeg/` folder beside `myliba.exe`, so the target computer needs no separate FFmpeg installation or `PATH` change. The script prefers an Essentials build extracted under `.runtime/ffmpeg-essentials/`; pass `-FFmpegBinDir <path>` to select another build; use `-SkipWebBuild` only when `cmd/myliba/web/dist` is already current. Keep the `ffmpeg/` folder with `myliba.exe` when copying the build. The included FFmpeg license and build information are in `ffmpeg/LICENSE` and `ffmpeg/README.txt`; review the [FFmpeg distribution guidance](https://ffmpeg.org/legal.html) before publishing a package.

## Remote web access

The same executable runs as a local client, a Hub, or a storage-only cloud node. The home client keeps its local HTTP service bound to `127.0.0.1:4317`; it maintains an outbound WebSocket connection to the Hub for signaling and metadata requests. After login, the browser negotiates a WebRTC DataChannel with the home client. File bytes and generated media require a direct DataChannel; they never fall back to the Hub WebSocket. Metadata requests can still use the Hub relay.

### 1. Build on the VPS

Install Go, a C compiler, C build tools, and pnpm. Build the Vue assets and executable on the VPS:

```sh
pnpm install --frozen-lockfile
pnpm build
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

`MYLIBA_WEB_PASSWORD` must contain at least 16 characters. `MYLIBA_NODE_TOKEN` must contain at least 32 characters and must match on the home node. The hub listens on loopback by default; place it behind the existing HTTPS gateway. Proxy `/`, `/api/`, `/node`, and `/signal` to the Hub address without rewriting paths. Preserve the `Host` header and WebSocket upgrades on `/node` and `/signal`, allow long-lived WebSocket connections, and avoid caching login and API responses. Keep the Hub port private and match `MYLIBA_HUB_LISTEN` to the address and port used by the gateway.

### 3. Connect the home node

On the computer that runs the local Myliba index, start the app and open <http://127.0.0.1:4317>. Click **服务端** in the top bar, then add the Hub's `wss://.../node` address and the node token configured on the VPS. The local app saves named Hub profiles in its local database. You can switch or disconnect at any time without restarting; only one Hub is connected at a time. The token is not returned to the browser after saving. Existing `MYLIBA_HUB_URL` and `MYLIBA_NODE_TOKEN` variables are imported once when the local database has no Hub profiles, for compatibility with older startup scripts.

```powershell
go run -tags sqlite_fts5 ./cmd/myliba
```

The home node still opens its local interface at <http://127.0.0.1:4317>. From a phone, open `https://library.example.com` and sign in with the web password. The remote page can search, tag, and organize while the home node is online. Opening, previewing, and downloading file content requires a direct WebRTC DataChannel. Set `MYLIBA_ICE_SERVERS` to STUN servers on the Hub and home node when needed; the default is `stun:stun.l.google.com:19302`. TURN relay candidates are ignored for content transfer. The remote page does not let a phone add a new home filesystem path.

The hub requires a web session for every remote API call, and both the hub and home node enforce the same remote API allowlist. The WebRTC DataChannel is created for an authenticated signaling session and can carry only allowlisted API requests. Adding a new index root and browsing the home filesystem remain local-only operations. Browser-active file types such as HTML, SVG, and JavaScript are downloaded rather than opened inside the authenticated app origin.

The hub and node token are shared secrets for this first self-hosted version; they are not yet individual paired device identities. Hub web sessions expire after 30 days and are held in memory, so restarting the hub requires users to sign in again. Use a unique, long web password, restrict VPS access to HTTPS, and keep the node token private. Do not set `MYLIBA_HUB_COOKIE_SECURE=0` outside local HTTP development.

After authentication, the browser negotiates a WebRTC DataChannel through the Hub's signaling WebSocket. File content, thumbnails, previews, and downloads use that direct channel. Without it, these operations report an unavailable direct connection. The Hub still serves the page, authenticates the browser, and relays metadata and signaling.

The Hub and home client use the same optional `MYLIBA_ICE_SERVERS` JSON array. For example, set `MYLIBA_ICE_SERVERS='[{"urls":["stun:stun.example.net:3478"]}]'`. If direct ICE connectivity is blocked, metadata remains available through the Hub, but content transfer waits for a direct route.

### Single-file public shares

In a resource detail page, create a share link with a 1, 7, or 30-day expiry. Anyone holding the link can open its public Hub page and download that file while the home node is online and WebRTC direct connectivity succeeds. The URL fragment contains a random secret; it is shown only when the link is created and is not sent in the initial HTTP request. The Hub asks the home node to validate it before signaling, and the node checks it again for every file request. Resource paths, the full-library login, the node token, and the cloud token are never included in the share response. The share list in the resource detail page can revoke a link, after which new file requests fail. If the source file changes, create a new link. This first version does not serve share bytes through the Hub or offline cloud replicas.

The HTTPS gateway must forward `/s/`, `/share/signal`, and `/api/shares/` to the Hub alongside the existing `/`, `/api/`, `/node`, and `/signal` routes. Keep WebSocket upgrades enabled for `/share/signal`. The browser and a future native client can use the same redeem and signaling protocol; the current recipient interface is the Hub web page.

## Encrypted cloud layer

The [global index and virtual node design](docs/cloud-index-and-virtual-nodes.md) defines OpenList as a separately identified virtual node. File and block indexes refer to node IDs and public access methods; access credentials live outside the index, locally on authorized nodes or at the Hub for on-demand lookup. The gateway configuration documented below is the current implementation and has not yet been migrated to that model.

The cloud layer stores each indexed file as immutable 4 MiB plaintext chunks encrypted locally with a fresh AES-256-GCM key for that file revision. Ciphertext hashes address blocks; an encrypted manifest records the filename, key, order, sizes, and optional user-visible history. The client keeps a local block copy and tries to place each block on up to two additional online nodes. Replication is best-effort: one node is enough to upload, and the Cloud panel shows the minimum online replica count per file. Each node keeps opaque catalog records and advertises its actual block inventory. The Hub also keeps the encrypted catalog, but never stores file blocks.

A local client can start as a **one-node cloud** without a Hub, cloud token, public URL, or another storage node. It encrypts and stores blocks, catalogs, and its recovery key in `MYLIBA_CLOUD_DATA_DIR` (or its default cloud data directory), then reads them locally. The Cloud panel reports one of three target replicas and warns about the missing backups without blocking uploads or downloads. Keep a separate backup of this directory and the recovery key while the client is the only node. Adding peers later enables replication; no separate cloud initialization is required.

The Hub's signed-in page has a **节点管理** button. It lists registered client and storage nodes, online/offline status, last heartbeat, direct URL, backend type, block replicas, and catalog counts. Disk nodes report encrypted block bytes and filesystem capacity/free space; Android storage nodes report app-private volume capacity/free space. S3/OSS and WebDAV gateways report block counts but currently have no reliable quota or byte usage, so those fields appear as unknown. The overview sums reports from online nodes: replicated blocks count more than once, and capacity can count the same underlying volume twice if multiple nodes share it. Offline nodes remain visible across Hub restarts, while peer discovery excludes them. The dashboard uses the normal Hub login session; the cloud token is never sent to the browser for this view.

Set a separate `MYLIBA_CLOUD_TOKEN` of at least 32 random characters on the Hub to enable cloud control endpoints. It must **not** be the existing `MYLIBA_NODE_TOKEN`. Configure the same cloud token in the local client's Hub settings (or its `MYLIBA_CLOUD_TOKEN` environment variable). The Hub stores encrypted catalog records under `MYLIBA_HUB_DATA_DIR/cloud`, defaulting to its user config directory. Back up the recovery key shown in the local Cloud panel; a new client can import it to decrypt catalog records restored from the Hub or storage peers.

To run a storage-only node, use a separate copy of the executable:

```sh
MYLIBA_MODE=storage \
MYLIBA_HUB_URL=wss://hub.example.com/node \
MYLIBA_CLOUD_TOKEN='a-separate-long-random-cloud-secret' \
MYLIBA_CLOUD_LISTEN=127.0.0.1:4319 \
MYLIBA_CLOUD_PUBLIC_URL=https://storage-a.example.com \
./myliba
```

The storage node exposes `/cloud/blocks/*`, `/cloud/catalog/*`, and `/cloud/inventory` through its **own** HTTPS gateway. Route those paths directly to that node's `MYLIBA_CLOUD_LISTEN` address, not through the Hub. Other nodes transfer encrypted blocks directly to this URL. A local client may also set `MYLIBA_CLOUD_PUBLIC_URL` to be an online source for other clients; without one it can still upload to reachable storage nodes. Direct endpoints must use HTTPS outside loopback development. For recovery when the Hub is unavailable, set `MYLIBA_CLOUD_BOOTSTRAP_URLS='["https://storage-a.example.com"]'` on the new client together with its cloud token and import the recovery key.

For a private Go or Android node without a public HTTPS address, use a **public storage node** as a relay. On that public storage node set `MYLIBA_CLOUD_RELAY_ENABLED=1` and expose its `/cloud/tunnel/*` and `/cloud/relay/*` paths through the same HTTPS gateway as the direct cloud paths. On the private Go node set `MYLIBA_CLOUD_RELAY_URL=https://storage-a.example.com` and leave `MYLIBA_CLOUD_PUBLIC_URL` empty. It opens an outbound authenticated WebSocket tunnel and advertises `https://storage-a.example.com/cloud/relay/<node-id>` to the Hub. The Hub still carries only node registration and opaque catalog records; encrypted block bytes flow through the public **storage** node. On Android, enter the relay address in the storage-node settings instead of a phone direct URL. If a public URL is configured, the direct endpoint remains preferred. Both relay and private node currently use the shared cloud token, so keep the relay inside your own trust boundary. A relay outage makes the private node temporarily unreachable; its local blocks and index remain intact. Peer-to-peer NAT traversal remains future work.

### Storage node platforms and virtual backends

The independent storage binary contains no SQLite or web UI. Build it on Windows with `./scripts/build-storage.ps1 -TargetOS windows -TargetArch amd64`; use `darwin` or `linux` and `arm64` for other targets. On macOS/Linux, run `sh scripts/build-storage.sh linux amd64` (or `darwin arm64`). Output is under `.runtime/storage-build/<os>-<arch>/`. The binary runs with the storage environment variables above; set `MYLIBA_CLOUD_DATA_DIR` to a persistent directory.

The **same Android APK** can be a normal library client or a storage-only node. Its default backend uses **the phone's own app-private storage**: the phone registers with the Hub as a `storage` node, receives encrypted blocks and opaque catalog records from cloud peers, advertises its inventory, and serves stored blocks back to peers. Under **本机 → 来源 → 云层存储节点**, keep **本手机存储（云层节点）** selected, configure the Hub HTTPS URL, cloud token, and either a phone direct HTTPS URL or a public storage relay HTTPS URL. Tap **切换为仅存储模式** to show the storage dashboard without the library navigation; **切换到完整客户端** returns to the normal UI. The foreground storage service continues after closing the screen. **OpenList 云盘** is an optional alternative storage backend for that Android node. If selected, provide your separately running OpenList HTTPS URL, a dedicated writable mount path, and an OpenList account with WebDAV read/manage/upload permissions; set the mount's WebDAV policy to **native proxy**. The APK never holds the cloud-drive provider's own credentials. The cloud token and OpenList password are encrypted with Android Keystore in app-private preferences; older plaintext settings are migrated when read.

The Android node accepts the same direct `/cloud` protocol on port 4319. With only a relay address, its local server binds to loopback and makes an outbound TLS tunnel; with a direct URL, provide an HTTPS gateway to the phone's port 4319. OpenList quota and provider limits still apply. Android background execution, device reachability, and OpenList provider behavior need real-device verification.

A virtual storage node runs that same binary as a gateway with `MYLIBA_CLOUD_BACKEND=webdav`, `s3`, or `openlist`. The gateway holds the cloud token; the backing account only receives encrypted blocks and opaque catalog records. Keep the gateway online for peer access and set its `MYLIBA_CLOUD_PUBLIC_URL` to the gateway's own HTTPS URL, or connect it to a public storage relay.

For WebDAV cloud drives (including services that expose the standard WebDAV interface), set `MYLIBA_WEBDAV_URL` to a dedicated collection, plus `MYLIBA_WEBDAV_USER` and `MYLIBA_WEBDAV_PASSWORD`. The gateway creates `blocks/` and `catalog/` below that collection. For S3-compatible object storage, set `MYLIBA_S3_ENDPOINT`, `MYLIBA_S3_BUCKET`, `MYLIBA_S3_REGION`, `MYLIBA_S3_ACCESS_KEY_ID`, and `MYLIBA_S3_SECRET_ACCESS_KEY`; optional `MYLIBA_S3_PREFIX` and `MYLIBA_S3_SESSION_TOKEN` are supported. The adapter signs requests with AWS Signature V4 and uses path-style addressing by default. Set `MYLIBA_S3_ADDRESSING=virtual` for virtual-hosted addressing, required by Alibaba Cloud OSS's S3-compatible endpoint. Use a dedicated bucket or prefix.

For Aliyun Drive, Baidu Netdisk, Quark, OneDrive, and other writable providers supported by OpenList, mount the provider in your **own OpenList instance** first. Then set `MYLIBA_CLOUD_BACKEND=openlist`, `MYLIBA_OPENLIST_URL=https://openlist.example.com`, `MYLIBA_OPENLIST_MOUNT=your-mount`, `MYLIBA_OPENLIST_USER`, and `MYLIBA_OPENLIST_PASSWORD` on a Myliba storage gateway. The Myliba gateway accesses OpenList's authenticated `/dav/<mount>/` endpoint; OpenList handles each provider's authorization and API. Give that OpenList user WebDAV read/manage and upload/mkdir permissions, and select a writable driver. Configure OpenList's WebDAV policy as **native proxy** so object downloads return bytes from OpenList instead of a redirect; Myliba rejects redirects to avoid leaking credentials. Each gateway targets one mount; run another gateway for another cloud-drive account or provider. This does not embed each vendor SDK in Myliba or bypass provider quotas. Backend inventory scans currently list all objects, so very large collections will need incremental indexing.

The Cloud panel uploads files already indexed in the local library. No-history is the default; selecting history retains revisions for browsing. Older encrypted blocks from no-history files are currently retained on disk pending safe garbage collection, so the setting controls visible history but does not yet reclaim that space. The cloud transport still uses a shared cloud token; individual device pairing and revocation are not yet implemented.

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
