# Myliba for Android

The native Android interface uses one navigation model for both libraries:

- **资源**: search, type shortcuts, combined filters, list/grid views, sorting and file details. Tap to open details; long-press to select. Selection exposes batch tag editing and virtual-folder moves, including all matching search results.
- **文件夹**: virtual folders and frequently used queries.
- **来源**: authorized phone directories, rescanning and server connection settings.
- **传输**: downloads started from the home library, with live progress, completion, failure, open and cancel controls. The list survives app restarts.

Switch **本机 / 家中** from the top-right library picker. Filters are independent for each library. Dialogs use native bottom sheets; short menus fit their content. Tap a file thumbnail for a full-screen preview. Images support pinch zoom, double-tap zoom/reset and bounded panning; tap once to hide preview controls. Previous/next buttons browse media in the current loaded result page. Lists reuse views and cache thumbnails in memory. There is no embedded remote web page.

See [actual APK screenshots and verification notes](docs/verification-0.3.md).

The Android app has two independent libraries:

- **Phone files:** Select folders with Android's system picker. The app indexes files in an app-private SQLite database. Search names, paths, and manual tags; combine manual tags, file type, size, format, source root, and the unorganized queue; sort and page results. Organize indexed files into virtual folders, save frequently used queries, select all matching results, and batch add/remove tags or move files between virtual folders. Preview images, text, PDF, and video in the app; other formats can open in a compatible installed app. Original files stay in place. A failed scan leaves the previous index intact.
- **Server:** Enter the root HTTPS URL of a Myliba Hub. The Android app uses the Hub's HTTPS API in a native mobile interface for login, search, combined filters, saved queries, virtual folders, resource details, tag editing, batch operations, and downloads. The same Hub and home-node authorization rules apply.

The phone library and the server library are separate. Local virtual folders, tags, and saved queries live only in the app database. This version does not upload phone files, sync tags, or provide an offline server library. Local search uses SQLite substring matching; server search uses the existing server API. Server tag suggestions can be requested from a file's details.

Server image, text and PDF previews load on demand, with a 24 MiB limit and a temporary app-private file deleted when the preview closes normally. Video/audio use authenticated streaming. Larger or unsupported files can be downloaded from the details sheet and followed in **传输**. The app requests a dedicated mobile thumbnail (maximum 720 × 960 pixels), with a legacy cache fallback. The server preprocessing queue generates both the web thumbnail and the mobile thumbnail or video first frame. The file detail sheet can generate six video segment screenshots at the same mobile size, or accept a manual timestamp when the video duration is unavailable; **重新生成移动端封面** and **后台预处理** use the current server API. Attribute filter counts follow the current search and other filters while retaining selected zero-count options. It uses HTTPS Hub requests rather than the web client's WebRTC transport.

## Build

Open `android/` as a project in Android Studio with JDK 17 and Android SDK 36. The project uses Android Gradle Plugin 9.4.0, Gradle 9.6.0, and the included Gradle Wrapper. On the configured Windows development machine, run `./build.ps1` from `android/` to use the repository-local toolchain. The debug APK is written to `app/build/outputs/apk/debug/`. You can also run `./gradlew.bat :app:assembleDebug` after setting `JAVA_HOME`, `ANDROID_HOME`, and a writable `GRADLE_USER_HOME`.

The Windows toolchain is under `.runtime/android-toolchain/` at the repository root and is ignored by Git. `build.ps1` sets environment variables only for its own process; it does not change system settings.

Minimum Android version: 8.0 (API 26). No broad storage permission is requested. Android's folder picker controls which folders are available; some system folders cannot be selected on newer Android versions.

## First use

1. Choose **本机**, tap **选择手机文件夹**, authorize a directory and wait for indexing. Search names, paths and tags. The filter icon opens combined filters; multiple manual tags use AND matching. Long-press a file for batch operations. The resource overflow menu saves the current search and filters as a frequently used query.
2. Choose **家中** from the top-right picker, enter the self-hosted Hub's `https://` root URL and log in with the existing web password. The Hub and home node must already be configured as described in the repository root README.

The Android app has no access to the home node's local-only folder-management APIs. It uses the Hub's existing remote interface. The Hub password is not stored; the authenticated session cookie is kept in app-private preferences. Phone-local folders and tags remain separate from the server library.

## Cloud storage node

Under **本机 → 来源 → 云层存储节点**, enter the Hub's HTTPS root URL, the phone's direct HTTPS URL, and the separate cloud token, then start the foreground service. It listens on port 4319, stores only encrypted blocks and opaque catalog records in app-private storage, reports its inventory, and synchronizes the encrypted catalog with the Hub. A public HTTPS gateway must connect peers directly to the phone; the Hub does not proxy block data. Keep the gateway-to-phone hop on a trusted network. The service starts only when the user enables it and can be stopped from the same screen. Uninstalling the app deletes its stored blocks. Real-device background lifetime, battery behavior, and public HTTPS connectivity remain to be verified.
