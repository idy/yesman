# Application permissions

Settings › **Grant** (授权) reads OS access for the current copy of yesman. Refresh and returning to the app only read status. Each Request button is an explicit user action; Open Settings launches a fixed OS destination without claiming access was granted. The page does not add capture or Bluetooth tools. Independently launched terminals, runtimes and helpers can have their own permission identity and may need separate authorization.

## Platform behavior

| Permission | macOS query | User action on macOS | Windows / Linux |
| --- | --- | --- | --- |
| Bluetooth | `CBManager.authorization`: not requested / restricted / denied / granted | Create a retained `CBCentralManager` only after Request; never scan or connect | Windows Bluetooth settings, no per-app query; Linux system managed |
| Notifications | `UNUserNotificationCenter` settings; distinguish denied, provisional, alerts disabled and sounds disabled | Request alert, sound and badge authorization; keep MyGo's delegate | Windows notification settings, no reliable per-app query for this Win32 build; Linux desktop managed |
| Full Disk Access | Manual; cannot reliably detect with a public global API | Privacy & Security › Full Disk Access | No equivalent macOS switch; existing file security still applies |
| Microphone / Camera | `AVCaptureDevice` authorization, including system restriction | Asynchronous `requestAccess`, with usage descriptions | Windows desktop privacy settings; Linux device/session/portal policy |
| Accessibility | `AXIsProcessTrusted`, a Boolean without decision history | `AXIsProcessTrustedWithOptions` with prompt only after Request | No equivalent macOS trust switch |
| Screen Recording | `CGPreflightScreenCaptureAccess`, a Boolean | `CGRequestScreenCaptureAccess`; refresh/restart as macOS directs | Windows session/security policy; Linux compositor/portal policy |
| Input Monitoring | `CGPreflightListenEventAccess`, a Boolean | `CGRequestListenEventAccess`; refresh/restart as macOS directs | No equivalent macOS switch; session/security policy still applies |
| Automation | No global query or global grant | Open Automation settings; macOS asks per target when an Apple Event is actually sent | No equivalent macOS per-target TCC grant |

Windows buttons use Microsoft's documented `ms-settings:` destinations for Bluetooth, notifications, microphone and camera. The installed application is unpackaged Win32; this page does not present packaged/UWP permission checks as this application's access. Linux has no portable application-wide permission status or single settings application, so the page explains desktop, device, session and portal/sandbox limits without spawning guessed commands. Neither platform reports “all access granted.”

The macOS checks require a real `.app` with bundle identifier `dev.idy.yesman` for native status/request operations. Unbundled `go run`, command-line binaries and differently identified development apps show Unavailable, avoiding notification-center exceptions and misleading host-app permissions. Full Disk Access and Automation still provide manual guidance. Ad-hoc signing is unchanged; permission persistence across replacing/updating an ad-hoc app is controlled by macOS, not guaranteed by this page.

## Implementation and build

`internal/nativepermissions` isolates adapters behind a small Go interface. `grants.go` binds only List and a whitelisted permission/action pair. The Darwin Cgo implementation dispatches UI operations to the main queue, uses asynchronous settings launching and runs synchronous CoreGraphics requests on a worker queue, catches Objective-C exceptions, retains Bluetooth objects, and passes numeric callback tokens instead of Go pointers. Query waits are bounded to 8 seconds and actions to 2 minutes. Closing the page cancels the MyGo context; late callbacks find no waiting token. OS prompts cannot generally be dismissed by the app: native pending guards survive a canceled wait and reject additional prompts until the system finishes. Boolean prompt APIs are offered once per process, followed by system settings.

`web/grants.js` owns the page's DOM and asynchronous generation. It preserves focused controls during local refreshes, coalesces focus/visibility refreshes, rejects repeated actions, and ignores results from a previous page visit. Read-only refresh remains available while an OS action is pending, including after leaving and returning. Every action completion invalidates older status reads and schedules one fresh read if needed, so an in-flight snapshot cannot restore stale authorization or busy state. Status query errors clear authorization, and successful settings launches retain the previous status with a manual-action message.

No protected user file, Safari/Mail directory or TCC database is read for detection. There is no private TCC API, reset, database write, elevated command or automatic permission grant.

macOS uses Cgo only in the app-owned module, linking Foundation, AppKit, AVFoundation, CoreBluetooth, UserNotifications and ApplicationServices. `mygo.json` keeps the existing bundle ID and signing identity, adds Bluetooth/camera/microphone purpose strings and device entitlements, and states macOS 13, the actual minimum for Go 1.27. The release workflow enables Cgo only for its macOS job. Explicit C flags keep both the module and Go's C runtime at that deployment target even on a newer SDK:

```sh
CGO_ENABLED=1 CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0' MACOSX_DEPLOYMENT_TARGET=13.0 go tool mygo build
```

Normal `go build` on macOS with Cgo works too. A `CGO_ENABLED=0` Darwin development build compiles but explicitly reports the native bridge unavailable. Linux and Windows cross builds remain Cgo-free. No dependency is patched or forked.

## Validation without granting machine permissions

```sh
CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0' MACOSX_DEPLOYMENT_TARGET=13.0 go test -race ./internal/nativepermissions
YESMAN_PLAYWRIGHT=/outside/repo/playwright/index.mjs node scripts/grants-browser.mjs
YESMAN_PLAYWRIGHT=/outside/repo/playwright/index.mjs node scripts/grants-lifecycle-browser.mjs
```

