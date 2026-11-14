# Application name and stable identity

The desktop product is **Yes Man**, with one ASCII space. Its main image is
`Yes Man` on macOS/Linux and `Yes Man.exe` on Windows. `yesman` remains the
repository, module, CLI command, workspace/agent identity, and installation slug.
The bundle ID remains `dev.idy.yesman`; updater keys, feeds, manifest names,
archive names, data directories, and existing workspace display names stay intact.

| Surface | macOS | Windows | Linux |
| --- | --- | --- | --- |
| Main executable | `yesman.app/Contents/MacOS/Yes Man` | `Yes Man.exe` | `Yes Man` |
| Native application name | localized bundle name `Yes Man` | PE ProductName/FileDescription `Yes Man` | desktop Name and window title `Yes Man` |
| Stable installation | `yesman.app` at the existing location | existing registry InstallLocation; new installs still `Programs/yesman` | `~/.local/yesman.app` or package manager's `/opt/yesman` |
| Legacy entry | `Contents/MacOS/yesman` symlink to main | tiny `yesman.exe` entry: GUI handoff or synchronous CLI | tiny `yesman` launcher; exec replaces it with main |
| Page profile | same bundle ID / MyGo `yesman` paths | same `%LOCALAPPDATA%/yesman/WebView2` | same GLib `yesman` program ID and WebKitGTK profile |

On macOS, the unlocalized CFBundleName/CFBundleDisplayName remain `yesman`, matching
the physical bundle basename. `en.lproj/InfoPlist.strings` localizes both to
`Yes Man` (the fallback for other languages), so Finder can show the product name
without moving an existing install. CFBundleExecutable names the actual `Yes Man`
image. This follows Apple's [bundle naming rules](https://developer.apple.com/library/archive/documentation/General/Reference/InfoPlistKeyReference/Articles/CoreFoundationKeys.html).
The hosted check records both NSRunningApplication.localizedName and
FileManager.displayName, alongside the kernel's `proc_name` and executable path.
It does not infer kernel process naming from the plist or a screenshot.

Windows calls with inherited console, pipe, or disk standard handles preserve
those streams and wait for the main process, returning its exact exit code.
When a GUI-subsystem terminal invocation has no handles, the entry can attach to
an existing parent console with the public AttachConsole API; it never allocates
a new console. Explorer/shortcut launches and MyGo's restart helper have no
console and use absent/NUL streams: they start main and release the forwarding
process. Callers redirecting diagnostics are CLI callers even if the main app
opens a window. No special application/test flag selects this behavior.

Windows' [ProcessName](https://learn.microsoft.com/en-us/dotnet/api/system.diagnostics.process.processname)
comes from the executable; PE descriptions are separate fields. The main image
and both descriptions are changed. Existing shortcuts/pins pointing at
`yesman.exe` remain functional and keep their existing labels after an automatic
update. Running the new installer replaces its standard `yesman.lnk` shortcuts
with `Yes Man.lnk`, retaining whether the user had a desktop shortcut. The stable
uninstall registry key recognizes the original folder. After an automatic archive
update, the main process refreshes the old standard Apps display name and icon
only when that stable registration points to its exact installation directory;
portable copies, other installs and custom registry labels are preserved.
User-created shortcuts and pins need no path migration; no second application
folder is created.

