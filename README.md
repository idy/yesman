# yesman

**the agent that says yes and gets things done.**

yesman is a tray app for hiring yesmen. A yesman takes on one project: it gets a workspace directory of its own, checks out whichever repositories the project needs from a shared repo pool, keeps notes its agents share, and starts workers when the job splits up. You talk to its lead in a Claude Code-style window.

> Status: early prototype. Hiring, the repo pool, worktrees, lead and worker agents, shared knowledge, and the window work on macOS. The app also compiles for Linux and Windows, but only macOS has been run. Watching pull requests and managing service keys are not built yet.

## How it works

| Piece | What it is |
| --- | --- |
| Repo pool | GitHub repositories, kept as bare repositories under `repos/` and written only by yesman. A repository is fetched whenever someone checks it out of the pool, and all of them with Fetch all, or every few minutes when Settings › Repositories says so. A fork also fetches the repository it was forked from, as its `upstream` remote. |
| Yesman | A workspace directory under `yesmen/<name>/`. Its worktrees, of any pool repositories, are checked out into it with `git worktree`, each on its own branch. |
| Lead | A [pi](https://pi.dev) agent in RPC mode, working in the workspace. You talk to it; it plans, keeps knowledge, and delegates. |
| Workers | More pi agents in the same workspace, started by the lead for one task each. A worker's final message is sent to its lead when it finishes a run. |
| Knowledge | `.yesman/knowledge/`, notes every agent of the yesman reads and writes, listed in `INDEX.md`. |
| Sessions | Each agent's pi session log in `.yesman/sessions/<agent>/`. Agents resume their latest session when they restart. |

Every agent runs your installed `pi` with your usual pi configuration (models, plugins, MCP servers), plus yesman's extension, [`pi/yesman.ts`](pi/yesman.ts). Before each run the extension gives the agent its yesman's context: the workspace, its worktrees, the pool, the knowledge index and the other agents. It also adds these tools, which call the app's loopback API:

- `yesman_repos`, `yesman_repo_add`, `yesman_checkout`, `yesman_worktree_remove`, `yesman_agents`
- for leads: `yesman_worker_start`, `yesman_worker_send`, `yesman_worker_remove`

The composer lets you choose the current agent's model and reasoning effort from the right side below the input box. Models come from pi's configured, authenticated catalog, grouped only when there is more than one provider; effort shows only the levels the selected model supports. Change them between runs without starting a new conversation. pi saves the selection in that agent's session once it has messages, and restores it when the agent resumes.

The context meter beside these choices opens pi's estimated current context usage and model limit, with free space, cumulative input/output and cache usage, and message/tool counts. It refreshes after messages, tool results and compaction. Current context and cumulative session totals are kept separate; when pi cannot report the current context size, such as just after compaction, the viewer shows it as unknown.

The app keeps running in the tray with its window closed. Agents are child processes of the app, so quitting it stops them; they resume from their sessions next time.

The files pane, opened from the titlebar, shows the workspace of the yesman you are talking to as a tree, and opens its files in tabs: text and code in an editor with syntax highlighting that saves with Cmd/Ctrl+S or by itself as you type, Markdown rendered, images shown. It follows changes as agents make them, and never saves over a file that changed on disk since it was opened without asking. Large files stay quick: the editor draws only the lines in view, and files over 5 MB open read-only at their first 512 KB. Each folder in the path above a file opens a list of that folder. Cmd/Ctrl+P goes to a file by name, and its menu checks out repositories, opens the knowledge notes and dismisses the yesman. Its edges can be dragged.

Settings, at the foot of the sidebar, has three pages: General (light, dark or the system's appearance, kept in `settings.json` in the data directory, and where yesman keeps its data), Repositories (the repo pool: name a GitHub user or organization to tick several of its repositories, or add one by `owner/repo`; they clone in the background, three at a time, and a clone that fails stays listed with its error until you try it again or dismiss it; Fetch all fetches the pool, and it can fetch by itself every 1 to 60 minutes), and About (the versions yesman runs with, and updates).

Release builds update themselves through MyGo's updater: they check GitHub releases of `idy/yesman` at launch and every 6 hours, and download and install a newer version in the background. The titlebar then offers to restart into it, and the next launch runs it anyway. Only release builds, made with `mygo build` and signed, do that; development builds (`go run`, `go build`) never check.

## Requirements

- Go 1.27 or later, and git
- Optionally the [GitHub CLI](https://cli.github.com) (`gh`), signed in. yesman asks it which repository a fork came from and which repositories an owner has, private ones too, and clones `owner/repo` over the protocol it is set to use. Without it, yesman asks GitHub's public API, which sees public repositories, and clones over HTTPS.
- [pi](https://pi.dev) 1.5.2 or later on the `PATH` (`npm install -g @earendil-works/pi-coding-agent`), with a model configured

The window uses [MyGo](https://github.com/egoist/mygo) v0.9.0 with a vanilla JavaScript page and the [Magpie design system](https://github.com/idy/magpie-design-system) v0.4.4, vendored in `web/mds/`. The editor is [CodeMirror 6](https://codemirror.net), bundled into `web/vendor/codemirror/` by `tools/codemirror/build.sh` from the exact versions in `tools/codemirror/package.json`. There is no frontend build step.

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

## Release

`mygo.json` describes the app for `mygo build`: its name, identifier, version, and where its updates come from. To release a version:

1. Set `version` in `mygo.json` and add its section to `CHANGELOG.md`, which installed copies show as the release notes.
2. Merge that, then push the tag: `git tag v0.2.0 && git push origin v0.2.0`.
3. [`.github/workflows/release.yml`](.github/workflows/release.yml) builds macOS, Windows and Linux and uploads them, with their signed updates, to a draft release.
4. Look the draft over and publish it. Installed copies then update to it.

Updates are signed with a key whose secret half is the repository's `MYGO_UPDATER_PRIVATE_KEY` secret, with a copy kept outside the repository. Installed copies accept only updates signed with it, so it must not be lost or replaced.

To build a release locally, without uploading: `go tool mygo build`, with the key in `MYGO_UPDATER_PRIVATE_KEY` for the update files.

The builds are not yet signed for other machines: macOS apps are signed ad hoc, which Gatekeeper blocks on first open from a download, and Windows apps have no signature, so SmartScreen warns.

## Key files

| Path | Contents |
| --- | --- |
| `main.go`, `app.go`, `service.go` | App startup, tray, window and notifications; the methods the page calls |
| `settings.go` | The settings file and the Settings and About pages' methods |
| `update.go` | Checking for and installing new versions |
| `files.go`, `files_menu.go` | Listing, reading, finding and watching a workspace's files, and the files pane's menus |
| `web/files.js`, `web/editor.js`, `web/code.js` | The files pane, its editor, and the language of each file |
| `tools/codemirror/` | Rebuilds the vendored CodeMirror bundle |
| `mygo.json`, `resources/`, `icons.go` | Packaging, the yak app icon and the monochrome tray template; see [icon sources](resources/README.md) |
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

[MIT](LICENSE). Magpie's files in `web/mds/` and CodeMirror in `web/vendor/codemirror/` keep their own licenses.