Go fixtures cover state/error mapping, action whitelists, system-only platforms, request outcomes, canceled waits, reentry, and retired/duplicate native callbacks. The optional browser test loads the full production page in Chromium and WebKit with controlled bridge results: navigation, every action ID, denial/restriction/unresolved response, errors/retry, no implicit request, stale reads, tab switches, focus restoration, English/Chinese, light/dark and narrow layouts. This is not a real OS-prompt or permission-grant end-to-end test.

`scripts/grants-probe` is an optional read-only native linkage probe. It never initializes yesman storage or agents and has no request/settings action path. Run unbundled to verify the identity guard, or package that probe separately to inspect the temporary copy's native status. Do not treat its statuses as those of an installed release; do not reset or grant machine permissions for tests. Production bundle inspection should check both architectures' Info.plist, code-signing entitlements, framework dependencies and `LC_BUILD_VERSION` without launching them.

## Production macOS startup gate

CI's `macos-startup` jobs use standard GitHub-hosted ARM and Intel macOS images. GitHub gives each job a fresh VM/user profile; `scripts/check_macos_startup.py` refuses non-hosted runners, existing yesman processes and existing app profile data. Do not run it in a user's account or spoof its runner guard. The signing input lives in `packaging/macos/`, outside the all-platform runtime resources.

Each job builds a normal `mygo build` production `.app` with Cgo (no debug/e2e tags or automation plugin), verifies its exact entitlement set/signature and actual Mach-O SDK/minimum version, then launches that unchanged executable twice. The first launch uses embedded assets and must create an on-screen native window. The second uses the existing `YESMAN_WEB_DIR` override: all production assets are identical, with only a read-only observer appended to the copied index. In the actual WKWebView it navigates Settings › Grant, confirms nine rendered rows and the real Go State/Grants bridge responses, and never clicks Request/Open Settings. Both processes must exit normally through MyGo's SIGTERM→Quit handler, and their native windows must disappear. The binary checksum must remain unchanged. A failed read, crash, missing page/window or forced exit fails the gate.

The runner's whole disposable profile isolates NSUserDefaults, WebKit and MyGo caches. Separate `YESMAN_HOME` directories contain no workspaces/vault, and an absent pi path prevents provider calls. The child receives no CI credentials. Its explicit loopback proxy rejects external HTTPS updater traffic. No Accessibility automation, screen image capture, grants, permission reset or system account creation is used. The observer page is a documented frontend-only instrumentation difference; this establishes production native startup/bridge, not permission-prompt E2E.

Before publishing, dispatch **macOS draft startup** (`macos-draft-smoke.yml`) with `release_tag` set to the final immutable tag, after its macOS archives exist. This runs both architectures on fresh hosted runners, downloads the **final draft archives**, checks their GitHub asset digests, and uses `web/` and the smoke scripts checked out at that exact tag. It neither rebuilds nor publishes. Both jobs must pass and their evidence must be retained before publication; the publisher's complete signature/manifest checks remain required.

The underlying command, for the matching architecture in a fresh standard hosted runner, is:

```sh
python3 scripts/check_macos_startup.py /path/to/extracted/yesman.app web "$RUNNER_TEMP/draft-startup-evidence"
```

The step must set `RUNNER_ENVIRONMENT: ${{ runner.environment }}`. Retain `bundle.json`, `page.json`, `smoke.json` and the two launch logs as publication evidence. A publisher must also save the release build's `xcodebuild -version` and `xcrun --show-sdk-version`; the downloaded binary's `machOSDK` and `machOMinOS` in `bundle.json` are authoritative. Do not copy a local SDK version into the release record. Standalone `python3 scripts/check_macos_bundle.py APP OUTPUT.json` inspects without launching and is safe outside the CI runner, but cannot replace startup verification.

## API references

- Apple: [Bluetooth authorization before creating a manager](https://developer.apple.com/documentation/corebluetooth/cbmanager/authorization-swift.type.property), [capture authorization](https://developer.apple.com/documentation/avfoundation/avcapturedevice/requestaccess(for:completionhandler:)), [notification settings](https://developer.apple.com/documentation/usernotifications/unnotificationsettings).
- Apple: [screen capture preflight](https://developer.apple.com/documentation/coregraphics/cgpreflightscreencaptureaccess()), [input monitoring preflight](https://developer.apple.com/documentation/coregraphics/cgpreflightlisteneventaccess()), [Full Disk Access changes and scope](https://developer.apple.com/news/?id=p6zjojqw). Accessibility prompt semantics are also declared in the public SDK's `AXUIElement.h`; passing no prompt option for the query avoids side effects.
- Apple: [audio input](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.device.audio-input), [camera](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.device.camera), [Bluetooth](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.device.bluetooth) entitlements.
- Microsoft: [settings URIs](https://learn.microsoft.com/en-us/windows/apps/develop/launch/launch-settings), [desktop camera/microphone privacy](https://support.microsoft.com/en-us/windows/privacy/windows-camera-microphone-and-privacy).
- Go: [minimum operating system requirements](https://go.dev/wiki/MinimumRequirements).
