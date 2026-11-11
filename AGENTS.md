# Project guidance

Read [README.md](README.md) when starting work in this repository. Read [docs/architecture.md](docs/architecture.md) before changing lead lifecycle, memory, task ownership, triage, execution adapters, or host connections.

- The product and repository name is always lowercase: **yesman**.
- Leads are permanent identities with independent long-term memory and multiple tasks. An execution session is replaceable; its lead is not.
- Leads coordinate. Task sessions perform implementation and delivery work.
- Items in **to triage** require the user's decision.
- Each task has one owning lead and host. Aggregate views preserve that ownership.
- Keep implemented behavior distinct from planned behavior. The repository currently contains design documentation only.
