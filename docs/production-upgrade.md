# Production upgrade evidence and publisher gate

There are three distinct evidence levels. A green low-level test is not proof of
a deployed-binary update.

| Level | Inputs and actual execution | Limits |
| --- | --- | --- |
| Low-level `-verify-updater` | Real pinned MyGo signatures/archive/delta/swap/helper/rollback, placeholder old file and recorder successor | Not a running old production main or released artifact chain |
| `application-name.yml` production-source upgrade jobs | Fixed `.29` source plus exact PR source, normal production builds, real windows/webviews/Go APIs and helpers; archive and delta old→new→next→failed install→rollback→legacy CLI; profile/locale/localStorage/data sentinels | Recompiled inputs: temporary fixture versions/public signing key/notes, no Git build metadata. Original compiled GitHub feed is retained; constrained VM TLS supplies signed cached inputs. No `.go` source edits. Not the downloaded `.29` release binary. |
| `application-upgrade-draft.yml` | Downloaded immutable public `.38` and draft `.39` archives; original binary bytes and original Ed25519 key; same real runner plus separately signed private QA successors for next-update/rollback | Actual `.38/.39` pair cannot exist before their ordered release preparation. QA successors are normal production-source builds from candidate source with private fixture versions, not future releases. Controlled feed transport does not prove public latest discovery. |

The controlled jobs build four production inputs: `.29`, then private fixture
`0.1.900001`/`900002`/`900003`. These versions are never tagged, uploaded to GitHub
Releases or allocated as product releases. A generated key exists only in
packaging subprocess environment. Runtime receives no signing key, provider
credentials or GitHub token. Per-Go-file hashes, source refs, original/fixture
configuration, archive manifests and binary hashes describe the differences.
Actual Go bridge `About/State/Settings/Update/CheckForUpdates/RestartToUpdate`
methods drive the real production application through the existing WEB_DIR
configuration. Only the observer is appended to each stage's original web files.

Both full-archive and delta scenarios verify real process name/path, visible
window, actual webview/bridge, exact installed image hash, same synthetic
localStorage marker, dark appearance, selected English locale and external
workspace/vault/session sentinels. Each positive delta hop must download a delta
without archive fallback. Failure points are intentionally reported separately:

- macOS: remove only the new staged bundle in the owned test installation after
  Ready, then use real RestartToUpdate and install-after-exit helper; the failed
  move restores the previous bundle and reopens its real main.
- Windows: hold an ordinary packaged `tray.svg` handle without delete sharing;
  an actual later resource rename fails after earlier entries were placed, so
  production swap undo must restore the previous real main and legacy entry.
- Linux: temporarily remove write permission on the owned installation directory;
  production install fails before placement. This proves failure preservation in
  the real app. Partial file-swap undo is independently covered at low level;
  this Linux case is not advertised as a partial-placement rollback.

For Launch Services/non-child Unix processes, the controller observes termination
and the next real process/bridge; it cannot retrieve their OS exit status. The
initial direct child and the final real legacy bindings CLI have observable exit
codes. The separate packaged naming jobs verify direct normal exits. Windows
uses process handles for actual child exit status.

## Final `.38 → .39` hard gate

After `.38` is public and all six ordinary latest/download manifests serve `.38`,
and after final reviewed `.39` metadata is merged/tagged and its draft six assets
are signed and verified, run:

```sh
gh workflow run application-upgrade-draft.yml -R idy/yesman --ref main \
  -f predecessor=v0.1.38 -f candidate=v0.1.39
```

Do not publish `.39` until that specific run's download, all four signing jobs
and all four fresh native VM jobs succeed. Do not rebuild the official pair as
a substitute or move an immutable tag to repair a failed gate.

