# yesman

**the agent that says yes and gets things done.**

yesman is the person you hope to meet: someone who takes responsibility and makes things happen. It handles the coordination while other agents do the work.

Discuss a goal with a lead. The lead turns it into tasks, assigns the right agents, and keeps delivery moving through implementation, commits, merges, and releases. You make the decisions that need a person; yesman follows through on the rest.

> Status: project definition and architecture. There is no runnable application yet. The capabilities below describe the intended system.

## How it works

1. **Discuss a goal.** Talk with a permanent lead about the outcome, scope, and acceptance criteria.
2. **Delegate the work.** The lead creates tasks and selects an execution agent for each one, such as Codex, Claude Code TUI, or pi.
3. **Keep tasks focused.** Each task session works toward one deliverable. Discoveries become linked findings or tasks instead of silently expanding its scope.
4. **Make the calls.** Anything needing your decision goes to **to triage**. Discuss it with the responsible lead and decide what happens next.
5. **Follow delivery.** The lead tracks the evidence from task sessions and delivery systems until the required work is committed, merged, or released.

This is getting things done through coordination: the lead owns the follow-through, and execution agents produce the deliverables.

## Permanent leads

A lead is a permanent identity, not a task or a temporary conversation. Completing a task never removes its lead.

Each lead maintains its own long-term memory, ongoing conversation, repositories, task board, and execution sessions. One host can run several leads, and each lead can manage several repositories and tasks concurrently. The current agent process can restart or be replaced while the lead's identity and state remain intact.

## Multiple hosts, one overview

Connect to several hosts and see their leads, tasks, blockers, and delivery status in one combined board.

- Each task has one owning lead and one owning host.
- Execution, repository changes, commits, and delivery actions stay on the owning host or are initiated there through the repository's delivery services.
- Parent leads can see child leads' tasks. Connected hosts can expose their boards to one another.
- Sharing a view does not transfer task ownership or a running session to another host.

## Execution backends

The execution layer is intended to support different coding agents and runtimes without tying task records or lead memory to one of them.

[herdr-core](https://github.com/herdrdev/herdr-core) is the first proposed backend for launching agents, maintaining terminal sessions, communicating with workers, and observing their status. These integrations are not implemented yet. Permanent lead identities, independent memory, task ownership, triage, and delivery tracking belong to yesman.

## Getting started

There is no installation, development server, or application entry point yet. No runtime dependencies are required for the current documentation.

Start with the [architecture](docs/architecture.md) for the domain model and execution boundaries. Runtime, storage, and UI choices remain open.

## Repository guide

| File | Purpose |
| --- | --- |
| [README.md](README.md) | Product purpose, intended workflow, and current status |
| [docs/architecture.md](docs/architecture.md) | Lead lifecycle, task ownership, triage, memory, and execution model |
| [AGENTS.md](AGENTS.md) | Project-specific guidance for coding agents |
| [LICENSE](LICENSE) | MIT license |

## License

[MIT](LICENSE).
