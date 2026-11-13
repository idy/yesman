# yesman

**the agent that says yes and gets things done.**

[Website](https://yesman.idy.dev/) · [Downloads](https://github.com/idy/yesman/releases/latest) · [Website maintenance](WEBSITE.md)

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

Settings, at the foot of the sidebar, has six pages: General (light, dark or the system's appearance, kept in `settings.json` in the data directory, and the language), Repositories (the repo pool: name a GitHub user or organization to tick several of its repositories, or add one by `owner/repo`; they clone in the background, three at a time, and a clone that fails stays listed with its error until you try it again or dismiss it; Fetch all fetches the pool, and it can fetch by itself every 1 to 60 minutes), Storage (where yesman keeps its data, below), pi (pi's packages, below), Import and export (below), and About (the versions yesman runs with, and updates).

yesman is written in English and translated to Simplified Chinese. General › Language chooses one, or Match the system, the default, which shows the first of them among the languages of the system's settings, else English. The window, its menus, the tray and notifications change at once; MyGo keeps the choice (`App.SetLocale`): on macOS as the app's language in System Settings, which AppKit and WebKit also say their own texts in from the next launch, elsewhere in its `locale` file. The page translates itself in [`web/i18n.js`](web/i18n.js), with each message's English as its key in [`web/locales/zh-Hans.js`](web/locales/zh-Hans.js); Go translates its menus, tray, notifications and dialogs with `tr` in [`i18n.go`](i18n.go). A message without a translation shows in English.

The pi page manages [pi packages](https://pi.dev/packages): the extensions, skills, prompt templates and themes pi loads. It has three tabs. Info shows the pi every agent runs, a summary of the installed packages, pi's own settings file and what pi installs npm packages with (its `npmCommand` setting, npm unless set). Installed lists the packages pi's user settings install, with their version, source and what they provide; each can be updated, unless pinned to a version, or removed, and an npm package whose registry has a newer version says so. Browse searches the npm packages tagged `pi-package`, which is what pi's gallery lists, most popular first; a package's details show its README, links and preview, and install it. Install from source takes anything `pi install` does: `npm:name[@version]`, a git repository or URL, or a local path.

Packages are installed for the user, in pi's agent directory (`~/.pi/agent`, or `PI_CODING_AGENT_DIR`), so they load into every yesman agent and into pi wherever you run it. yesman changes them only by running `pi install`, `pi update` and `pi remove`, one at a time in the background; a change that fails stays listed with pi's or npm's error until you try it again or dismiss it. pi loads packages when it starts, so agents already running keep the packages they started with: the page says which, and puts the idle ones to sleep on request, to load the change when they next start.

Import and export saves yesman's configuration as a TOML file, to back it up or carry it to another computer: the appearance, the fetch schedule and the pool's GitHub repositories as `owner/repo`. Workspaces, their knowledge and conversations stay out, as does where yesman keeps its data. Importing a file first shows what it would change, then either merges it, which clones the repositories it lists that the pool lacks and changes the settings it sets, or replaces with it, which also resets the settings it leaves out and removes the pool repositories it does not list. Replace keeps a repository that work depends on, one with a worktree checked out of it (in a workspace or the archive) or a branch with commits no remote has, and says why. It also stops the clones of repositories the file leaves out, except one an agent is waiting for.

```toml
version = 1
appearance = "dark"   # system, light or dark
auto_fetch = 15       # minutes: 0, 1, 5, 15 or 60
repos = ["idy/yesman", "idy/magpie"]
```

In Settings › Storage, **Change…** lets you enter or browse to a data folder, then **Use folder and restart** opens it after restarting. Existing folders with data are accepted; a new folder starts fresh. To keep existing data elsewhere, quit yesman, copy the folder yourself, then reopen yesman and select the copy. Data transfers and any Git worktree path repairs are manual. The chosen location is kept in `yesman/storage.json` under the operating system's user configuration directory (Application Support on macOS, `$XDG_CONFIG_HOME` or `~/.config` on Linux, `%AppData%` on Windows). A launch with `YESMAN_HOME` overrides that choice. Storage also has a compact list of the repo pool, workspace parent, archive, runtime files, settings file and storage configuration, with a Reveal action for each and the full path on hover.

Release builds update themselves through MyGo's updater: they check GitHub releases of `idy/yesman` at launch and every 6 hours, and download and install a newer version in the background. The titlebar then offers to restart into it, and the next launch runs it anyway. Only release builds, made with `mygo build` and signed, do that; development builds (`go run`, `go build`) never check.

## Requirements

- Go 1.27 or later, and git
- Optionally the [GitHub CLI](https://cli.github.com) (`gh`), signed in. yesman asks it which repository a fork came from and which repositories an owner has, private ones too, and clones `owner/repo` over the protocol it is set to use. Without it, yesman asks GitHub's public API, which sees public repositories, and clones over HTTPS.
- [pi](https://pi.dev) 1.5.2 or later on the `PATH` (`npm install -g @earendil-works/pi-coding-agent`), with a model configured. Installing npm packages from Settings › pi also needs the command pi installs them with, npm by default.

The window uses [MyGo](https://github.com/egoist/mygo) v0.9.0 with a vanilla JavaScript page and the [Magpie design system](https://github.com/idy/magpie-design-system) v0.4.4, vendored in `web/mds/`. The editor is [CodeMirror 6](https://codemirror.net), bundled into `web/vendor/codemirror/` by `tools/codemirror/build.sh` from the exact versions in `tools/codemirror/package.json`. There is no frontend build step.

## Run

```bash
go run .
```

| Variable | Effect |
| --- | --- |
| `YESMAN_HOME` | Override the data directory; otherwise use the folder chosen in Settings, default `~/yesman` |
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

Automation cannot reach native file dialogs, so in such builds `YESMAN_E2E_SAVE` and `YESMAN_E2E_OPEN` name the files that Export and Import use instead of asking.

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
| `settings.go`, `storage.go`, `internal/home/` | Settings and About, the remembered data location and directory layout |
| `pipackages.go`, `internal/pipkg`, `web/pi.js` | Settings › pi: pi's packages, installed, updated and removed through pi, and found in the npm registry |
| `config.go` | The configuration file: export, and import by merging or replacing |
| `update.go` | Checking for and installing new versions |
| `files.go`, `files_menu.go` | Listing, reading, finding and watching a workspace's files, and the files pane's menus |
| `web/files.js`, `web/editor.js`, `web/code.js` | The files pane, its editor, and the language of each file |
| `i18n.go`, `web/i18n.js`, `web/locales/` | The languages yesman is written in, and their translations |
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
