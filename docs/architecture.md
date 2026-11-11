# Architecture

This document defines the intended behavior of yesman. It is a design, not a description of an implemented runtime.

## Responsibility

The user discusses a goal with a lead. The lead clarifies the outcome, creates bounded tasks, selects execution agents, monitors progress, and brings decisions back to the user. Task sessions perform implementation, verification, commits, merges, and release work within the agreed scope.

A lead coordinates work. It does not take over a worker's implementation or repository operations.

## Domain model

| Entity | Responsibility |
| --- | --- |
| Host | Own the local repositories, execution environment, and running agents |
| Lead | Maintain a permanent identity, independent memory, conversation, repository scope, and task board |
| Goal | Preserve the requested outcome, agreed scope, decisions, and acceptance criteria |
| Task | Describe a bounded deliverable with one owning lead and host, dependencies, status, and evidence |
| Session | Represent a particular execution attempt or the current runtime conversation for a lead |
| Finding | Report a discovery from a task session for deduplication, association, and disposition |

One host can contain several leads. One lead can manage several repositories, goals, and tasks. A task can involve several repositories on its owning host. A task can have multiple sequential execution attempts; these attempts do not create new task identities.

Host connection topology, lead parent-child relationships, and task dependencies are separate relationships. A parent lead can aggregate a child's board without owning its tasks.

## Permanent lead slots

A slot is the stable home of one lead. The slot keeps the lead's identity and management scope while its runtime can change.

Each slot preserves:

- A stable lead ID, name, owning host, and optional parent lead ID.
- Its own memory store, user conversation, decisions, and operating instructions.
- Its managed repositories, goals, tasks, and worker-session associations.
- Its execution configuration and current lead-session reference.

Leads are permanent. Task completion, cancellation, session exit, and application closure never delete a lead. Pausing execution or recovering a failed process preserves the slot and its records.

Only one lead session at a time may issue scheduling decisions for a slot. An application restart must reconcile existing work before launching more workers.

## Long-term memory

Every lead owns its own memory. Decisions, user preferences, repository relationships, and reusable lessons can be maintained automatically by that lead. Replacing its runtime must preserve access to that memory.

Operational facts such as task status, dependencies, session identities, PR links, CI results, and release records are stored as task-system state. They must not exist only in conversation history or generated memory summaries.

A worker receives the task brief, acceptance criteria, relevant decisions, and the context needed for its assignment. Worker output can propose memory updates; it does not silently rewrite another lead's memory.

Storage technology and memory retrieval mechanisms have not been selected.

## Goals and delegation

1. The user and lead establish a goal, scope, and acceptance criteria.
2. The lead creates tasks with explicit dependencies and owning repositories.
3. Tasks within the agreed scope can proceed without asking the user to approve every subtask.
4. The lead selects a supported execution agent for each task and dispatches it on the owning host.
5. Worker findings, execution events, and delivery evidence update the goal and task records.
6. Decisions beyond the agreed scope enter **to triage** for the user.

Codex, Claude Code TUI, and pi are examples of intended execution agents, not completed integrations.

## Findings and human triage

A finding is not automatically an instruction to start new work. The lead first associates it with existing goals and tasks, checks for duplicates, and determines whether it blocks the current deliverable.

Non-blocking discoveries can be reported while the worker finishes its assigned task. Blocking findings can put the task into a blocked state and trigger an appropriate dependency or a request for a decision.

The **to triage** column is the user's decision queue. A lead can gather context, explain impact, identify duplicates, and recommend a disposition. Only the user's decision releases an item from triage. Decisions made in conversation must be written back to the relevant records.

## Task and delivery status

The initial board vocabulary is:

- **to triage**: waiting for the user's decision.
- **ready**: accepted and eligible to run once dependencies allow it.
- **running**: an execution session is performing the task.
- **blocked**: a dependency, missing information, or execution issue prevents progress.
- **awaiting merge**: the task requires a merge that has not happened yet.
- **awaiting release**: required changes are merged, but the required release is outstanding.
- **done**: the task's acceptance and delivery requirements are satisfied.
- **cancelled**: the task is intentionally stopped, with its history retained.

Not every task requires a merge or release. Its completion requirement determines the applicable stages.

Execution state and delivery state are different. An agent becoming idle, exiting, or reporting success does not prove that its changes passed checks, merged, or shipped. Task completion must cite the relevant deliverables and verification evidence.

## Host ownership and aggregate views

Each task has exactly one owning lead and one owning host. Its working directories, sessions, and repository changes stay there. Repository services such as hosted CI or release infrastructure can perform work initiated by that host.

A cross-host request is accepted by the destination host's lead and becomes a task owned there. It does not move a live session, branch, or worktree between hosts. Related tasks can depend on one another while retaining their ownership.

The combined board projects records from connected hosts; it does not create duplicate tasks. The view should show connection health and when each host last reported its state so stale data is distinguishable from current status.

## Execution adapters

A backend adapter is responsible for launching an execution session, sending messages, reading output or receiving events, inspecting runtime status, attaching to existing work, and stopping a session when requested.

The proposed first adapter uses herdr-core. Its terminal and parent-child mechanisms can support leads supervising workers. Herdr launch templates can provide reusable role instructions and agent commands.

The yesman task record must keep its own association with the lead slot and execution session. A process-level parent relationship alone is not a durable task identity.

Herdr children normally end when their parent ends; its `--outlive-parent` option can change that behavior. Recovery must explicitly choose whether to retain workers and reconcile their state before redispatching tasks. Agent conversation resume capabilities also depend on the selected execution agent.

Herdr does not replace the yesman lead-memory store, task database, human triage workflow, or delivery records. These are separate responsibilities even when the runtime provides related session history or notifications.

## Repository coordination

Parallel code tasks should use separate worktrees or equivalent isolated working directories. Leads operating on the same repository must share merge and release coordination so that independent task execution does not cause competing delivery actions.

The concrete coordination and persistence mechanisms remain implementation decisions.

## Initial implementation scope

1. Define persistent lead slots, goals, tasks, findings, and execution-session records.
2. Connect one local lead to one execution backend and complete a task with recorded evidence.
3. Add the user's triage queue and lead conversation.
4. Connect multiple leads and hosts to a combined board.
5. Extend delivery tracking and support additional execution backends.
