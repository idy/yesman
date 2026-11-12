# Project guidance

Read [README.md](README.md) when starting work in this repository. Read [docs/review-workflow.md](docs/review-workflow.md) before changing automated review or interpreting its checks.

- The product and repository name is always lowercase: **yesman**.
- A yesman is one workspace directory for one project, with a lead agent, workers, and shared knowledge. Its worktrees may come from any repository in the pool.
- The repo pool holds bare repositories that only yesman writes, and takes only GitHub repositories. Agents get worktrees through yesman, never by cloning into or writing to the pool.
- Agents are pi processes in RPC mode, children of the app. herdr is not used.
- The app is Go with MyGo v0.9.0. The page is vanilla JavaScript without a framework or build step, styled with Magpie v0.4.4 vendored in `web/mds/`; follow Magpie's tokens and typography rather than adding fonts or weights.
- Keep the app cross-platform: macOS, Linux and Windows must build (`GOOS=linux go build ./...`, `GOOS=windows go build ./...`).
- Pull request watching, service keys, and agents acting without the user are not designed yet. Confirm the direction with the user before building them.
