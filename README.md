<p align="center"><img src="resources/icon.svg" width="128" height="128" alt=""></p>

<h1 align="center">yesman</h1>

<p align="center"><strong>Yes-man Ships!</strong></p>

<p align="center">AI has a reputation for saying yes to everything. Put that yes-man energy to work:<br><strong>hire a yesman to help you ship to production.</strong></p>

<p align="center"><a href="https://yesman.idy.dev/">Website</a> · <a href="https://github.com/idy/yesman/releases/latest">Downloads</a> · <a href="WEBSITE.md">Website maintenance</a></p>

## What makes a yesman different

- **A manager, not a coder.** The lead has no shell and never does the work itself. It only talks with you, plans and manages, so it is always free to answer.
- **It takes the whole job.** It turns your goal into tasks on a board and hands each to a worker, without waiting on any of them.
- **It manages upward, so you don't have to manage down.** Workers bring their questions, failures and reports to the lead, which answers them itself. Workers never interrupt you.
- **It does everything it can, and leaves you only the hard calls.** The decisions it cannot make reach you together, once everything else is done.
- **It works on a project, not a repository.** A yesman checks out worktrees of whichever repositories the project needs into its own workspace, one per worker, each on its own branch.
- **One pool of bare repositories.** Each repository is cloned once, written only by yesman, and shared by every yesman.
- **It picks up where it left off.** Shared knowledge, the board and every agent's session survive restarts.
- **Coming next: always on.** A yesman connected to a channel, talking with users and customers, turning what they need into tasks, and shipping release after release.

yesman is a tray app for hiring yesmen. A yesman takes on one project: it gets a workspace directory of its own, checks out whichever repositories the project needs from a shared repo pool, keeps notes its agents share, and starts workers when the job splits up. You talk to its lead in a Claude Code-style window.

> Status: early prototype. Hiring, the repo pool, worktrees, lead and worker agents, shared knowledge, and the window work on macOS. The app also compiles for Linux and Windows, but only macOS has been run. Watching pull requests is not built yet.

## How it works

