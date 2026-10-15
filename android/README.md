# Myliba for Android (first version)

The Android app has two independent libraries:

- **Phone files:** Select a folder with Android's system picker. The app keeps read access to that folder, indexes its files in an app-private SQLite database, and supports name/path/tag search, local tags, rescans, and in-app previews for images, text, PDF, and video. Other formats can open in a compatible installed app. Original files stay in place. A failed scan leaves the previous index intact.
- **Server:** Enter the root HTTPS URL of a Myliba Hub. The app opens the existing authenticated web UI in a WebView. The same Hub and home-node authorization rules apply. Regular same-origin downloads use Android Download Manager; the browser button opens the Hub in the system browser when a page needs browser-specific behavior.

The phone library and the server library are deliberately separate. This version does not upload phone files, sync tags, or cache server content for offline use.

## Build

Open `android/` as a project in Android Studio with JDK 17 and Android SDK 36. The project uses Android Gradle Plugin 9.4.0 and Gradle 9.6.0. This checkout does not contain a generated Gradle Wrapper; with Gradle 9.6.0 installed, run `gradle :wrapper --gradle-version 9.6.0` once inside `android/`, then `./gradlew :app:assembleDebug` (Windows: `gradlew.bat :app:assembleDebug`). The debug APK is written to `app/build/outputs/apk/debug/`.

Minimum Android version: 8.0 (API 26). No broad storage permission is requested. Android's folder picker controls which folders are available; some system folders cannot be selected on newer Android versions.

## First use

1. On **手机文件**, choose a folder and wait for the scan. Search the indexed file names, paths, and tags. Tap **打开** to preview; use **标签** to add or remove local tags.
2. On **连接服务端**, enter the self-hosted Hub's `https://` root URL and log in with the existing web password. The Hub and home node must already be configured as described in the repository root README.

The Android app has no access to the home node's local-only folder-management APIs. It uses the Hub's existing remote interface. Neither the Hub password nor node token is stored by the Android app; the WebView retains the Hub's session cookie.
