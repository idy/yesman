# Project guidance

Read [README.md](README.md) when starting work in this repository. Read [docs/review-workflow.md](docs/review-workflow.md) before changing automated review or interpreting its checks.

- The product and repository name is always lowercase: **yesman**.
- A yesman is one workspace directory for one project, with a lead agent, workers, shared Knowledge for reusable guidance, and a Task log for task/PR history. Its worktrees may come from any repository in the pool.
- The repo pool holds bare repositories that only yesman writes, and takes only GitHub repositories. Agents get worktrees through yesman, never by cloning into or writing to the pool.
- Knowledge (`.yesman/knowledge/`) holds durable conventions, preferences and architecture decisions, such as using the project's Phosphor icon helper and local font assets. A completed task is not a reason to add a note. Keep PR/head/CI/validation/release timelines and temporary blockers in `.yesman/task-log.md` or the board. The lead maintains these shared documents from worker reports; workers report results and reusable discoveries rather than concurrently rewriting the log. See [workspace documents](docs/workspace-documents.md).
- Agents are pi processes in RPC mode, children of the app. herdr is not used.
- The app is Go with MyGo v0.9.0. The page is vanilla JavaScript without a framework or build step, styled with Magpie v0.4.4 vendored in `web/mds/`, with CodeMirror 6 vendored in `web/vendor/codemirror/` for the editor (rebuilt by `tools/codemirror/build.sh`, never by the app); follow Magpie's tokens and typography rather than adding fonts or weights.
- Keep the app cross-platform: macOS, Linux and Windows must build (`GOOS=linux go build ./...`, `GOOS=windows go build ./...`).
- Secrets and variables reach agents only as environment variables of each command, set by the extension's shell tool; a secret's value never enters the context, pi's own environment or a session log. Keep it that way.
- Pull request watching and agents acting without the user are not designed yet. Confirm the direction with the user before building them.
