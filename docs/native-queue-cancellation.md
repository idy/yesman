# Native queue cancellation: dependency boundary and proposed API

Status: **not implemented in yesman**. Message Copy is available; Cancel/Edit and
ArrowUp recall remain blocked on a native pi queue API. A queue count or a successful
prompt acknowledgement cannot authorize cancellation of an individual receipt.

## Verified current behavior

The audited installed packages are `@earendil-works/pi-coding-agent@1.5.2` and
`@earendil-works/pi-agent-core@1.5.2`. yesman currently launches the user's installed
`pi` executable; it does not own a pinned npm runtime.

| Boundary | Installed implementation | Missing capability |
| --- | --- | --- |
| RPC | `dist/modes/rpc/rpc-mode.js:314–355` forwards raw text/images; the request ID only correlates the response; `clear_queue` calls `session.clearQueue()` | No stable item ID, snapshot of item membership, or selective cancellation command |
| Session SDK | `dist/core/agent-session.js:1836–1905` runs input hooks then creates a new user object; `:2046–2065` clears both queues or returns text arrays | No pending-item handle or selective removal method |
| Session display | `agent-session.js:784–807` removes a pending display string using `indexOf(messageText)` at `message_start` | Text equality cannot establish which original intent was consumed, especially across hooks and equal inputs |
| Core | `node_modules/@earendil-works/pi-agent-core/dist/agent.js:60–86` owns `PendingMessageQueue`; `:186–215` exposes enqueue, clear-all and peek | No selective removal; peek only previews the next batch, not all pending entries |
| Extension SDK | `ExtensionContext` exposes `isIdle`, `abort`, `hasPendingMessages`, read-only session history; commands add session navigation | No access to a native pending-item handle or supported cancellation API |

`Agent.continue()` and the low-level loop call the same synchronous `drain()`
(`agent.js:263–270,334–342`). That is the ownership boundary. `message_start`
happens later, after the queue item has already left the pending set. Retrofitting
cancellation at a message hook is too late.

SHA-256 of the audited installed JavaScript:

- `agent-session.js`: `ee756f6f74afe9f8930ebf216c95334605d913a275a0eec2c9702f101baef3c3`
- `rpc-mode.js`: `505297dba2df1de3003c0399b3312fd581359a4e27a5e9262b274b6cb5cb4ef3`
- `pi-agent-core/dist/agent.js`: `f45ae001e2b14fced8ae151f0a00303b5fbdb5fc4b25418d45de6a5546f48a8c`

Run the unmodified installed RPC probe:

```sh
node scripts/check-queue-capabilities-pi.mjs
```

It uses isolated configuration, synthetic images and a local deterministic
provider. Two identical-text steering inputs with different image counts plus
one follow-up produce string-only queue arrays. `cancel_queued_message` returns
`Unknown command`; the three pending items remain. Passing `queueId` to
`clear_queue` still clears all three, returning only text. This destructive command
is exercised only in the isolated probe and is not used by yesman.

## Proposed native change

Prefer upstream pi support. If an independently maintained runtime is selected,
carry a small reviewed source patch in that pinned runtime and test its built CLI;
do not modify the user's global installation or introspect/mutate private queue
arrays from yesman's extension. Depending on a different pi runtime also requires
preserving the user's provider, package, resource, MCP and tool configuration.
That deployment decision is larger than an extension-only change.

1. Give each core **enqueue occurrence** an opaque handle, returned by `steer()` or
   `followUp()`. A token is separate from the message object: two writers may enqueue
   the very same object, and each occurrence must be independently removable.
2. Add synchronous `cancelQueued(handle): boolean` at the core queue owner. It must
   remove exactly that entry without awaiting, rebuilding either queue, clearing
   others, aborting the run or invoking model hooks. `drain()` and cancellation run
   in the same event loop: whichever removes the entry first wins. After drain,
   cancellation returns false even if no `message_start` event has reached the host.
3. Add native enqueue/drain/remove notifications carrying handles and monotonically
   increasing revisions. Session bookkeeping must update at those transitions,
   not by matching displayed text. Include custom/extension-injected messages in
   queue accounting, with their own provenance, without granting human edit actions.
4. Carry the original submission identity in the **prompt invocation's private
   closure** through asynchronous hooks and expansion. At enqueue, bind it to the
   returned handle. Do not correlate by final text, timestamp or image fingerprint,
   and do not append metadata to user content, `AgentMessage`, or model/context
   hook inputs. Handled commands and inputs never create a pending handle.
5. Expose a capability version plus session/process epoch, per-item snapshot and
   cancellation in SDK/RPC. Illustrative wire contract (not a current command):

```json
{"type":"get_queue","id":"read-1"}
{"type":"response","id":"read-1","success":true,"data":{"capability":1,"epoch":"process-session-nonce","revision":12,"items":[{"queueId":"opaque-id","submissionId":"immutable-intent-id","mode":"steer"}]}}
{"type":"cancel_queued_message","id":"cancel-1","epoch":"process-session-nonce","queueId":"opaque-id"}
{"type":"response","id":"cancel-1","success":true,"data":{"outcome":"cancelled","revision":13}}
```

