# GitHub App identity for agent pull requests

Settings → **GitHub App** lets you use your own App's bot name and avatar when yesman agents run `gh pr create`. It is opt-in for explicitly selected workspaces and repositories. Every desktop user supplies or registers their own App; the installer contains no shared private key or token and needs no public token server.

## Set up

1. Set up the separate App key storage. Leave the password empty to use the operating system's credential store (macOS Keychain, Windows Credential Manager, or Linux Secret Service). Without one, choose a password of at least eight characters and enter it after each restart.
2. Either **Start registration** with a personal GitHub owner and App name, or enter an existing App's numeric ID and **Choose key file…**. The native file chooser reads the PEM directly in Go; its contents never pass through the page. Import verifies the key and App identity against GitHub before saving.
3. Open the registration page, complete GitHub login/confirmation, and install with **Only select repositories**. Select only the repositories you intend to use. The local registration link expires after 30 minutes; if it expires or a conversion fails after GitHub created the App, inspect that registration before creating another.
4. Use **Save App logo** to save the existing yesman yak artwork, then upload it under the GitHub App's **Display information** and set the avatar. An App's registered name/slug and avatar determine its bot appearance.
5. Enter explicit `owner/repo` names, select the intended workspaces, and **Save scope**. Then **Test connection** and **Enable App identity**. Changing the key or scope disables App identity until a fresh connection test and explicit enable. Renaming a workspace keeps its selection; recreating one requires saving and testing its new scope.

For the initial project setup, the owner is `idy`, the suggested display name is `yesman by idy`, and the installation/scope is only `idy/yesman`. The bare `yesman` username is held by another GitHub account and cannot be used as idy's App name. GitHub's registration form determines final availability and permits editing the proposed name.

The manifest creates a private personal App, with `https://github.com/idy/yesman` as the homepage, no user OAuth authorization, no webhook endpoint and no event subscriptions. Required repository permissions are exactly:

- Pull requests: read and write.
- Contents: read only.
- Metadata: GitHub's automatic read permission.

No Issues, Checks, Actions, organization or account permissions are requested. The read-only Contents permission does not push branches. Push the branch using the existing Git credentials before creating the PR.

## What enabling changes

For selected workspaces, including workers already running the current extension, the next standard `gh pr create` uses the App even when `GH_TOKEN` or `GITHUB_TOKEN` is configured. Other workspaces, other gh subcommands, Git pushes, pool fetches, merging and releases retain their original credentials. There is no global `gh auth` switch, token replacement, Git config change or repository remote rewrite. Disable App identity to return subsequent creations to the existing personal credential mode. Locking retains the selected App mode and makes creation fail until unlocked; it never silently changes the author to a human.

The supported path is noninteractive creation with an already-pushed head:

```sh
gh pr create --repo idy/yesman --head feat/example --base main \
  --title 'feat: Example' --body-file /path/to/pr-body.md --draft
```

Supported options are `--repo/-R`, `--head/-H`, `--base/-B`, `--title/-t`, `--body/-b`, `--body-file/-F` (including `-` for stdin), and `--draft/-d`. Repository flags may precede or follow the subcommand, including `gh pr --repo owner/repo create`; attached short values and `--flag=value` are accepted. Repository, head, base and title must be explicit. Unsupported flags, duplicate options, interactive creation and implicit branch pushing fail clearly in App mode. This is the normal yesman shell path; absolute gh paths, aliases that create PRs, direct API calls, remote shells and third-party plugins' own command tools are not rewritten. Agent context directs creation through the supported path.

The app prepares a private `gh` executable wrapper under its runtime directory and prefixes only its agents' command PATH. A wrapper installation failure leaves the desktop running. If a saved App mode needs the missing wrapper, commands in that enabled or unverifiable scope are refused before the shell starts; scopes confirmed to use existing credentials keep working. Repair the wrapper and restart, or explicitly disable App identity in Settings. A missing wrapper cannot enable a new App mode.

The wrapper checks the authenticated caller's workspace mode through the loopback API. In personal mode it preserves arguments, stdin and the command environment. In App mode it sends only the PR fields to Go. The backend runs the installed real gh in an isolated directory with a repository-scoped installation token in that child alone. No App token reaches the wrapper, pi, the shell command environment, an extension, the model, or the webview. After gh creates the PR, a repository API read verifies the Bot author and registered App slug before reporting success.

