# Myliba for Android

The Android app has two independent libraries:

- **Phone files:** Select folders with Android's system picker. The app indexes files in an app-private SQLite database. Search names, paths, and manual tags; combine manual tags, file type, size, format, source root, and the unorganized queue; sort and page results. Organize indexed files into virtual folders, save dynamic smart collections, select all matching results, and batch add/remove tags or move files between virtual folders. Preview images, text, PDF, and video in the app; other formats can open in a compatible installed app. Original files stay in place. A failed scan leaves the previous index intact.
- **Server:** Enter the root HTTPS URL of a Myliba Hub. The app opens the existing authenticated web UI in a WebView. The same Hub and home-node authorization rules apply. Regular same-origin downloads use Android Download Manager; the browser button opens the Hub in the system browser when a page needs browser-specific behavior.

The phone library and the server library are separate. Local virtual folders, tags, and smart collections live only in the app database. This version does not upload phone files, sync tags, or cache server content for offline use. Local search uses SQLite substring matching; the server's FTS5 index and AI-assisted tag suggestions are available through the server tab.

## Build

Open `android/` as a project in Android Studio with JDK 17 and Android SDK 36. The project uses Android Gradle Plugin 9.4.0, Gradle 9.6.0, and the included Gradle Wrapper. On the configured Windows development machine, run `./build.ps1` from `android/` to use the repository-local toolchain. The debug APK is written to `app/build/outputs/apk/debug/`. You can also run `./gradlew.bat :app:assembleDebug` after setting `JAVA_HOME`, `ANDROID_HOME`, and a writable `GRADLE_USER_HOME`.

The Windows toolchain is under `.runtime/android-toolchain/` at the repository root and is ignored by Git. `build.ps1` sets environment variables only for its own process; it does not change system settings.

Minimum Android version: 8.0 (API 26). No broad storage permission is requested. Android's folder picker controls which folders are available; some system folders cannot be selected on newer Android versions.

## First use

1. On **手机文件**, choose a folder and wait for the scan. Search indexed names, paths, and tags. Use **筛选** for combined filters, **文件夹** for virtual groups, and **智能集合** to save the current search. Select individual results, the loaded page, or all matching files for batch operations. Tap **打开** to preview; use **标签** to edit one file's tags.
2. On **连接服务端**, enter the self-hosted Hub's `https://` root URL and log in with the existing web password. The Hub and home node must already be configured as described in the repository root README.

The Android app has no access to the home node's local-only folder-management APIs. It uses the Hub's existing remote interface. Neither the Hub password nor node token is stored by the Android app; the WebView retains the Hub's session cookie.
