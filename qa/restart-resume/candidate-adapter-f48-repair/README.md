# t199 — F48 repaired-candidate binding

**Quick binding preparation complete; source/current gates and native acceptance are not claimed.** PR100 candidate is now `cfaaeec242a7d372669b5e39739b71bbd15ad646`, tree `c7b0c06488b613c56dd0e52ceba98e343a4e0369`, sole parent rejected `8a73d96335dc5d2fa787f9b15e1b812bd58b0728`. Base remains immutable v0.1.48 `6f0a1f1400f667c09cc57a2b5d5fc93962334ee9` / tree `0126ef4a6e19a665f0f8482ba31d9a2bffb49d41`. Verified all three full commits before read-only object inspection. No author checkout/index/ref/file was borrowed or changed.

This new directory does not overwrite [candidate-adapter-main048](../candidate-adapter-main048/README.md), its rejected-source record or any earlier proof. `candidate-contract.json` updates candidate identity and retains **S=null, Q=null and artifactRuntimeReceipt=null**. No actual package release/asset ID, archive/main digest, metadata/tag commit or execution run is invented. The .48 publication prerequisite remains satisfied by the already saved t193 receipt; it is not used as S's later artifact identity.

## Actual delta and effect on the adapter

`source-delta.json` binds exact before/after mode/blob identities for all nine changed paths, full commit/tree/parents and base ancestry. All **673 other tree entries are identical**. I inspected the six production-file diff saved as `production-delta.diff` (80 additions/20 removals); the other paths are the fake-RPC test option and two new regression files.

The production changes record actual incoming work with generation-scoped in-memory `givenWork`, separate one-recovery `restartHandled`, and use those ownership fences at startup/admission/report/pipe and hold boundaries. History-only startup and recovery dispatch do not mark incoming work. A definite refusal's own inbox bookkeeping no longer serves as the hold's input fence. t206 independently determines whether this closes F48-1/F48-2; this packet only checks adapter compatibility.

No adapter-facing contract changed: public bridge/DTO, service/settings/crew schema, managed runtime and production extension, canonical origin/session/receipt format, restart checkpoint/result storage, task ledger storage, renderer, package/updater/helper source and workflow files are outside the nine paths and identical. The added `leadPause.givenWork` is in-memory; the shown delta adds no serialization field. Thus no new package seam, package case, profile schema, provider protocol or collector change is needed for this repair.

`adapter.py` is a **binding-only loader**, not another copy of the fixture. It verifies the original adapter manifest and adapter bytes, executes those verified bytes in memory with their original file location, checks the old identity and changes only the two candidate identity pins to cfaa/c7b0. Existing planners, production bridge binding, raw collectors and prototype loading retain their original implementation. Put this directory before the old adapter directory on the later host adapter's Python import path; imports of `adapter` then use this repaired binding. Original `host_proof.py` and sealed prototype/controller/driver are reused.

Three small pure checks pass: contract-only rebasing with artifact inputs still unset; repaired identity accepted through the original bridge binder with exported-Q revision semantics preserved; rejected and mixed old/new source/tree identities refused. Python compilation passes. The earlier seven pure groups are reused by exact implementation identity and were not rerun. All33 earlier sealed payloads (9+13+11) were rehashed unchanged. No server, profile, app, pi, package, native command or production-source test ran.

## One next executable route

After t206 accepts the corrected source/current CI and the Lead supplies actual immutable S/private-Q artifacts plus execution authority, continue through **`scripts/check-production-upgrade.py::main → run` on the one isolated hosted macOS ARM64 runner**, using its existing archive/feed/TLS verification, `switch_web`, native inspection and failure snapshots. The narrow t199 join uses this directory's `adapter.py`, the original host-proof collectors and the sealed Controller/provider/driver. The existing joining sequence is in [the preserved adapter README](../candidate-adapter-main048/README.md#joining-these-pieces-in-the-later-authorized-host-run); replace only its candidate identity with this checkpoint, never the rejected commit.

This is the existing acceptance route, not authority to launch the unmodified historical runner: its `missing-pi`/zero-agent expectations are already known to require the t199 join. The remaining narrow runner attachment and actual artifact receipts still belong to the Lead's execution handoff. There is no second runner or native test matrix proposed here, and no product hook/change is required by the inspected repair. The route stays the previously agreed eligible/paused controls, S→Q update, separate prearmed failed-launch/verified explicit recovery and ordinary no-replay reopen.

Keep the real initial conversation/history path enabled; initial observer reports do not prove history call ordering. Corrected-source tests/CI supply deterministic observation-before-recovery and refusal→durable-hold→ordinary-open proof, as detailed in [the unchanged proof-boundary note](../corrected-source-proof-boundary.md). Do not add a package refusal scenario or a forced race barrier. An explicitly stopped paused control and a preclaim missing-main launch failure retain their original, distinct claims.

Pure reconstruction check (safe now, already executed once in this preparation):

```sh
PYTHONDONTWRITEBYTECODE=1 python3 .yesman/evidence/t199/restart-resume-release/candidate-adapter-f48-repair/test_binding.py
```

**t199 remains blocked on t206 accepted source/current CI and the Lead's binding/execution handoff.** This prep made no checkout, remote request/write, hosted dispatch, main integration, source modification, secret/profile/native run, version allocation or publication. No background process remains. Stop here until the next handoff.
