# Automated review

The repository uses `.github/workflows/codex-review.yml` as a trusted caller for the maintained [GizClaw reviewer](https://github.com/GizClaw/github-workflows/blob/latest/README.md).

## Configuration

Both reusable review jobs reference:

```yaml
uses: GizClaw/github-workflows/.github/workflows/codex-openai-review.yml@latest
```

The repository's Actions secret `OPENAI_API_KEY` is forwarded explicitly. Never put its value in this repository, workflow inputs, PR comments, or logs. Do not replace the explicit secret mapping with `secrets: inherit`.

Model selection, reasoning effort, CLI version, and shared review policy are maintained upstream. Repository-specific instructions cover the permanent lead model, independent memory, human triage, host ownership, Herdr, Magpie, and the current design-only stage.

## Triggers

- Opening, reopening, updating, editing, or marking a PR ready requests review automatically.
- A new PR comment containing `@codex review` on its own line requests review manually.
- Editing a linked Issue refreshes the open PRs GitHub associates with closing that Issue.
- Actions **Run workflow** accepts `pull_request_number` as a manual fallback. Leave `dispatch_source_run_id` empty for a human-initiated run.

Closing Issues are optional. Draft and closed PRs are not review targets. PR titles use a lowercase `prefix: Subject` form, and the body should describe the result and relevant validation.

## Trust and concurrency

The PR-event job dispatches the worker on the repository's trusted default branch. The shared reviewer treats the PR diff as data and does not execute PR-head code. Keep this caller isolated from jobs that execute untrusted PR code with the provider secret.

The canonical caller keeps separate dispatcher and worker queues and preserves pending requests with `queue: max`. Direct review requests and linked-Issue refreshes also share a per-PR job lock. Do not remove these locks or silently cancel pending review approvals.

## Results

The shared reviewer publishes three checks against the reviewed PR head:

- `OpenAI PR Review`
- `OpenAI Issue Review`
- `OpenAI Code Review`

Inspect their conclusions and the completed native review, not just a request reaction or a successful dispatcher. A PR without closing Issues can pass the Issue stage without an Issue-model turn.

A fresh request or descendant commit can reuse compatible review evidence. Use a new comment, push, or manual dispatch for another request; rerunning an old workflow attempt is not a fresh request.

## Maintenance and validation

When changing the integration, compare the current upstream README and copyable `openai-pr-review-dispatch.yml`. Keep reusable references on `@latest` and change the repository's review instructions in both review jobs together.

Validate workflow syntax with `actionlint`, then use a ready PR to verify the default-branch dispatcher, worker, three head checks, and native review. Repository secret metadata confirms that a secret is configured; only a successful live review confirms usable provider access.

`actionlint` v1.7.12 does not recognize the documented `concurrency.queue` field. For that version, compare the queue configuration with the canonical caller and [GitHub's concurrency documentation](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency); any compatibility suppression must be limited to that unsupported key. Do not remove the queues to silence an older validator.

Do not add required-status rules until the actual checks have been verified in this repository. Automated review is evidence for merging, not permission to merge or publish.
