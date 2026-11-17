# macOS Developer ID signing and notarization

Release builds of yesman for macOS can be signed with the owner's **Developer ID Application** certificate and notarized by Apple. Configuration is optional. Until it is set, releases stay signed ad hoc, exactly as before.

Signing gives every release the same publisher identity: Apple's anchor, the Developer ID Application certificate type, the team's Team ID and the identifier `dev.idy.yesman`. That identity is what macOS can use to recognize later versions as the same app. Notarization lets Gatekeeper open a downloaded copy without warnings. Neither changes the updater: the Ed25519 key in `mygo.json` `updates.publicKey` still authenticates every archive and delta, and stays unchanged.

## Modes

The source decides. `packaging/macos/signing.json` holds `{"teamId": ""}` or the team's 10-character Team ID; the tag's own copy of it says how that release is signed and checked.

| Configuration | Release macOS job |
| --- | --- |
| No `teamId` and none of the four secrets below | Ad hoc, as before |
| A `teamId` and all four secrets, valid | `-sign <certificate SHA-1>`; MyGo signs each app with the hardened runtime and a secure timestamp, signs and notarizes each disk image and staples both, then writes the update archives, deltas, checksums and manifests |
| Only some of them, or any check failing | The job fails. It never falls back to ad hoc |

The tracked `mygo.json` stays the ad-hoc configuration: a plain `go tool mygo build` needs no flag or input. Only the release launcher, given a keychain that setup prepared, adds `macos.notarize` to a derived configuration (below).

The secrets, under the repository's **Settings › Secrets and variables › Actions › Secrets**:

| Name | Value |
| --- | --- |
| `MACOS_SIGNING_CERTIFICATE_P12` | Base64 of a `.p12` holding only the **Developer ID Application** certificate and its private key |
| `MACOS_SIGNING_CERTIFICATE_PASSWORD` | The password chosen when exporting that `.p12` |
| `MACOS_NOTARY_APPLE_ID` | The Apple Account email of the membership |
| `MACOS_NOTARY_PASSWORD` | An app-specific password made for this at account.apple.com |

The Team ID is public (it appears in every signature) and is committed, not a repository variable, so that a variable changed later cannot change how an already built release is judged.

A free Xcode “Personal Team”, an Apple Development, Mac App Distribution or Developer ID Installer certificate cannot be used: setup rejects them. App Store Connect *individual* API keys cannot use notarytool, so notarization uses the Apple Account with an app-specific password.

## What the release job does

1. **Prepare macOS signing** (`scripts/macos_signing.py setup`) is the only step that receives the four secrets. It refuses to run anywhere but a GitHub-hosted runner, because it changes the account's keychain search list. It makes a fresh directory of its own under `RUNNER_TEMP` (`0700`) with a `0600` receipt holding a random nonce, and writes the directory's path, device, inode and nonce as the step output `state` before creating anything else. In that directory it creates a keychain with a random password, writes the `.p12` to a `0600` file, imports it and deletes the file. The key's access list names `/usr/bin/codesign` (`-T`), and its partition list `apple-tool:,apple:,codesign:` lets Apple-signed tools and codesign use it without a prompt; the keychain goes first in the search list, where MyGo's `codesign` finds it. Setup then requires exactly one identity, valid on the runner, named `Developer ID Application: … (<Team ID>)`, signs a scratch file with it as MyGo will, and has `codesign` evaluate Apple's Developer ID Application requirement for that team. Last, it stores the notary profile `yesman-release-notary` in the same keychain. Its other outputs are the mode, the certificate's SHA-1, the team and the certificate name; none is secret.
2. **Build and upload** runs `scripts/release_build.py` on every row. It receives no Apple secret; see below for the updater key and upload token. In Developer ID mode it first checks that `state` still names the directory setup made and its keychain, then builds with `-sign <SHA-1>` and the derived configuration's `macos.notarize` naming that keychain. A signing, notarization (anything but `Accepted`) or stapling failure stops MyGo before it writes that architecture's update files or uploads anything.
3. **Remove the signing keychain** always runs, given `state`. It deletes the keychain, takes it out of the search list and deletes the directory, only after checking that the directory is still the one setup made: not a link, the same device and inode, owned by the runner's user, closed to others, and holding a regular-file receipt with the same nonce. Anything else is left alone and fails the step. Without a `state` (ad hoc, or setup refused before making anything) it does nothing.
4. **Verify the Developer ID signatures and notarization** then checks both architectures' app and disk image: not ad hoc, the expected team and certificate, Apple's Developer ID requirement, hardened runtime, secure timestamp, a valid stapled ticket and Gatekeeper's “Notarized Developer ID” verdict. Each app's designated requirement must be, as `csreq` compiles it, exactly the Developer ID requirement of the team and identifier (an `always` or team-less requirement fails even when both architectures agree), and each app must satisfy the other's. The evidence is kept as the run's `macos-signing` artifact.

MyGo uploads within step 2, so a failure in step 4 happens after the upload. Any failed job means the draft must not be published; per the release policy, the tag is not moved or reused.

The draft gate `.github/workflows/macos-draft-smoke.yml` checks out the tag's source and reads its `packaging/macos/signing.json`, with no variable or secret. With a Team ID, the downloaded archives must pass the same signature, requirement, ticket and Gatekeeper checks before their startup runs; without one, they must be signed ad hoc. Tags made before this support carry neither file and keep the earlier check.

### Interruption and limits

