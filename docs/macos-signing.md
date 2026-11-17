# macOS Developer ID signing and notarization

Release builds of yesman for macOS can be signed with the owner's **Developer ID Application** certificate and notarized by Apple. Configuration is optional. Until it is set, releases stay signed ad hoc, exactly as before.

Signing gives every release the same publisher identity: Apple's anchor, the Developer ID Application certificate type, the team's Team ID and the identifier `dev.idy.yesman`. That identity is what macOS can use to recognize later versions as the same app. Notarization lets Gatekeeper open a downloaded copy without warnings. Neither changes the updater: the Ed25519 key in `mygo.json` `updates.publicKey` still authenticates every archive and delta, and stays unchanged.

## Modes

| Configuration | Release macOS job |
| --- | --- |
| None of the five inputs below | Ad hoc, as before: `go tool mygo build … -skip-notarize` |
| All five, valid | `-sign <certificate SHA-1>`; MyGo signs each app with the hardened runtime and a secure timestamp, signs and notarizes each disk image and staples both, then writes the update archives, deltas, checksums and manifests |
| Some of them, or any check failing | The job fails. It never falls back to ad hoc |

The inputs, under the repository's **Settings › Secrets and variables › Actions**:

| Name | Kind | Value |
| --- | --- | --- |
| `MACOS_SIGNING_TEAM_ID` | Variable | The 10-character Team ID of the paid Apple Developer Program membership (Membership details). Public: it appears in every signature |
| `MACOS_SIGNING_CERTIFICATE_P12` | Secret | Base64 of a `.p12` holding only the **Developer ID Application** certificate and its private key |
| `MACOS_SIGNING_CERTIFICATE_PASSWORD` | Secret | The password chosen when exporting that `.p12` |
| `MACOS_NOTARY_APPLE_ID` | Secret | The Apple Account email of the membership |
| `MACOS_NOTARY_PASSWORD` | Secret | An app-specific password made for this at account.apple.com |

A free Xcode “Personal Team”, an Apple Development, Mac App Distribution or Developer ID Installer certificate cannot be used: setup rejects them. App Store Connect *individual* API keys cannot use notarytool, so notarization uses the Apple Account with an app-specific password.

## What the release job does

1. **Prepare macOS signing** (`scripts/macos_signing.py setup`) is the only step that receives the secrets. It makes the keychain named by `mygo.json` `macos.notarize.keychain`, in a new folder only it can read, with a random password. The `.p12` is written to a `0600` file, imported so that only `/usr/bin/codesign` may use the key, and deleted. Setup then requires exactly one identity, valid on the runner, named `Developer ID Application: … (<Team ID>)`, signs a scratch file with it as MyGo will, and has `codesign` evaluate Apple's Developer ID Application requirement for that team. Last, it stores the notary profile `macos.notarize.keychainProfile` in the same keychain. Its outputs are the mode, the certificate's SHA-1, the team and the certificate name; none is secret.
2. **Build and upload** receives no Apple secret. It passes `-sign <SHA-1>` (or `-skip-notarize` when ad hoc). MyGo's notarization reads the profile from the keychain. A signing, notarization (anything but `Accepted`) or stapling failure stops MyGo before it writes that architecture's update files or uploads anything.
3. **Remove the signing keychain** always runs: it deletes the keychain and its folder and takes it out of the search list. Setup also cleans up after its own failure.
4. **Verify the Developer ID signatures and notarization** then checks both architectures' app and disk image: not ad hoc, the expected team and certificate, Apple's Developer ID requirement, hardened runtime, secure timestamp, a valid stapled ticket and Gatekeeper's “Notarized Developer ID” verdict. Both apps must have the same designated requirement, and each must satisfy the other's. The evidence is kept as the run's `macos-signing` artifact.

MyGo uploads within step 2, so a failure in step 4 happens after the upload. Any failed job means the draft must not be published; per the release policy, the tag is not moved or reused.

The draft gate `.github/workflows/macos-draft-smoke.yml` reads only the public `MACOS_SIGNING_TEAM_ID` variable. When it is set, the downloaded archives must also pass the same signature, ticket and Gatekeeper checks before their startup runs.

### Credential boundaries

- Secrets exist only in the setup step's environment. Setup removes them from every tool's environment, never prints them, and never writes them to step outputs, `GITHUB_ENV`, artifacts or the workspace. It reports failures without argument lists and redacts secret values from tool output.
- Two tools take a secret only as an argument: `security import -P` (the `.p12` password, for an instant) and `notarytool store-credentials --password` (the app-specific password, while Apple validates it). On a GitHub-hosted runner the VM and its single job are disposable. GitHub masks both values in logs.
- No secret reaches CI, pull-request code, review, Pages, the draft smoke jobs, npm, pi or the app.

## Owner setup, once

1. In the Apple Developer account of the personal membership, check under **Membership details** that the program is active and note the **Team ID**.
2. Under **Certificates, Identifiers & Profiles**, create a **Developer ID Application** certificate (only the Account Holder can) or use the existing one, then export it with its private key from Keychain Access on the Mac as a `.p12` with a new password.
3. At account.apple.com › Sign-In and Security › App-Specific Passwords, make one named for example “yesman notarization”.
4. In GitHub, add the variable and four secrets above. The `.p12` must be pasted as base64: on the Mac, `base64 -i <file>.p12 | pbcopy` copies it.
5. Delete the exported `.p12` file afterwards; the certificate itself stays in the login keychain.

The next release tag then builds signed. Revoking the app-specific password or the certificate, or deleting the inputs, stops signing; removing **all** inputs returns to ad hoc.

## Permissions across updates

macOS keeps some permissions, such as Accessibility and Screen Recording, against the app's code requirement. An ad-hoc build's requirement is its exact code hash, so a new build cannot satisfy the requirement saved for an old one. The first Developer ID release is therefore a one-time migration: the permissions granted to ad-hoc versions must be granted again. After that, releases from the same team and identifier share one designated requirement. That they keep permissions across a real update is to be shown by updating one signed build to another on a test Mac; requirement equality alone does not prove it. Renewing the certificate within the same team keeps the requirement; check it before switching.

## Local builds

`mygo.json` configures notarization, so a local or CI build without a Developer ID identity passes `-skip-notarize` (MyGo otherwise refuses):

```sh
CGO_ENABLED=1 CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0' MACOSX_DEPLOYMENT_TARGET=13.0 go tool mygo build -skip-notarize
```

## Tests

`python3 scripts/test_macos_signing.py` checks setup, cleanup and verification with stand-in Apple tools, the release workflow's real build and draft-gate step scripts, and that the signing inputs reach only the two steps above. `--mygo` also runs the real MyGo build of a copy of the repository with stand-in Apple tools, for both architectures: signing, notarization and stapling precede the update archives and checksums, each failure stops the build, and the ad-hoc build is unchanged. `--real-keychain`, only on a GitHub-hosted runner, has the real keychain tools refuse a synthetic self-signed “Developer ID Application” certificate and leave nothing behind. CI runs all three on both macOS architectures. None uses a real certificate or contacts Apple; a real signed release still has to be verified as described above.