An unavailable broker, a conflicting non-github.com `GH_HOST`, or unreadable App configuration fails without personal fallback. A changed/locked/disabled configuration cancels outstanding App operations. Archiving a workspace, removing a worker or stopping its process also cancels and drains its App operations; already-admitted requests cannot borrow a replacement actor or workspace. An interrupted write may already have reached GitHub, so its error directs the caller to inspect the repository before retrying; creation is never blindly replayed.

## Key and token lifecycle

App metadata and the private key are encrypted together with the existing age/sops vault implementation in a **separate** `github-app/vault/` directory under the yesman data folder. The vault password has its own `github-app-password/…` namespace in the native credential store. It is not the shell Secrets vault, not exposed by `yesman_env`, and not included in normal configuration exports. Imports/exports of other settings do not enable, replace or erase this App configuration. Keep the original key or generate a replacement in GitHub when moving to a different machine.

The backend's client allowlist consists of explicit `owner/repo` names. It looks up each repository's installation, validates the App, owner and suspension state, and mints a token narrowed to that one repository and the requested permissions. Cache entries are per repository, including across different installation owners. Tokens refresh with a one-minute safety margin; concurrent requests share a mint. Canceling one waiter does not cancel other agents' mint; the shared request has its own 30-second deadline.

An HTTP 401 invalidates only the token used by that request and permits one authentication refresh before a write. Missing/suspended installations, insufficient permissions, redirects and other HTTP/transport failures do not fall back to a personal token. Revocation or uninstall causes subsequent operations to fail; a still-valid cached token can remain usable until GitHub invalidates it or it expires. Locking drops the cached client. Disabling this local mode does not uninstall the GitHub App or revoke its key.

Registration uses random state, one conversion, duplicate parameter and Host validation, no-store/referrer/CSP headers, and a bounded loopback listener. Codes, callback URLs with codes, response bodies, PEM, JWTs and installation tokens are never logged. A canceled or superseded registration cannot replace a newer key. Native import and a replacement key disable any old selected identity before storage changes.

## Validate the real identity

A connection test is read-only and does not create a PR. Starting a new test clears the prior verification; only the latest successful test of the current key and scope can permit Enable. Failure or cancellation clears stale connection details. For an authorized smoke test, create a distinct synthetic branch/commit with the existing push identity, then create a clearly titled **draft** PR using the selected App path. Independently verify REST `user.type = Bot`, the expected `user.login = <slug>[bot]`, avatar/profile URLs and the head repository/branch/SHA. Preserve the PR link; close the synthetic draft afterward without merging it or deleting its reference.

The current yesman OpenAI dispatcher skips draft PRs. A draft proves the author/avatar path, not ready-PR review admission or merge gates. App-created events must still trigger normal CI and current-head reviews; this App must not serve as an independent reviewer of its own PR. Ordinary GitHub Apps do not automatically join GitHub's partner Agents selector, and historical PR/comment authors are not rewritten.

## Development checks and standalone helper

```sh
go test -race ./internal/githubapp ./internal/agent ./internal/server .
go vet ./...
node --test scripts/*.test.mjs
node scripts/check-github-app-pi.mjs
YESMAN_TEST_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node scripts/check-github-app-browser.mjs
GOOS=linux go build ./...
GOOS=windows go build ./...
```

Tests use generated keys and mock GitHub endpoints. They exercise the installed real gh through an isolated TLS proxy, the compiled desktop wrapper through actual pi with a deterministic local provider, native-import wire shape and vault isolation, and production Chromium/WebKit page behavior. On Windows the network fixture uses gh's supported local socket transport, keeping the github.com request host and installation credential without modifying OS certificate stores or waiting for security dialogs. The production transport and TLS settings are unchanged. These tests do not substitute for the real App installation/author/avatar test.

`go run ./tools/github-app` remains a macOS/Linux developer setup helper with `register`, `test`, and `create-pr`. Pass a new absolute `--directory` outside workspaces. Its standalone registration stores PEM in dedicated `0700/0600` files; the desktop uses the encrypted vault on every platform instead. The helper never overrides existing registrations, global gh/Git authentication or environment secrets. Import its saved PEM using the desktop's native chooser and the public App ID from `app.json`.