| Piece | What it is |
| --- | --- |
| Repo pool | GitHub repositories, kept as bare repositories under `repos/` and written only by yesman. A repository is fetched whenever someone checks it out of the pool, and all of them with Fetch all, or every few minutes when Settings › Repositories says so. A fork also fetches the repository it was forked from, as its `upstream` remote. |
| Yesman | A workspace directory under `yesmen/<name>/`. Its worktrees, of any pool repositories, are checked out into it with `git worktree`, each on its own branch. |
| Lead | A [pi](https://pi.dev) agent in RPC mode, working in the workspace. You talk to it; it plans, keeps the board and knowledge, and delegates. It never does the work itself, so it stays free to talk: it has no shell, and writes only knowledge notes. It answers for its workers and sees the work through: it takes their questions and failures, decides what it can, and brings you only the decisions it cannot make, together, once everything else is done. |
| Workers | More pi agents in the same workspace, started by the lead for one task each, with a title naming its subject. The lead never waits for them: a worker's final message is sent to its lead when it finishes a run, and the lead is also told when a worker's run is stopped by you or its process fails. Once its report reaches the lead, a worker's process closes and it shows as done; a message to it starts it again with its session. |
| Knowledge | `.yesman/knowledge/`, notes every agent of the yesman reads and writes, listed in `INDEX.md`. |
| Board | `.yesman/board.json`, the yesman's task list: each task has a status (todo, doing, blocked, done, dropped), an owner, and a note on where it stands. The lead adds tasks and hands them to workers; workers keep their own tasks current. It is the plan, not what the agents are doing now. |
| Sessions | Each agent's pi session log in `.yesman/sessions/<agent>/`. Agents resume their latest session when they restart. |
| Secrets and variables | Environment variables every agent's commands get, edited in Settings: secrets in `secrets/secrets.sops.yaml`, encrypted, and variables in `variables.yaml`. |

Every agent runs your installed `pi` with your usual pi configuration (models, plugins, MCP servers), plus yesman's extension, [`pi/yesman.ts`](pi/yesman.ts). Before each run the extension gives the agent its yesman's context: the workspace, its worktrees, the pool, the knowledge index and the other agents. It also adds these tools, which call the app's loopback API:

- `yesman_repos`, `yesman_repo_add`, `yesman_checkout`, `yesman_worktree_remove`, `yesman_agents`, `yesman_board`, `yesman_env`
- for leads: `yesman_worker_start`, `yesman_worker_send`, `yesman_worker_answer`, `yesman_worker_remove`

For a lead, the extension also takes away pi's `bash` and adds the read-only `grep`, `find` and `ls`, and refuses `edit` and `write` outside `.yesman/knowledge/`. Tools from your own pi configuration stay.

Only a lead turns to you. A question a worker's tools ask (an extension dialog) goes to its lead as a message, which answers it with `yesman_worker_answer`; the worker shows as asking its yesman, and you can still answer it from its conversation. A worker's failure goes to the lead too. When the lead ends a run with board tasks still to do or in progress and no worker running, yesman reminds it of them once, until you or a worker write to it again. You are notified when a lead asks you something, fails, or finishes with its whole yesman settled.

The sidebar lists each yesman's lead with its workers under it, in the order they started. Each worker has one line with its status, a number such as a1, and the subject of its work in words, its title or else its board task's title, never the commands it runs. The board uses the same numbers, and hovering a worker shows its name. A working agent shows how long its run has gone on. A done worker stays in its place, greyed; right-click the yesman to hide or show its done workers. What an agent is doing now, taken from its run's events, shows at the foot of its conversation while it works, and the lead sees it in its context.

The crew are the models yesman runs agents as, listed in Settings › yesman: each option is one of pi's models at one reasoning effort, such as Claude Opus 5.5 at high. A new yesman's lead runs as the option chosen there for new yesmen, recorded in its `yesman.json`, so hiring asks only for a name; a lead that resumes its session keeps the model it was using. The lead runs each worker as an option it names with `yesman_worker_start`'s `crew` parameter, or else as its own model and effort, which must be an option too; with no options, no yesman can be hired and no worker started.

The lead chooses by scores it sees in its context: [Artificial Analysis](https://artificialanalysis.ai)'s Intelligence Index, output speed, coding, and cost and time per task, for each option at its effort. The page does not show them. They come from a snapshot of Artificial Analysis's leaderboard embedded in the app, [`internal/crew/scores/`](internal/crew/scores/), one row per model and effort, found by the model's name: pi's model ID or name, without where it is served (`codex-remote/codex/gpt-6.1-sol` and "GPT-6.1 Sol · Codex" both find GPT-6.1 Sol). A newer snapshot is a newer dated CSV file there with the same columns; the newest date wins. When Artificial Analysis has no record of a model at an effort, adding it asks for your own estimate, intelligence at least, on the same scales.

yesman reads pi's catalog by starting pi on its own once, when it is first needed, and again when you ask on the page after changing pi's configuration.

The composer lets you choose the current agent's model and reasoning effort from the right side below the input box, from any of pi's models, crew or not. Models come from pi's configured, authenticated catalog, grouped only when there is more than one provider; effort shows only the levels the selected model supports. Change them between runs without starting a new conversation. pi saves the selection in that agent's session once it has messages, and restores it when the agent resumes.

The composer accepts images from its **Attach images** button, pasted screenshots or images, and files dropped onto the input. It previews each attachment and lets you remove it before sending. PNG, JPEG, GIF and WebP are supported, up to four images, 5 MB each and 10 MB in total. Images travel with the message to pi, including messages sent during a run; you can send an image with or without text. Draft text and attachments stay with their agent while switching conversations, and remain available to retry if submission fails.

The context meter beside these choices opens pi's estimated current context usage and model limit, with free space, cumulative input/output and cache usage, and message/tool counts. It refreshes after messages, tool results and compaction. Current context and cumulative session totals are kept separate; when pi cannot report the current context size, such as just after compaction, the viewer shows it as unknown.

The app keeps running in the tray with its window closed. Agents are child processes of the app, so quitting it stops them; they resume from their sessions next time.

The files pane, opened from the titlebar, shows the workspace of the yesman you are talking to as a tree, and opens its files in tabs: text and code in an editor with syntax highlighting that saves with Cmd/Ctrl+S or by itself as you type, Markdown rendered, images shown. It follows changes as agents make them, and never saves over a file that changed on disk since it was opened without asking. Large files stay quick: the editor draws only the lines in view, and files over 5 MB open read-only at their first 512 KB. Each folder in the path above a file opens a list of that folder. Cmd/Ctrl+P goes to a file by name, and its menu checks out repositories, opens the knowledge notes and dismisses the yesman. Its edges can be dragged.

The info panel sits between the conversation and the files, and shows the yesman you are talking to at a glance; the titlebar hides or shows it. Knowledge opens the notes in the files pane, rendered and read-only. Storage folds into one row with the disk the workspace takes; open, it shows a bar of its largest parts (each worktree, the conversations, the knowledge, the rest of `.yesman`) and sums the others. Git's objects stay in the shared pool and are not counted. Worktrees lists the workspace's worktrees, each with the lines it changed against the remote's default branch, counting edits not committed yet and new files, or, when its branch is in a pull request, that pull request: its number, title, state, size and checks. Worktrees whose pull request is merged fold into one row at the end of the list, which opens them. yesman finds pull requests through the GitHub CLI, which also sees private repositories and checks, or else GitHub's public API, in the repository and, for a fork, in its upstream.

A worktree opens in the files pane, as its files or as its changes. The pane's bar is one row: the part of the workspace shown, whose menu switches to all of it, the knowledge or another worktree, then the path of what is open, each segment a menu of its folder; in Changes those menus hold only what changed. The changes tree arrives a folder at a time: the Go side measures the changes once and lists one folder per request, opening as many folders below it as 200 rows allow, so a diff of thousands of files never arrives whole. A file's diff is read when it is chosen, up to 256 KB unless you ask for all of it (16 MB at most), and drawn as it scrolls into view. The tree can also show folders alone: choosing a folder then lists its changed files, to see what changed without reading every diff, a thousand at a time.

The board, opened beside it in the titlebar, shows the same yesman's board as a list in the agents' order. Finished and dropped tasks fold into Done at the end, a blocked task shows why, a task's owner opens that agent's conversation, and a task expands to its detail and note. It follows the agents' changes; you change it by asking the lead. The board and the files share the place beside the conversation, one at a time.

Settings, at the foot of the sidebar, has nine pages: General (light, dark or the system's appearance, kept in `settings.json` in the data directory, and the language), yesman (the crew, above, and what new yesmen run as, kept in `settings.json` too), Repositories (the repo pool: name a GitHub user or organization to tick several of its repositories, or add one by `owner/repo`; they clone in the background, three at a time, and a clone that fails stays listed with its error until you try it again or dismiss it; Fetch all fetches the pool, and it can fetch by itself every 1 to 60 minutes), Secrets and Variables (the environment variables of agents' commands, below), Storage (where yesman keeps its data, below), pi (pi's packages, below), Import and export (below), and About (the versions yesman runs with, and updates).

yesman is written in English and translated to Simplified Chinese. General › Language chooses one, or Match the system, the default, which shows the first of them among the languages of the system's settings, else English. The window, its menus, the tray and notifications change at once; MyGo keeps the choice (`App.SetLocale`): on macOS as the app's language in System Settings, which AppKit and WebKit also say their own texts in from the next launch, elsewhere in its `locale` file. The page translates itself in [`web/i18n.js`](web/i18n.js), with each message's English as its key in [`web/locales/zh-Hans.js`](web/locales/zh-Hans.js); Go translates its menus, tray, notifications and dialogs with `tr` in [`i18n.go`](i18n.go). A message without a translation shows in English.

The pi page manages [pi packages](https://pi.dev/packages): the extensions, skills, prompt templates and themes pi loads. It has three tabs. Info shows the pi every agent runs, a summary of the installed packages, pi's own settings file and what pi installs npm packages with (its `npmCommand` setting, npm unless set). Installed lists the packages pi's user settings install, with their version, source and what they provide; each can be updated, unless pinned to a version, or removed, and an npm package whose registry has a newer version says so. Browse searches the npm packages tagged `pi-package`, which is what pi's gallery lists, most popular first; a package's details show its README, links and preview, and install it. Install from source takes anything `pi install` does: `npm:name[@version]`, a git repository or URL, or a local path.

Packages are installed for the user, in pi's agent directory (`~/.pi/agent`, or `PI_CODING_AGENT_DIR`), so they load into every yesman agent and into pi wherever you run it. yesman changes them only by running `pi install`, `pi update` and `pi remove`, one at a time in the background; a change that fails stays listed with pi's or npm's error until you try it again or dismiss it. pi loads packages when it starts, so agents already running keep the packages they started with: the page says which, and puts the idle ones to sleep on request, to load the change when they next start.

### Secrets and variables

Secrets (API keys, tokens) and variables (an account name, a region, an endpoint) are environment variables that every command of every agent gets. Each has a name, which is the variable's, a value, and a description that tells agents what it is for. Settings › Secrets and Settings › Variables edit them as a form, or as YAML with the editor; switching converts between the two, and a mistake in the YAML keeps it open there. A name is set in one of them only, and a secret's must not end in `_unencrypted`, which sops keeps in the clear. Comments in the YAML are not kept.

```yaml
version: 1
secrets:            # variables: in variables.yaml
  GH_TOKEN:
    description: GitHub token for idy; gh and git push read it
    value: ghp_…
  OPENAI_API_KEY: sk-…   # a value alone is short for one without a description
```

Secrets are kept in `secrets/secrets.sops.yaml` in the data directory, a [sops](https://getsops.io) file: names in the clear, every value and description encrypted with AES-256-GCM, a MAC over all of them, and the data key encrypted to an [age](https://age-encryption.org) key. The key is `secrets/identity.age`, encrypted with a password. Setting up makes the password at random and keeps it in the system's credential store (the login Keychain on macOS, the Credential Manager on Windows, the Secret Service on Linux), keyed by the vault's age recipient, so yesman unlocks the secrets when it starts; Settings › Secrets copies it. Where the system has no credential store, you choose the password and type it after each launch. Unlocking reads the key into memory until you lock again or quit yesman; while locked, commands run without the secrets. yesman reads and writes the sops format itself, so neither sops nor its key services are needed; with the age key, which `age -d secrets/identity.age` gives with the password, `SOPS_AGE_KEY_FILE=key.txt sops edit secrets/secrets.sops.yaml` edits the same file, and yesman keeps any other age recipient sops added. A lost password cannot be recovered: the exported configuration keeps a copy. Variables are kept in plain text in `variables.yaml`, which you may edit by hand.

Agents get them through yesman's extension. Its `bash` tool (and `powershell` on Windows) replaces pi's own and asks the app, before each command, for the variables and, while unlocked, the secrets, and sets them in that command alone: a change reaches the next command without restarting anyone, and pi's process, its other extensions and its MCP servers never see the secrets. The context says one sentence about them, and `yesman_env` says what each variable is: its description, a variable's value, never a secret's. Whatever a tool returns, output or a file read, shows a secret's value, or a line of one, as `[secret NAME]`, for values of six characters or more, including values the agent was given before they changed or were removed. This keeps values out of the model's context and the session logs; it does not stop a command from using a secret as it likes, or a determined agent from encoding one.

Neither page has a backup of its own: Settings › Import and export backs both up with the rest of the configuration, below.

### Import and export

Import and export saves yesman's configuration as a TOML file, to back it up or carry it to another computer: the appearance, the fetch schedule, the pool's GitHub repositories as `owner/repo`, the variables, and the secrets: the vault's key file and secrets file as they are, encrypted, with the password from the credential store. With the password in it the file opens the secrets, so it is written readable only by you; keep it private. Importing its secrets where none are set up sets them up as they are and keeps the password in the credential store; where there are secrets, the file's are opened with its password and merged or replaced into them. A name both variables and secrets would set stops the import before anything changes. A file without variables or secrets, such as one of version 1, leaves those here alone, replacing too. Workspaces, their knowledge and conversations stay out, as does where yesman keeps its data. Importing a file first shows what it would change, then either merges it, which clones the repositories it lists that the pool lacks and changes the settings it sets, or replaces with it, which also resets the settings it leaves out and removes the pool repositories it does not list. Replace keeps a repository that work depends on, one with a worktree checked out of it (in a workspace or the archive) or a branch with commits no remote has, and says why. It also stops the clones of repositories the file leaves out, except one an agent is waiting for.

```toml
version = 2
appearance = "dark"   # system, light or dark
auto_fetch = 15       # minutes: 0, 1, 5, 15 or 60
repos = ["idy/yesman", "idy/magpie"]

[[variables]]
name = "DEPLOY_REGION"
value = "eu-west-1"
description = "AWS region the deploy scripts use"

[secrets]
password = "…"        # from the credential store
key = '''
-----BEGIN AGE ENCRYPTED FILE-----
…
'''
file = '''
secrets:
    GH_TOKEN:
        description: ENC[AES256_GCM,…]
        value: ENC[AES256_GCM,…]
sops:
    …
'''
```

In Settings › Storage, **Change…** lets you enter or browse to a data folder, then **Use folder and restart** opens it after restarting. Existing folders with data are accepted; a new folder starts fresh. To keep existing data elsewhere, quit yesman, copy the folder yourself, then reopen yesman and select the copy. Data transfers and any Git worktree path repairs are manual. The chosen location is kept in `yesman/storage.json` under the operating system's user configuration directory (Application Support on macOS, `$XDG_CONFIG_HOME` or `~/.config` on Linux, `%AppData%` on Windows). A launch with `YESMAN_HOME` overrides that choice. Below it, Usage measures the data folder in the background: its total size and file count, a bar of the repo pool, workspaces, archive, runtime files and the rest, each with its share, and the space left on its disk. A measure is reused for a minute unless you measure again; symbolic links are not followed. Storage also has a compact list of the repo pool, workspace parent, archive, secrets, variables, runtime files, settings file and storage configuration, with a Reveal action for each and the full path on hover.

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
node --test scripts/images.test.mjs
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

Automation cannot reach native file dialogs, so in such builds `YESMAN_E2E_SAVE` and `YESMAN_E2E_OPEN` name the files that Export and Import use instead of asking. `YESMAN_E2E_NAME` names the app otherwise, so that a test build runs beside another yesman instead of handing over to it.

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
| `settings.go`, `storage.go`, `disk_*.go`, `internal/home/` | Settings and About, the remembered data location, its usage and disk space, and directory layout |
| `crew.go`, `internal/crew`, `web/crew.js` | Settings › yesman: the crew, Artificial Analysis's scores and pi's catalog of models |
| `pipackages.go`, `internal/pipkg`, `web/pi.js` | Settings › pi: pi's packages, installed, updated and removed through pi, and found in the npm registry |
| `config.go` | The configuration file: export, and import by merging or replacing |
| `secrets.go`, `internal/secrets`, `web/env.js` | Settings › Secrets and Variables: the sops and age format, the vault, the variables file, and their pages |
| `update.go` | Checking for and installing new versions |
| `files.go`, `files_menu.go` | Listing, reading, finding and watching a workspace's files, and the files pane's menus |
| `web/files.js`, `web/editor.js`, `web/code.js` | The files pane, its editor, and the language of each file |
| `web/board.js` | The board pane |
| `info.go`, `web/info.js` | The info panel: knowledge, worktrees with their changes and pull requests, and storage |
| `internal/pool/changes.go`, `web/diff.js` | A worktree's changes against its base, listed a folder at a time, and each file's diff |
| `i18n.go`, `web/i18n.js`, `web/locales/` | The languages yesman is written in, and their translations |
| `tools/codemirror/` | Rebuilds the vendored CodeMirror bundle |
| `mygo.json`, `resources/`, `icons.go` | Packaging, the yak app icon and the monochrome tray template; see [icon sources](resources/README.md) |
| `internal/pool` | The repo pool and its worktrees |
| `internal/github` | What the pool takes: GitHub repositories, their clone URLs and a fork's parent, and the pull request of a branch |
| `internal/yesmen` | Workspaces, workers, the board and their files |
| `internal/agent` | pi processes in RPC mode, their status and current activity |
| `internal/server` | The loopback API for the extension, and the context each agent gets |
| `pi/yesman.ts` | yesman's pi extension |
| `web/` | The page: `index.html`, `app.js`, `markdown.js`, `app.css`, and Magpie in `mds/` |

## Contributing

Use a lowercase `prefix: Subject` pull-request title and describe both the change and its relevant validation. Closing Issues are optional.

Ready pull requests receive automated OpenAI/Codex review through the shared workflow. See the [review guide](docs/review-workflow.md) for manual requests and how to interpret the three checks and completed review on the current head.

## License

[MIT](LICENSE). Magpie's files in `web/mds/`, the local Phosphor icon font in `web/icons/`, and CodeMirror in `web/vendor/codemirror/` keep their own licenses. The model scores in `internal/crew/scores/` are [Artificial Analysis](https://artificialanalysis.ai)'s, used under its terms, which ask that it be credited as their source; Settings › yesman does.