The download job is the only job with draft-read `contents:write`. It has no
checkout and executes only the reviewed inline downloader; all `gh` operations
are GET/download. The token exists only in that step. The local Python entry
extracts this exact inline implementation for offline contract tests. The job
checks canonical allocated tags, public/draft flags,
all six ordinary predecessor manifests, stable tag commits, unique nonempty
assets and API SHA256 digests. It downloads the original signed archives,
manifests/deltas and each tag's original web source. The signing jobs receive the
existing updater private key only during packaging. Its producer comes from the
workflow's own commit (`.qa-producer`); the candidate checkout is build input
only. The producer removes the key from its environment before any subprocess:
`go env`, git, the Go/cgo compilers and the upgrade-assets helper's compilation
never receive it. Each MyGo build signs through a fresh owner-only key file named
by the temporary QA `mygo.json` and removed after that build; the precompiled
helper alone gets the key in its execution environment. A missing, mismatched
or unused key fails the step rather than leaving an unsigned successor. The
producer compiles the helper only if the candidate's helper blob equals its own
checked-out helper as cleaned by that checkout's Git rules (`git hash-object
--path`), so a CRLF checkout of the same file passes and any content change fails.
Every producer subprocess runs in one owned process group (Linux/macOS, joined
before exec) or job object (Windows). After each subprocess, and before any key
file, QA source/config or helper is removed, the producer confirms the group or
job has no other live member; on failure or TERM/INT/HUP (Windows Ctrl-C/Break)
it stops and confirms all members first. Removal is bound to the created
file/directory identity: a replaced pathname is left alone and reported, and the
key file is cleared through its held handle. If members cannot be confirmed
stopped, those resources are kept, listed in `qa-producer-retained-*.json` in
`RUNNER_TEMP`, and the step fails. A process that deliberately leaves the POSIX
group, or a SIGKILL of the producer, is outside this guarantee (Windows still
terminates the job when its handle closes). This is environment isolation, not a
same-user filesystem sandbox. They preserve both official
manifests byte-for-byte and make two private QA successor packages from exact
candidate source to test another update and real failed-install recovery.
Neither runtime jobs nor their artifacts receive the private key.

An untouched released main has a compiled GitHub feed and cannot use the
controlled rebuild's linker override. Runtime therefore uses a tightly scoped
loopback CONNECT/TLS cache serving only the downloaded/signed inputs for known
GitHub hosts. A one-day transport-test CA is installed only in the disposable
hosted VM (Linux process CA file, Windows VM machine Root store, macOS VM admin
trust domain), and that exact CA is removed in `finally`. This is separate from
the updater Ed25519 key: original archive/delta signature checks remain enabled.
No TCC/Grant permission, existing user profile, binary patch or process injection
is used. The transport does not establish public release discovery; publication
and all six ordinary public manifest checks remain separate mandatory gates.

Keep `released-upgrade-proof-*` and signed input artifacts. Evidence must show:

1. Correct run ID; exact `.38`/`.39` tag commits/release IDs/asset IDs and API
   digests; unchanged official archive/main hashes; expected version/OS/arch.
2. Old real main/helper → actual `.39` main through the legacy path, then `.39`
   initiates the private QA successor update; full and delta paths both succeed.
3. Real failed-install rollback/preservation at the documented platform point,
   previous image hash/version restored and actual legacy CLI still usable.
4. Actual native process/window/Go bridge and preserved locale/localStorage,
   settings and synthetic workspace/vault/session markers, with no duplicate app
   profile or resident GUI forwarding process.
5. Existing Grant final-draft startup checks still pass for the downloaded `.39`
   Mac ARM/Intel packages, with minOS13/cgo, exact entitlements/purpose strings and
   source identity intact. This upgrade gate does not replace those checks.

The workflow is prepared for the future immutable pair. Until `.38/.39` exist,
no result is pre-recorded as PASS. Native Windows/Linux ARM64 execution is also
not inferred from x64 jobs or six-target cross-builds.

## Bindings incident evidence boundary

The known real path rewritten during the earlier unintended generation was
`/Volumes/H004-R04T-APFS/Users/idy/yesman/.runtime/pi/yesman.ts`. Its post-incident
content matched fixed `.29` source and its mtime matched the recorded incident.
There is no pre-incident runtime hash/snapshot, so that comparison cannot prove
prior customizations were absent, original contents were unchanged, all other
files were untouched or no transient activity occurred. No broad private
vault/session scan or blind restore is used to fill that historical gap.
The early MYGO_GENERATE return remains, independently tested to precede storage
and application goroutines; disabling it creates isolated storage in the known
counterexample. This document narrows the historical claim, not the guard.
