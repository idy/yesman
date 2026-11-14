# GitHub App identity: local setup helper

This helper registers a **user-owned** GitHub App and can create a PR with its installation credential. It does not yet change desktop settings, agent tools, `gh`, Git credentials, merging, or releases. Desktop integration and a live identity test are required before enabling this as the default agent PR path.

## Registration

Run outside an agent workspace's credential directories, using a new, dedicated directory under your own account:

```sh
go run ./tools/github-app register \
  --owner idy --name 'yesman by idy' \
  --directory '/absolute/private/path/yesman-github-app'
```

Open the printed loopback URL within 30 minutes and continue to GitHub. Sign in as the selected personal owner. GitHub lets you edit the display name before creating the App. A GitHub username already held by somebody else cannot be used as the App name; a missing public `/apps/slug` page does not prove availability.

The manifest requests only:

- Pull requests: read and write.
- Contents: read only.
- Metadata: GitHub's automatic read permission.

The App is private, with webhooks disabled, no subscribed events, no user OAuth authorization, and `https://github.com/idy/yesman` as its homepage. It creates PRs from branches already pushed using the existing Git credentials.

After creation, use the provided installation URL and choose **Only select repositories → idy/yesman** for the initial test. In the App's settings, upload the existing `resources/icon.png` under Display information and set the avatar. This is the project's 1024px yak artwork, not an Action branding icon.

The exchange saves only public `app.json` metadata and `private-key.pem` in the dedicated `0700` directory, with both files `0600`. It discards unneeded OAuth and webhook secrets. Existing registrations are never overwritten. Do not move these files into a repository, configuration export, public installer, or Settings → Secrets: that vault supplies shell environments. The standalone helper fails closed on Windows pending credential-store integration; cross-compilation alone does not establish secure Windows storage.

The local callback uses a random state, rejects duplicate state/code parameters and wrong Host headers, accepts one exchange, disables response caching, and never logs its callback URL, exchange body, private key, JWT or token. A browser navigation loss does not cancel an in-progress one-time exchange. Stop the helper with Ctrl+C; it also expires after 30 minutes. If an exchange fails, inspect the App registration before starting another one, as GitHub may already have created it.

## Verify a connection and create a draft

```sh
go run ./tools/github-app test \
  --directory '/absolute/private/path/yesman-github-app' --repo idy/yesman
```

The result contains public App identity, repository, installation ID and expiration. It validates an actual repository API call using the installation token. Authentication and permissions failures do not fall back to personal credentials.

For an authorized smoke test, push a separate synthetic branch first, then prepare a non-secret JSON file:

```json
{
  "repository": "idy/yesman",
  "head": "test/github-app-identity",
  "base": "main",
  "title": "test: GitHub App identity smoke test",
  "body": "Synthetic draft for verifying the yesman App author and avatar. Do not merge.",
  "draft": true
}
```

```sh
go run ./tools/github-app create-pr \
  --directory '/absolute/private/path/yesman-github-app' \
  --repo idy/yesman --input /absolute/path/pull.json
```

The PR request is made directly by the Go client with an installation token; the token is never passed to the shell, pi, a frontend, or a model. The response must identify a `Bot` author matching the registered App slug. Preserve the returned PR URL, `user.login`, `user.type`, avatar/profile URLs and source SHA as evidence. Verify the live PR independently and close the synthetic draft after testing; do not merge it or delete the retained reference.

The current repository's OpenAI dispatcher skips draft PRs. A draft proves identity without proving ready-PR review admission or the full merge gates. A real ready feature PR must still receive the existing CI and current-head reviews.

## Credential behavior

The client allowlist consists of explicit `owner/repo` names. It looks up the installation for the requested repository, validates its App and owner, and mints a token narrowed to that repository and the two requested permissions. Cache entries are per repository, including when different owners install the same App. It refreshes with a one-minute expiry margin and coalesces concurrent mints. A canceled waiter does not cancel another caller's mint; shared mint attempts have a 30-second timeout.

An authentication rejection invalidates only the token used by that request and permits one remint/retry. Permission errors, missing installations, suspension, redirects, other HTTP errors and ambiguous network failures are reported without personal fallback or write retries. Responses and request URLs are omitted from errors to prevent reflected credentials. Removing an installation or revoking the key causes subsequent operations to fail; previously cached tokens may remain valid until GitHub revokes them or they expire.

## Validation

```sh
go test -race ./internal/githubapp ./tools/github-app
go vet ./internal/githubapp ./tools/github-app
GOOS=linux go build ./tools/github-app
GOOS=windows go build ./tools/github-app
```

The tests use local mock GitHub endpoints and generated fixture keys. They cover callback replay/expiry/CSRF, owner checks, private Unix storage, JWT signatures, exact mint permissions and repository scope, concurrent refresh/cancellation, token invalidation, denied requests, output redaction and PR-author checks. They do not substitute for the user's real App installation test, browser setup or Windows credential-store support.
