# Design notes

This document records the reasoning behind yesman and the decisions made during its initial design discussion. The [architecture](architecture.md) translates those decisions into a proposed implementation. Nothing here implies that the application already exists.

## Product intent

**the agent that says yes and gets things done.**

The person using yesman should be able to discuss an outcome with a lead and leave the coordination to that lead. The lead decomposes the outcome into bounded tasks, chooses execution agents, follows their progress, and makes sure the agreed delivery happens.

Getting things done means following through on the outcome. Writing code, submitting it, merging it, and releasing it are distinct milestones; the required endpoint belongs in the task's acceptance criteria.

## From cascading computers to permanent leads

The original idea was a task board spanning several computers, potentially arranged in a parent-child cascade. The discussion established that the useful unit of responsibility is a **lead**, rather than a computer or a temporary session.

- A computer supplies the environment and local execution resources.
- A lead owns an ongoing area of responsibility, its memory, and its task board.
- A goal records the outcome agreed with the user.
- Tasks are the bounded deliverables used to reach that outcome.
- Sessions are the replaceable agent executions used to do the work.

One host can run multiple leads. One lead can manage multiple repositories and multiple tasks at the same time. A lead is permanent: finishing work never deletes it. Its fixed position in the application is its **slot**; a new runtime session can continue the same lead's work.

## Coordination and execution are separate responsibilities

The lead discusses, analyzes, prioritizes, decomposes, dispatches, and follows up. Task sessions do the implementation and delivery work.

The lead can choose different execution agents for different tasks. Codex, Claude Code TUI, and pi are examples. This choice does not change task identity, repository ownership, or the lead's memory.

"The lead handles it" means the lead arranges and tracks the work. It does not mean the lead stops coordinating to perform the worker's implementation itself.

## Discoveries should not derail delivery

A session should finish a focused task and deliver promptly. If it notices another problem, it reports a finding to its lead.

The lead can associate the finding with existing work, identify duplicates, create a task within the agreed scope, or ask the user for a decision. Non-blocking discoveries should not silently expand the current task. A blocking discovery should be recorded as a blocker or dependency.

This keeps discoveries visible without forcing every session to become an indefinitely growing project.

## Human triage is explicit

The board has a **to triage** column. Every item in that column waits for the user.

A lead can investigate, explain the tradeoff, and recommend an action. It cannot silently clear human triage. The user can discuss an item directly with the responsible lead, and that conversation's decision must update the task record.

Tasks that are already within an agreed goal and authorization can proceed automatically. Human triage is for decisions; it is not a requirement to approve every routine subtask.

## Memory belongs to each lead

Each lead maintains independent long-term memory, including preferences, project relationships, decisions, and reusable lessons. Its memory survives runtime replacement and application restarts.

Terminal persistence and saved conversation history do not provide this product contract by themselves. yesman stores the lead identity and memory independently of Herdr's current terminal.

Task state, dependencies, active executions, and delivery evidence are structured application records. They must not exist only in a model's recollection or a conversation summary.

## Why Herdr

The chosen runtime backend is [herdr-core](https://github.com/herdrdev/herdr-core). Its documented CLI provides structured launch and inspection commands, messaging, terminal output, parent-child relationships, and an event stream. Templates can hold reusable agent commands and role instructions.

This gives yesman a runtime for both lead sessions and heterogeneous task sessions. yesman supplies the product-level task, memory, triage, recovery, and delivery semantics.

The integration uses the documented `hc` interface first. A Rust implementation does not require linking a private Herdr crate or rebuilding the coding agents' own reasoning loops.

Alma was also examined for its conversational presentation and cross-conversation memory. It is a reference for the interaction model, not a required runtime dependency.

## Chosen desktop stack

- **Tauri 2** for the desktop shell and its Rust bridge.
- **Rust** for the host service, domain rules, persistence, and Herdr integration.
- **TypeScript and HTML** for the frontend application, using [idy/magpie-design-system](https://github.com/idy/magpie-design-system) for its design foundation.
- **SQLite** for host-owned durable records, with separate memory namespaces for each lead.

Magpie supplies CSS tokens, components, shell styles, fonts, icons, and small JavaScript helpers. yesman supplies the board, lead conversation, task interactions, and asynchronous application state. A separate UI framework is not required for the initial design.

The host service is independent of the desktop window so that closing the UI does not end the coordination service or intentionally cancel work.

## First-version connectivity

The first version supports a local host and explicitly configured **flat LAN host connections**. A desktop client can aggregate those hosts' boards and talk to their leads.

Hierarchical cascading, parent leads supervising other leads, recursive board forwarding, and automated host-to-host delegation are deferred. Herdr's ordinary lead-to-worker parent-child relationship remains part of the local execution model.

A task stays on its owning host. Looking at another host's board does not move its session or let a local worker modify its checkout. Selecting a remote lead in the desktop UI sends the user's request to that lead's own host service.

## Networking stays outside yesman

The referenced networking project is [idy/tailsplit](https://github.com/idy/tailsplit), not a new component to be built inside yesman.

yesman connects to an address that is already reachable. LAN configuration, cross-network connectivity, routing, VPNs, NAT traversal, and relays are deployment responsibilities. Existing tailsplit and network configuration can provide reachability where appropriate; the yesman protocol does not change.

A Tailsplit application proxy or exit-node route does not, by itself, establish that an inbound host service is exposed to peers. Deployment must supply and verify the reachable endpoint. Application authentication is still a yesman responsibility.

## Deferred work

- Lead-to-lead and host-to-host cascading.
- Automatic cross-host task delegation and distributed scheduling.
- A custom VPN, network-discovery system, relay, or NAT traversal implementation.
- A replacement for Herdr's terminal runtime or the agents' existing provider integrations.
- A shared memory pool that implicitly mixes separate leads.

These exclusions keep the initial product focused on the discuss-delegate-deliver loop.