Linux's kernel comm uses the actual main image. Its desktop entry still targets
the stable compatibility entry, quoted with an absolute path by the installer.
The launcher resolves a CLI symlink, preserves arguments/environment/cwd, and
execs the main image in the same PID. The GLib program ID / StartupWMClass remain
`yesman` to retain window grouping and the WebKitGTK profile; these are distinct
from the actual process name. See the [desktop Exec specification](https://specifications.freedesktop.org/desktop-entry/latest/exec-variables.html).

## Building

Use `go tool mygo build`, with the ordinary MyGo build options, such as
`-platform darwin/arm64,darwin/amd64 -skip-dmg` or `-upload`. The tool directive
points to `tools/mygo`: builds route through `tools/package` and other commands
forward to the pinned upstream command. `go run ./tools/package` is equivalent
for builds. `go build` and `go run .` remain development commands; their output
filename is selected by Go/the caller. The main process of an unrenamed
development executable keeps that filename. Upstream MyGo has no independent
Linux executable name or stable outer bundle-name setting.

The adapter stages the exact Go-module-checksummed MyGo v0.9.0 source in a private
temporary directory, checks SHA256 for each changed upstream file, and applies
small reviewed replacements. A temporary modfile makes both its CLI and the
production application use that staged dependency. Neither the source go.mod,
the module cache, nor installed Go tools are modified. Go build information records
the local replacement; the source revision still identifies this repository.
Unknown dependency versions or changed inputs fail closed. Updating MyGo requires
reviewing these adaptations and rerunning the native/update contracts.

The changes cover bundle/file naming and localized display names, including the
old executable symlink before signing; a real Linux main image and legacy launchers
before archive/file-list/signature creation; Windows/Linux page-profile identity;
and Linux updater desktop-registration identity. Archive extraction, signatures,
delta generation/application, bundle staging, file swaps, and rollback remain the
pinned upstream implementations. The Windows install hook only adjusts the old
standard shortcuts. No argv[0] rewriting, process-title spoofing, private OS API,
secondary lowercase GUI process, data migration, or updater-key rotation is used.

At startup, the app takes the old single-instance lock under `yesman`, pins MyGo's
name-derived user-data/cache/log paths, then sets the runtime display name. The
lock release uses the already stored lock path. A second old/new launch therefore
still forwards to the one running instance. Explicit e2e identities remain isolated.
Bindings generation returns before storage/vault/agent initialization.

## Verification and publication

`go test ./...` includes a real compatibility-launcher argument transfer test and
a subprocess proving bindings generation cannot initialize storage. Run
`go run ./tools/package -verify-updater` for the real pinned updater with a signed
synthetic old install through both full archives and deltas, a main recorder,
the compatibility launcher, restart and portable/workspace/vault/session sentinels.
The renamed-file delta (including macOS file-to-symlink replacement) must succeed
without silently falling back to the archive. It includes upstream signature/delta,
file-swap rollback and restart tests plus macOS install-after-exit/relaunch and
failed-move rollback. Fixtures never run the real application against user data.

`.github/workflows/application-name.yml` builds the normal production distribution
on fresh hosted macOS ARM/Intel, Windows x64, and Linux x64 runners. It checks real
OS process metadata, native visible windows, an actual webview/Go State call,
legacy entry launch, unchanged executable hashes and normal exit. Linux also runs
the generated installer twice at a space-bearing profile path, launches the actual
.desktop through gio, and checks comm/CLI/uninstall. Windows runs the actual NSIS
installer against a synthetic registered old install, checks PE/Get-Process,
shortcut targets, old-folder reuse, and uninstallation. Scripts refuse self-hosted
or workstation execution. These checks require no provider credentials or paid calls.
Native Windows/Linux ARM execution is not implied by their cross-builds.

Before publishing the release containing this change:

1. Integrate against the final preceding main. Existing `go tool mygo build`
   commands in production startup and draft-smoke steps now route through this
   repository's adapter. The stable `yesman.app` archive path remains valid;
   binary discovery must read CFBundleExecutable. Preserve macOS cgo, deployment
   target, permission entitlements and purpose strings.
2. Recheck all six final archives, binary layouts, file lists, signatures,
   deltas and ordinary manifest URLs. Download filenames remain lowercase.
3. Run the production naming smoke and existing Grant startup/draft smoke against
   the final immutable tag and downloaded draft binaries (no rebuild substitution).
4. Exercise the actual preceding public version's update to this draft in isolated
   VMs, including restart through old Windows/Linux entry points, macOS bundle
   replacement, page settings and external workspace/vault/session sentinels.
5. Publish only after the preceding release is public and all six ordinary update
   manifests serve it. Keep the allocated patch order and immutable old tags.
