# Project guidance

Read [README.md](README.md) when starting work in this repository. Read [docs/architecture.md](docs/architecture.md) before changing lead lifecycle, memory, task ownership, triage, execution adapters, or host connections. Read [docs/frontend.md](docs/frontend.md) before frontend or Tauri window integration work, and [docs/design-notes.md](docs/design-notes.md) when revisiting product scope.

- The product and repository name is always lowercase: **yesman**.
- Leads are permanent identities with independent long-term memory and multiple tasks. An execution session is replaceable; its lead is not.
- Leads coordinate. Task sessions perform implementation and delivery work.
- Items in **to triage** require the user's decision.
- Each task has one owning lead and host. Aggregate views preserve that ownership.
- Use Tauri 2 and Rust, with TypeScript/HTML and `idy/magpie-design-system` for the frontend. Use herdr-core for lead and worker runtimes.
- The first version supports flat, explicitly configured LAN hosts. Lead-to-lead cascading and automatic host-to-host delegation are deferred.
- Networking and any cross-network reachability come from the existing environment and `idy/tailsplit`; do not introduce a VPN, relay, or routing layer.
- Keep implemented behavior distinct from planned behavior. The repository currently contains design documentation only.