A stale epoch, wrong owner, or already-drained entry cannot succeed. A repeated
cancel is distinguishable from an unknown handle through bounded terminal records;
item IDs are never recycled within an epoch. Snapshot membership is useful for
showing an action, but the cancellation result is the final authority. A missing
capability means no Cancel/Edit or ArrowUp recall, including on old histories.

An executable, **test-only** core prototype is in
[`scripts/probe-queue-cancel-core.mjs`](../scripts/probe-queue-cancel-core.mjs). On
Node 24.22+ it checks the exact audited source hash and uses a loader hook to apply
an in-memory delta; it writes no installed files and never runs in the app. It
executes the actual installed agent loop with a deterministic stream to prove:

- cancellation of a middle steering and middle follow-up preserves survivor order;
- same text/different images, equal images and even repeated identical object
  references have distinct handles;
- duplicate cancellation succeeds once; unknown handles leave all items intact;
- cancellation before drain wins; cancellation at `message_start` loses;
- both queue modes (`all` and `one-at-a-time`) behave correctly;
- handles never enter the model messages, and the installed file is unchanged.

```sh
node scripts/probe-queue-cancel-core.mjs
# For a non-global installation:
YESMAN_TEST_PI_PACKAGE=/path/to/pi-coding-agent node scripts/probe-queue-cancel-core.mjs
```

This proves only the proposed core primitive. It does **not** validate a patched
Session SDK/RPC, a shipped dependency, Go lifecycle cancellation, or composer
restoration. Those layers must be implemented and independently reviewed before
claiming native recall support.

## yesman integration once the API exists

Keep this separate from receipt acknowledgements. An eligible row needs a known
current epoch, exact submission-to-native-handle binding, positive pending
membership and the full original payload. Preserve original text plus ordered
`{type,mimeType,data}` images before hooks or image normalization in a separately
bounded in-memory cache; never store full images in `delivery.json`, and never
reconstruct original input from a preview, transcript transformation or thumbnail.
If cache capacity is exhausted or the payload is lost, omit recall eligibility.
The existing receipt limits (256 metadata entries/64 active) and disk bounds stay.

A cancel transaction must:

- capture the agent ID, `Since`, workspace `HiredAt`, native process/session epoch,
  submission ID and queue handle; reserve against dispatch/retirement/removal;
- acquire the complete original payload for the destination composer before asking
  native pi to remove it; protect a nonempty draft with an explicit user choice or
  safe separate draft storage; never silently replace or concatenate drafts;
- atomically remove that native occurrence, then persist a terminal cancellation
  receipt before reporting success; leave failed/uncertain outcomes truthful;
- preserve the immutable intent's fingerprint and tombstone/floor semantics:
  same-ID Submit retries return the old terminal record, never dispatch again;
  edited resubmission uses a fresh ID; terminal-record eviction still advances the
  persisted floor so a full Manager restart cannot revive the old intent;
- revalidate owner/process/generation on callbacks. Slow native RPC or persistence
  waits must not hold the global Manager mutex. Archive and worker removal seal
  and join writers using the existing lifecycle barriers;
- make lost-ack retries idempotent. The result can be reconfirmed, but restoring
  the draft must happen once. Refresh/process loss without a full payload cannot
  fabricate a successful text/image restore. A native removal with failed durable
  acknowledgement remains uncertain until reconciled, never automatically resent.

ArrowUp is a composer-local action: empty text **and** no attachments, collapsed
selection at the appropriate boundary, no IME composition, no active higher-priority
menu/palette/dialog/recording, no pending send/read/cancel. Choose the current
agent's newest reliably eligible handle. Suppress repeated activation while pending;
late results update only that agent generation and cannot overwrite a new draft.
Integrate through the final shortcuts registry rather than creating another global
keyboard listener. No history editing, fork, implicit abort, clear/replay or restart.

Required end-to-end acceptance includes both native queue modes and delivery kinds,
multiple writers, middle-item cancellation, identical inputs, transformed input
hooks, double clicks, consume/cancel and ack races, full image restoration,
new-intent resend exactly once, lost acknowledgements, failed persistence, process
exit and full restart, receipt eviction, archive/removal/same-name replacement,
and production DOM/bridge tests for drafts, IME, keyboard consumers, refresh and
cross-agent late results. Canceled input must not invent model usage or task
completion. The existing unmodified-pi capability probe is evidence of the blocker,
not acceptance evidence for that future implementation.

## Current Copy behavior

`web/message-actions.js` reads raw text blocks, never rendered Markdown. Native
messages have Copy only. Submission receipts copy only when their stored text is
complete (`preview` is absent/false); a truncated receipt disables Copy rather
than losing content silently. Image-only inputs disable text copying with an
explicit tooltip. Original images are not copied to the clipboard. Copy uses the
existing `CopyValue` bridge, leaves drafts and queue state alone, prevents duplicate
pending clicks, and surfaces clipboard failures. Internal notes/reports keep their
existing renderer and have no message action toolbar.
