# yesman

**the agent that says yes and gets things done.**

yesman is a tray app for hiring yesmen. A yesman takes on one project: it gets a workspace directory of its own, checks out whichever repositories the project needs from a shared repo pool, keeps notes its agents share, and starts workers when the job splits up. You talk to its lead in a Claude Code-style window.

> Status: early prototype. Hiring, the repo pool, worktrees, lead and worker agents, shared knowledge, and the window work on macOS. The app also compiles for Linux and Windows, but only macOS has been run. Watching pull requests and managing service keys are not built yet.

## How it works

| Piece | What it is |
| --- | --- |
| Repo pool | GitHub repositories, kept as bare repositories under `repos/` and written only by yesman. A repository is fetched whenever someone checks it out of the pool. A fork also fetches the repository it was forked from, as its `upstream` remote. |
| Yesman | A workspace directory under `yesmen/<name>/`. Its worktrees, of any pool repositories, are checked out into it with `git worktree`, each on its own branch. |
| Lead | A [pi](https://pi.dev) agent in RPC mode, working in the workspace. You talk to it; it plans, keeps knowledge, and delegates. |
| Workers | More pi agents in the same workspace, started by the lead for one task each. A worker's final message is sent to its lead when it finishes a run. |
| Knowledge | `.yesman/knowledge/`, notes every agent of the yesman reads and writes, listed in `INDEX.md`. |
| Sessions | Each agent's pi session log in `.yesman/sessions/<agent>/`. Agents resume their latest session when they restart. |

Every agent runs your installed `pi` with your usual pi configuration (models, plugins, MCP servers), plus yesman's extension, [`pi/yesman.ts`](pi/yesman.ts). Before each run the extension gives the agent its yesman's context: the workspace, its worktrees, the pool, the knowledge index and the other agents. It also adds these tools, which call the app's loopback API:

- `yesman_repos`, `yesman_repo_add`, `yesman_checkout`, `yesman_worktree_remove`, `yesman_agents`
- for leads: `yesman_worker_start`, `yesman_worker_send`, `yesman_worker_remove`

The app keeps running in the tray with its window closed. Agents are child processes of the app, so quitting it stops them; they resume from their sessions next time.

Settings, at the foot of the sidebar, has three pages: General (light, dark or the system's appearance, kept in `settings.json` in the data directory, and where yesman keeps its data), Repositories (the repo pool), and About (the versions yesman runs with).

## Requirements

- Go 1.27 or later, and git
- Optionally the [GitHub CLI](https://cli.github.com) (`gh`), signed in. yesman asks it which repository a fork came from, private ones too, and clones `owner/repo` over the protocol it is set to use. Without it, yesman asks GitHub's public API and clones over HTTPS.
- [pi](https://pi.dev) 1.5 or later on the `PATH` (`npm install -g @earendil-works/pi-coding-agent`), with a model configured

The window uses [MyGo](https://github.com/egoist/mygo) v0.9.0 with a vanilla JavaScript page and the [Magpie design system](https://github.com/idy/magpie-design-system) v0.4.4, vendored in `web/mds/`. There is no frontend build step.

## Run

```bash
go run .
```

| Variable | Effect |
| --- | --- |
| `YESMAN_HOME` | Data directory, default `~/yesman` |
| `YESMAN_PI` | The pi executable, default `pi` from the `PATH` (the app adds your login shell's `PATH`) |
| `YESMAN_WEB_DIR` | Serve the page from this directory instead of the embedded copy, to edit it without rebuilding |
| `YESMAN_HIDDEN` | Start in the tray without opening the window |

## Test

```bash
go test ./...
```

CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) vets, builds and tests on Linux, macOS and Windows for every pull request and every push to `main`. It does not start the app's window or real pi agents.

The end-to-end test starts real pi agents and calls your configured model:

```bash
YESMAN_IT=1 go test ./internal/server -run WithPi -v
```

To drive the real window, build with the `e2e` tag and attach [`mygo-automation`](https://github.com/egoist/mygo/tree/main/plugins/automation):

```bash
go build -tags e2e -o /tmp/yesman . && YESMAN_HOME=/tmp/yesman-home MYGO_AUTOMATION=/tmp/yesman.sock /tmp/yesman
```

## Key files

| Path | Contents |
| --- | --- |
| `main.go`, `app.go`, `service.go` | App startup, tray, window and notifications; the methods the page calls |
| `settings.go` | The settings file and the Settings and About pages' methods |
| `internal/pool` | The repo pool and its worktrees |
| `internal/github` | What the pool takes: GitHub repositories, their clone URLs and a fork's parent |
| `internal/yesmen` | Workspaces, workers and their files |
| `internal/agent` | pi processes in RPC mode and their status |
| `internal/server` | The loopback API for the extension, and the context each agent gets |
| `pi/yesman.ts` | yesman's pi extension |
| `web/` | The page: `index.html`, `app.js`, `markdown.js`, `app.css`, and Magpie in `mds/` |

## Contributing

Use a lowercase `prefix: Subject` pull-request title and describe both the change and its relevant validation. Closing Issues are optional.

Ready pull requests receive automated OpenAI/Codex review through the shared workflow. See the [review guide](docs/review-workflow.md) for manual requests and how to interpret the three checks and completed review on the current head.

## License

[MIT](LICENSE). Magpie's files in `web/mds/` keep their own licenses.
