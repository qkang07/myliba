# Android 0.3 interface verification

Date: 2026-10-17. Screenshots show the actual APK running in an Android 15 (API 35) Google APIs emulator, with a Pixel 6 screen profile. The images and documents are generated test fixtures imported through Android's system folder picker. They are not bundled with the app.

## Completed

- `android/build.ps1 :app:assembleDebug :app:lintDebug`: successful, zero lint errors. Remaining lint warnings concern localization, SDK/tooling updates, backup configuration, launcher resources and the gesture detector's accessibility callback.
- System folder authorization and indexing: six files imported through SAF; originals stayed in the authorized directory.
- List/grid switching and cached image/PDF thumbnails.
- Search by a newly added manual tag; image type filtering; all-result selection and batch tag addition.
- Tag intersection: `Travel` matched three files, `Keep` matched one, and selecting both returned one file.
- Saved that filter as `Favorites`; reopening the smart collection returned the same file.
- Created `Travel` virtual folder and moved three selected files into it; folder count became three and unfiled count decreased from six to three.
- Image detail and full preview; double-tap zoom; PDF first-page rendering; UTF-8 Chinese text preview.
- Tag entry with the soft keyboard visible; save action stayed accessible.
- At 360 × 640 dp with font scale 1.15, search and its clear action worked; the filter body scrolled to its last controls while the apply/reset buttons remained visible.
- Source management and native library picker; non-HTTPS server addresses rejected inline.
- No app `AndroidRuntime` crash appeared during these operations.

## Scope still unverified

- Physical Android devices, Android 8 compatibility at runtime, and Android 16 predictive-back gestures on a device.
- A deployed Hub + home node + Android connection, including authentication, remote search, suggestions, previews and downloads. The remote adapter uses the existing API contract; compilation and local UI checks are not an end-to-end remote test.
- Video/audio playback codecs, pinch gestures with real multi-touch input, very large indexes, and long-running background scans.

## Screenshots

- [Resource library](screenshots/library.png)
- [Combined filters](screenshots/filters.png)
- [Folders and smart collections](screenshots/folders.png)
- [File details](screenshots/detail.png)
- [Native library picker](screenshots/source-picker.png)