- Setup turns `SIGTERM`, `SIGINT` and `SIGHUP` (a cancelled job) into a failure, and on any failure removes its `.p12`, keychain, search-list entry and directory before exiting. Each tool it starts must finish within 300 seconds, or setup fails and cleans up the same way. Tool output kept for an error message is bounded and redacted, and only its last six lines are shown.
- `SIGKILL` cannot be handled. Then the always-run cleanup step removes what setup left, provided the runner recorded setup's `state` output; otherwise the keychain stays on the runner until GitHub discards the VM with the job. Nothing here claims more than that.
- Only GitHub-hosted runners are used: a self-hosted runner would keep a killed job's files, so setup refuses one.

### Credential boundaries

- The four Apple secrets exist only in the setup step's environment. Setup gives every tool an environment without them (and without any name containing `TOKEN`, `SECRET`, `PASSWORD`, `PRIVATE`, `CREDENTIAL` or `P12`), never prints them, and never writes them to step outputs, `GITHUB_ENV`, artifacts or the workspace. It reports failures without argument lists and redacts secret values from tool output.
- Two tools take a secret only as an argument: `security import -P` (the `.p12` password, for an instant) and `notarytool store-credentials --password` (the app-specific password, while Apple validates it). Another process of the same user on the runner could read those arguments while they run; on a GitHub-hosted runner the VM and its single job are disposable. GitHub masks both values in logs.
- The build step has the updater key `MYGO_UPDATER_PRIVATE_KEY` and the upload token `GH_TOKEN`. MyGo starts go, the compilers, cgo's clang, codesign, xcrun, hdiutil, makensis and gh with its own environment, so `scripts/release_build.py` takes both out of it. The key goes to a `0600` file in a fresh `0700` directory, which the derived configuration names as `updates.privateKey`: only MyGo reads it, to sign the update archives, deltas and manifests. The token goes to a `0600` file there, which a `gh` wrapper first on MyGo's `PATH` reads before running the real `gh`. Every other child gets the step environment without any credential name, and the launcher refuses to start if a credential's value remains under another name. The files are readable by the runner's user while the step runs and are deleted when it ends.
- The derived configuration lives in a fresh directory under the ignored `build/`. It is `mygo.json` with its paths made absolute, `resources/` linked, the changelog and key file named, and in Developer ID mode `macos.notarize`. MyGo takes it as its project directory while `go` still builds this module, so binaries keep the exact source revision, unmodified. A new `mygo.json` key, or anything MyGo discovers next to its configuration (`package.json`, `frontend`, `locales`…), makes the launcher refuse until it is taught to carry it.
- No secret reaches CI, pull-request code, review, Pages, the draft smoke jobs, npm, pi or the app.

## Owner setup, once

1. In the Apple Developer account of the personal membership, check under **Membership details** that the program is active and note the **Team ID**.
2. Under **Certificates, Identifiers & Profiles**, create a **Developer ID Application** certificate (only the Account Holder can) or use the existing one, then export it with its private key from Keychain Access on the Mac as a `.p12` with a new password.
3. At account.apple.com › Sign-In and Security › App-Specific Passwords, make one named for example “yesman notarization”.
4. In GitHub, add the four secrets above. The `.p12` must be pasted as base64: on the Mac, `base64 -i <file>.p12 | pbcopy` copies it.
5. Commit the Team ID to `packaging/macos/signing.json` in a normal pull request, before the next release tag.
6. Delete the exported `.p12` file afterwards; the certificate itself stays in the login keychain.

The next release tag then builds signed. Setting the secrets without the Team ID, or the Team ID without the secrets, fails the release instead of signing ad hoc. Revoking the app-specific password or the certificate stops signing; clearing the Team ID **and** deleting all four secrets returns to ad hoc.

## Permissions across updates

macOS keeps some permissions, such as Accessibility and Screen Recording, against the app's code requirement. An ad-hoc build's requirement is its exact code hash, so a new build cannot satisfy the requirement saved for an old one. The first Developer ID release is therefore a one-time migration: the permissions granted to ad-hoc versions must be granted again. After that, releases from the same team and identifier share one designated requirement. That they keep permissions across a real update is to be shown by updating one signed build to another on a test Mac; requirement equality alone does not prove it. Renewing the certificate within the same team keeps the requirement; check it before switching.

## Local builds

Local and CI builds are ad hoc and need no flag:

```sh
CGO_ENABLED=1 CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0' MACOSX_DEPLOYMENT_TARGET=13.0 go tool mygo build
```

## Tests

`python3 scripts/test_macos_signing.py` (macOS) checks setup, cleanup and its ownership checks (forged, linked, copied or replaced directories and receipts are left alone; a signal or deadline during setup leaves nothing), verification including weak requirements, the launcher's arguments, derived configuration, credential files and refusals with a stand-in `go`, the draft gate's step script, and that the Apple secrets reach only the setup step. `--mygo` also runs the real MyGo build of a committed copy of the repository through the launcher, with stand-in Apple tools and `gh`: a plain `go tool mygo build` with no input; both architectures signed, notarized and stapled before the update archives, whose updater signatures verify; each failure stopping the build before any update file or upload; the binaries' exact, unmodified source revision; the same ad-hoc app, Linux and Windows files as the tracked configuration; and no credential name or value in the environment of `go` or any cgo compiler run. `--real-keychain`, only on a GitHub-hosted runner, has the real keychain tools refuse a synthetic self-signed “Developer ID Application” certificate and leave nothing behind. CI runs all three on both macOS architectures. None uses a real certificate or contacts Apple; a real signed release still has to be verified as described above.
