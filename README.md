# yesman

**the agent that says yes and gets things done.**

yesman is the person you hope to meet: someone who takes responsibility and makes things happen. It handles the coordination while other agents do the work.

Discuss a goal with a lead. The lead turns it into tasks, assigns the right agents, and keeps delivery moving through implementation, commits, merges, and releases. You make the decisions that need a person; yesman follows through on the rest.

> Status: design documentation. The stack and architecture below are selected design decisions; there is no runnable application yet.

## How it works

1. **Discuss a goal.** Talk with a permanent lead about the outcome, scope, and acceptance criteria.
2. **Delegate the work.** The lead creates tasks and selects an execution agent for each one, such as Codex, Claude Code TUI, or pi.
3. **Keep tasks focused.** Each task session works toward one deliverable. Discoveries become linked findings or tasks instead of silently expanding its scope.
4. **Make the calls.** Anything needing your decision goes to **to triage**. Discuss it with the responsible lead and decide what happens next.
5. **Follow delivery.** The lead tracks evidence from task sessions and delivery systems until the required work is committed, merged, or released.

This is getting things done through coordination: the lead owns the follow-through, and execution agents produce the deliverables.

## Permanent leads

A lead is a permanent identity, not a task or a temporary conversation. Completing a task never removes its lead.

Each lead maintains independent long-term memory, an ongoing conversation, repository scope, and a task board. One host can run several leads; each lead can manage several repositories and tasks concurrently. Its runtime can restart or be replaced while the lead's identity and state remain intact.

## Stack

| Layer | Choice |
| --- | --- |
| Desktop shell | Tauri 2 |
| Frontend | TypeScript/HTML with [idy/magpie-design-system](https://github.com/idy/magpie-design-system) |
| Backend | Rust host service and Tauri bridge |
| Persistence | SQLite with independent memory namespaces for each lead |
| Agent runtime | [herdr-core](https://github.com/herdrdev/herdr-core), using its CLI and event stream |

Herdr runs lead and task sessions. yesman owns permanent identities, task records, memory, human triage, and delivery tracking. Different tasks can use different configured coding agents.

## Local and LAN hosts

The first version targets local execution and explicitly configured, flat LAN host connections. See connected hosts' tasks and status in a combined board, or select a lead to discuss its work directly.

- Each task has one owning lead and one owning host.
- Worktrees, implementation, commits, and delivery operations remain owned by that host.
- Viewing another host's board does not move its sessions or repository changes.
- Hierarchical cascading and automatic host-to-host delegation are deferred.

Networking is provided by the existing environment. Where needed, [idy/tailsplit](https://github.com/idy/tailsplit) and network configuration supply a reachable host address. yesman does not build a VPN, relay, routing system, or NAT traversal layer.

## Design documents

| Document | Purpose |
| --- | --- |
| [Architecture](docs/architecture.md) | Processes, domain model, storage, Herdr integration, host API, recovery, and implementation sequence |
| [Design notes](docs/design-notes.md) | The original discussion, product intent, decisions, and deferred cascading scope |
| [Frontend integration](docs/frontend.md) | Magpie assets, desktop integration, UI responsibilities, and future validation |
| [Agent guidance](AGENTS.md) | Project-specific instructions for implementation work |
| [Automated review](docs/review-workflow.md) | Shared OpenAI/Codex review triggers, trust boundaries, and result checks |

## Getting started

There is no application entry point, development server, Cargo workspace, or frontend package yet. Reading the current repository requires no runtime dependencies. Start with the design documents; the first implementation milestone is a local discuss-delegate-deliver loop.

## License

[MIT](LICENSE).
