# Managed pi and native message recall

The pinned runtime advertises `humanSteer` alongside queue capability 4. The Go bridge sets the new option only when Pi advertises it. The child serializes human input preparation and makes the single-slot decision synchronously at native admission. A pending occurrence stays available to the native poll while a later input runs hooks. Different trusted contexts, commands, stale Stop generations and budget refusals leave the existing queue intact. `follow_up` and the legacy promotion RPC keep their original behavior. The conversation has a single recall control for a pending human occurrence.

The v2 receipt file keeps private IDs and payload fingerprints. The same per-agent writer saves a bounded, private `human-drafts.json` alongside it before managed dispatch. A taken queue occurrence and a recorded history entry are separate events. Unresolved originals can return to the composer on request. Receipt IDs from the previous process still deduplicate retries. Only an explicit human submission can send recovered content to Pi.

The writer uses one reserved `.human-drafts-v1.stage` file in that agent's session directory. It publishes the complete snapshot by rename. On initialization, a stage with the expected file type, permissions and size is retired as unpublished. Other filenames are left alone. A corrupt or unreadable snapshot, a receipt mismatch, or an unexpected reserved stage blocks submission for that agent and leaves the files in place. The Manager keeps the load error for its lifetime. After reviewing the files and fixing the problem, restart Yes Man to try loading again.

yesman's default supported runtime is **pi 1.5.2 with queue protocol patch 1**
(`1.5.2-yesman.5`, queue capability 4). It provides actual per-occurrence pending
identity, atomic cancellation and same-occurrence append. Original upstream pi 1.5.2 lacks that API; external runtimes retain
normal sending/Copy and only expose recall if they report the supported protocol.

## Runtime selection and distribution

The app honors `YESMAN_PI` exactly. An unrecognized executable/wrapper found as
`pi` on PATH also keeps its execution behavior. The official npm CLI (including
npm's exact standard Windows `.cmd` shim), or no installed pi, selects the managed
runtime. `Settings → pi` and About identify the selected managed/external source
and report its actual available version. Preparing or unknown versions are shown
explicitly; About does not trigger installation or probe a separate global pi.

Managed mode requires **Node.js 22.19+**, with npm for the first preparation. The
app embeds the package manifest, complete npm lockfile, source patch manifest,
queue implementation, installer and MIT license through Go `embed`; these assets
ship in every platform build. It does not modify a global npm/pi installation.

`internal/piruntime` prepares the runtime under `<yesman-data>/.runtime/pi-managed/`
directory. A hash of all embedded assets selects an immutable version directory.
Preparation runs outside the UI and Manager mutex, in a unique temporary staging
directory. It uses `npm ci --ignore-scripts --no-audit --no-fund`: every transitive
version and tarball integrity is fixed by the lockfile, lifecycle scripts do not
run, and npm's download cache remains under the application's runtime directory.
The first preparation needs a network connection; a verified installed cache
needs only Node. Failed preparation is reported and can be retried. The pinned
installer inherits only OS paths and registry/proxy/CA settings; model keys,
GitHub credentials, agent API credentials and Node loader hooks are excluded.
This filter applies only to dependency installation: pi and explicit wrappers
retain their existing environment contract.

Every patched input/output is SHA256 checked. The complete embedded assets,
patched source, queue module and ready manifest are verified before atomic
publication and on first use by each application process. Unexpected existing
cache contents in those verified files fail closed and stay available for diagnosis.
Unpatched dependency files are integrity checked by npm during installation, not
rehash checked on every cached launch. One shared preparation per Runtime instance
has a 10-minute deadline owned by the runtime/app lifecycle. Callers cancel only
their wait: a short Settings deadline or stopping one agent cannot kill an install
another agent needs. App quit cancels and joins the installer subprocess tree and
cleans that attempt's staging directory. Failed attempts can be explicitly retried;
a competing process's completed preparation is accepted only after verification.

The runtime uses pi's original CLI, configuration/resource loader, user agent
directory, providers, auth, models, packages and MCP behavior. It preserves the
original environment, workspace cwd, `--session-dir`, `--continue`, model selection
and production yesman extension. The installer does not read or rewrite pi user
settings. Package-management actions still run pi's explicit package commands.
Explicit external runtimes remain responsible for their own dependencies.

The upstream npm version is `@earendil-works/pi-coding-agent@1.5.2`, MIT licensed;
upstream tag `v1.5.2` resolves to `b4afdddf44e059c67ccf5097e5317d913521b470`.
`assets/LICENSE-pi` preserves the upstream license. Downloaded dependencies retain
the files/licenses published in their integrity-pinned npm packages. The source
patch covers twelve files: core implementation/declarations; AgentSession
implementation/declarations; RPC mode/types/client implementation/declarations;
and the package CLI/RPC entrypoint paths plus a thin entry at the original bundled
CLI path (so npm's existing platform shims also reach patched code). It adds one
queue implementation module.
No dependency code is patched dynamically at runtime.

Rebuild the same dependency as the application in an isolated cache:

```sh
go run ./tools/pi-runtime /tmp/yesman-runtime-cache > runtime.json
# runtime.json contains root, node, argv prefix, and the managed version.
YESMAN_TEST_RUNTIME=/path/from/runtime.json node scripts/check-queue-runtime.mjs
YESMAN_TEST_RUNTIME=/path/from/runtime.json node scripts/check-queue-sdk.mjs
YESMAN_TEST_RUNTIME=/path/from/runtime.json go test -race ./internal/agent ./internal/piruntime
```

Maintainers regenerate `assets/patch.json` from an **unmodified** `npm ci` using
`node tools/pi-runtime/generate-patch.mjs /path/to/pristine-runtime <patch.json>`.
Review both the generator and generated before/after hashes. Any lock, patch or
asset change selects a new cache directory. CI prepares the dependency with this
same Go preparer and runs the real RPC/SDK/Manager fixtures on all three OSes.
The root `protobufjs: 7.6.6` override repeats upstream pi 1.5.2's override because
npm does not inherit a dependency package's overrides; it satisfies the
`@google/genai` dependency's `^7.5.4` range.

Managed pi stays at the app's fixed reviewed version when the global pi or shared
pi packages are updated. A package requiring a newer host may need a later yesman
release or an explicit `YESMAN_PI` choice. There is no automatic npm/latest update
or implicit external fallback. A runtime dependency upgrade must regenerate the
lockfile and patch hashes, review the source delta, and rerun the full native
RPC/SDK/Manager and browser acceptance matrix on all supported platforms.

Old digest directories and the shared npm download cache are retained across
upgrades (each runtime can occupy roughly 154 MB). They may still be used by
running processes. This feature does not automatically delete them or implement
cache GC; an ownership-aware cache collector is optional future work.

## Follow-up promotion and idle recovery

Capability 3 includes the v2 `promote_queued_message` and SDK `promoteQueuedMessage`,
plus Session-owned idle admission and separate host-only input provenance.
The command identifies an existing epoch/queueId plus its item revision and the
Session control generation. Native promotion synchronously moves that same entry
to the tail of steering, preserving other entries and the original message object.
Repeated promotion returns `already_steering` without reordering. Cancel optionally
compares the item revision too; the app supplies it for v2 so concurrent mutations
based on the same snapshot cannot both change the entry. A later intentional
cancel of the updated steering entry remains legal. New Go accepts v1 for recall,
but never infers promotion support from counts or a v1 capability.

The runtime advertises `promoteAllowed` for an active Session without a pending
Abort/settlement barrier, or a fully idle Session that can reserve a queued run. Abort increments its control generation, preventing a
late pre-Stop request from mutating or waking the queue. Promotion does not interrupt
the in-flight provider or any tool: native steering is polled after the assistant
turn and its complete tool batch, subject to existing retry/compaction/hooks and
steering FIFO/mode. The same occurrence may be taken before its promotion ACK
arrives; receipt reconciliation preserves that later state.

An idle Session reserves its existing lifecycle before asynchronous model/auth
checks (bounded and cancellable). The 15-second admission/auth timer is cleared
when priority admission succeeds; it never times out normal context hooks or
pre-run compaction after the request was accepted. Stop and cancellation remain
bound to the live reservation throughout that preparation. Promotion changes priority first, but **does not
take the occurrence**. Preparation uses an isolated copy of a captured steering
prefix while every original stays native-pending and cancellable. After the
production context request, compaction and hooks finish, the core validates the
same epoch, exact entries/revisions/order/mode, Session reservation, control
generation and abort signals. Only then does a synchronous commit consume
next-turn context and those exact queue entries, immediately entering the normal
prompt-recording loop. Appended inputs remain in order for a later checkpoint.

Stop, cancellation, an invalidated batch or preparation failure before this commit
leaves surviving originals pending with their complete text/images. A preparation
failure produces no synthetic assistant turn/usage record; settling without intake
is aborted, not task completion. The additive `queue_resume_start` host event
exposes the real Session preparation as busy so the desktop Stop button works;
it is not `agent_start` or a provider request. A priority acknowledgement can thus
be followed by a still-pending steering item after Stop, which the user can recall
or process with a later explicit input. Already-steering retries never start a run.

After intake commits, a Stop uses the normal loop to record the committed inputs
with its aborted signal; it never discards or re-enqueues them. Input/template
hooks are not repeated, and the original retry/compaction/settle driver remains the
sole Session owner. Process retirement ends native queue authority. Retained
originals have an explicit draft recovery action. Queue promotion remains
available to callers of the legacy protocol. The desktop conversation exposes
recall and shows the native lane under the pending message.

Input identity is carried by batch position and host event envelopes, never on
AgentMessage. A same-object duplicate has distinct occurrences. After recording a
user message, pi appends minimal `yesman-input-v1` custom metadata linked to its
actual entry ID; it contains no image/text payload or queue authority. These custom
records do not enter model context; boundary/compaction hook projections redact
their private data. Live `get_messages` returns a parallel `inputRefs` array; idle
history uses the canonical session projection/entry IDs. `recordedInputs` also
reconciles known receipts after compaction removes their messages from context.
Legacy history remains unknown rather than being matched by body or assistant
answers. Explicitly selected v2 runtimes can still perform their supported queue
operations, but lack this input/history envelope: source association degrades to
unknown, and queue transfers never feed the legacy text/count provenance tracker.
Only Go's window DTO gets the existing `yesman` projection field.

Go promotion never calls Submit or requires a retained original payload. It
captures owner/process/session identity under a short workspace lease, releases
it across RPC and persistence waits, and revalidates before returning a durable
receipt. Ordered events reconcile lost ACKs; old initial submission ACKs cannot
downgrade the changed kind. Restart invalidates live queue authority and never
replays the original intent. No new payloads or queue authority are persisted.

## Same-occurrence append (capability 4)

A later human input can join a human message that is still pending in pi, so pi
takes and the model receives one message instead of several. RPC `prompt` accepts
an optional `appendTo: {epoch, queueId, expectedRevision, controlGeneration}`.
It applies only while the Session is streaming, where the prompt would otherwise
be queued. An idle Session ignores it and starts a run as usual.

The input first passes through its own input hooks, skill/template expansion and
image list. Then one synchronous registry step decides at the exact point pi would
enqueue it. The input is appended to that occurrence when every condition holds:
- it is pending and not claimed by an idle preparation;
- its epoch and item revision match;
- the Session's control generation matches, and the Session is neither stopping nor settling;
- it is in the same lane (steer/follow-up);
- it is the newest pending occurrence in either lane;
- both inputs carry a 48-hex submission ID and the same host origin, and the origin is the user
  (a scheduled run's `schedule` origin is never joined);
- both inputs have the same trusted conversation `context` in their host origin, or neither has
  one (a topic's messages carry `topic:<id>`, so they never share an occurrence with the
  composer's or another topic's);
- fewer than 15 inputs have been appended (at most 16 members: the primary input plus 15);
- the final combination, after both inputs' hooks, fits one message: at most 4 images, each at
  most 5 MiB and together at most 10 MiB, decoded.

Otherwise the input is enqueued as a new occurrence in that same step, so it is
never lost or processed twice. The reply is `{disposition:"queued", append:{outcome,
queueId, itemRevision, enqueued?}}`. Possible outcomes are `appended`, `already_appended`,
`duplicate`, `stale`, `not_pending`, `preparing`, `stale_item`, `stale_control`,
`kind_mismatch`, `not_latest`, `ineligible`, `limit` and `budget` (the final combination would
exceed the image limits above).

A repeated append of a submission ID that is already a segment, a pending primary or
recently retired (1024 bound) changes nothing and enqueues nothing.

The merged message keeps pi's queued shape: one text block, then images in their
original order. The text is the earlier text verbatim, one newline (`"\n"`), then the
later text verbatim; a trailing newline of the earlier text is kept before the
separator. Image-only inputs add no separator. The occurrence keeps its
handle, its primary submission ID, its origin and its timestamp. Its item revision
increases with each append. Snapshot items list the appended IDs as `segments`.
`queue_update` reports state `appended` with `appendedSubmissionId`, and the
eventual `taken` change carries `segments`. The host-only `yesman-input-v1` record
stores the segments next to the primary ID. None of these IDs appear on an
AgentMessage, in hooks or in provider context.

An idle preparation (`runQueuedInput`) **claims** its captured occurrences and the
promoted target. Snapshot items carry `preparing: true` and the transitions are reported as
`preparing`/`pending`. Claimed items remain pending, cancellable and complete;
they are **not** taken. Claimed items cannot be extended, so a later input waits for
its own checkpoint instead of invalidating the preparation. The claim is released
when the preparation commits or ends without intake.

`get_pending_queue` adds `appendAllowed` as a hint. The SDK exposes
`Agent.appendQueuedMessage`, `PromptOptions.appendTo` and `RpcClient.promptAppend`.
Hosts never send `appendTo` to capability ≤3 runtimes. With those runtimes a later
input simply remains its own message.

yesman proposes a target only for the composer's own submissions
(`Manager.SubmitCoalescing`), and only an occurrence that is itself a composer
submission of the same agent generation and session (its receipt's private `coalesce`
fact, never restored after a restart). A scheduled run (`SubmitFollowUp`), a topic's
message (`SubmitInContext`), a plain `Submit`, a worker report or a lead's message is
never a target, and is never appended into one. pi's own checks above apply
independently.

## Protocol and atomicity

Core enqueue returns a unique handle for each occurrence, even if two writers
submit identical text/images or enqueue the same message object twice. Core
records contain the handle, queue kind and optional host submission identity
**separately from the AgentMessage**. Nothing is appended to user text, content
blocks, persisted model messages or input/context/provider hook inputs.

`AgentSession.prompt`, `steer` and `followUp` carry an optional `submissionId` in
the invocation's private options through asynchronous input hooks and expansion.
At actual enqueue it binds to the new native handle. Extension-handled input
creates no pending occurrence. Extension messages without a yesman submission ID
remain native queue entries and cannot be edited through a human receipt row.

Core `drain()` and `cancelQueuedMessage(epoch, queueId)` synchronously mutate the
same pending registry. Exactly one can remove an occurrence. Cancellation after
drain returns `not_pending`, even before a `message_start` observer has seen it.
Cancellation never aborts the current response, clears another item, rebuilds
queues, invokes input hooks again or changes survivor order. `taken` means pi
removed it for dispatch; it does not claim a provider has already read it.

The additive SDK and RPC methods are `getPendingQueue`/`get_pending_queue` and
`cancelQueuedMessage`/`cancel_queued_message`. `get_state` includes `pendingQueue`
and runtime identity. `queue_update` preserves the legacy `steering`/`followUp`
string arrays and adds a revisioned `pending` snapshot with exact transitions.
Legacy display bookkeeping is updated from native membership, never `indexOf`
on user text.

```json
{"type":"get_pending_queue","id":"read-1"}
{"type":"response","id":"read-1","success":true,"data":{"capability":1,"epoch":"session-nonce","revision":12,"items":[{"queueId":"session-nonce:3","submissionId":"48-hex-intent-id","kind":"steer"}],"steering":["message"],"followUp":[]}}
{"type":"cancel_queued_message","id":"cancel-1","epoch":"session-nonce","queueId":"session-nonce:3"}
{"type":"response","id":"cancel-1","success":true,"data":{"outcome":"cancelled","capability":1,"epoch":"session-nonce","revision":13,"items":[],"steering":[],"followUp":[]}}
```

The core keeps the last 1024 cancellation handles for idempotent acknowledgement
recovery. Handles are never reused within an epoch; expired/unknown handles return
`not_pending` without removing anything. A different epoch returns `stale`.
Go's durable submission receipts provide the longer-lived idempotence contract.
Snapshot membership enables the button; the atomic cancellation result decides
whether recall actually succeeded.

## Receipt and original-payload lifecycle

Go sends its immutable submission ID only after a capability handshake. Its
existing durable registration, fingerprint, 256 metadata/64 active bounds,
retention floor and owner-generation checks remain. Exact native transitions can
establish per-item pending membership, `dispatched`, or `cancelled`; unsupported
runtimes keep the previous acceptance/uncertainty semantics.

Complete original text plus ordered `{type,mimeType,data}` images are retained
before pi hooks/normalization in bounded runtime memory: **32 MiB and 64 payloads
per agent, 128 MiB across the Manager**. A record without its original payload has
no Cancel/Edit action. This includes inputs that exceed available cache capacity
and histories restored after a whole app restart. Receipt previews, transformed
transcripts and thumbnails are never used to reconstruct the original input.
Original images/full text are never written to `delivery.json`.

Cancel captures agent `Since`, workspace `HiredAt`, process/session epoch and the
exact handle. It reserves the operation against cleanup, validates retirement and
removal, and calls only native selective removal. The initial workspace lease is
released before RPC; fresh leases verify the same workspace/agent incarnation for
the short commit and final payload-return phases. RPC and durability waits happen
outside both the workspace lease and global Manager mutex, so a frozen pi does not
block Archive or other workspace submissions. A successful result returns original content
only after the cancelled receipt is durable. A disk failure reports failure and
can safely retry the same cancellation/persistence; a lost RPC response is
reconciled by the ordered native event or an idempotent retry. Unknown outcomes
are never automatically resent.

A cancelled submission ID remains bound to its old immutable intent. Submit retry
returns its existing receipt without another dispatch; edited resend allocates a
new ID. After terminal receipt eviction, the persisted floor still rejects the old
ID before starting pi. A Manager restart retains the tombstone. Payload recovery
is available for managed submissions whose originals are still retained.

Managed originals are released as soon as the user message is recorded, or when
an extension handles the input, the receipt expires, or the user acknowledges or
discards recovery. A cancelled input can survive a restart until the UI acknowledges
restoration. Archive and worker removal seal and join the writer so an old callback
cannot attach to a replacement agent. An acknowledged composer draft has the same
in-memory lifetime as any other draft.

## User behavior

Reliable pending human receipt rows show **Cancel and edit**. Their footer uses
**Waiting · follow-up** or **Steering · waiting for input checkpoint**, never
Sending after native acceptance. The conversation is pi's messages; a receipt
draws a row only while it is open: sending/accepted/queued/steering or native
pending, cancelled with a restorable original, `failed`, `interrupted` or
`unconfirmed` with an error until the person hides it (**Hide**) or this window
resends its kept draft unchanged, Pi confirms admission, and any earlier retained
original is durably released. A refusal or an uncertain retry keeps the earlier
recovery action available. An `unconfirmed` answer without an error can show while
it is the latest submission. Other later sends, replies and empty queues never
retire a problem. Its status can arrive afterwards, even on a reloaded page.
Hide remembers receipt IDs in localStorage per conversation. If a managed original
is still available, Hide first waits for the server to remove it from draft storage.
The receipt remains for deduplication and nothing is resent. The person sending that
ID again, or an accepted update
(this generation, newer revision, still retained) that is not finished, drops
the ID from the hidden set and persists that, so the new attempt's outcome shows
live and after reload; lower revisions and earlier generations cannot. If the
set cannot be written, the page tries to remove the whole stored key (every
conversation's Hides); when that works, a reload shows rather than hides. If
removal is refused too, only the page's memory is reliable, and a reload after
storage recovers may read the old Hides again.
Taken (`dispatched`), `handled` and other finished receipts draw
nothing, including after compaction removes their messages from context; their
receipts stay in Go for idempotence and audit. Visibility does not depend on the
eight-entry `Convo.sent` display cache, which only supplies a visible row's exact
text and images, so live and reloaded windows agree. Receipt binding uses only an
explicit recorded submission identity, including identical text/images, so late
events and reloading do not add a second bubble. A recorded message reads as an
ordinary message without a delivery label; being taken does not promise provider
execution has finished. A pending occurrence with appended inputs keeps one
bubble with all member text and images, copied or recalled as a whole. Preparing
still means pending and recallable, not taken. Topics keep their recorded chip
and copy only the person's text; scheduled runs retain their Tasks origin rather
than appearing as a person's typed input. Their canonical request has no
permanent receipt status either. Topic-pane submission history retains its own
recorded state labels.

`SubmissionPayload` reads complete text/images from the bounded original
Go cache, without changing native membership. A visible unbound row may hydrate
from it after display-cache eviction or page reload. Missing payloads stay previews
with an image count. The read rejects retired or sealed owners, old generations
and worktree cleanup. An app restart can restore originals in the managed draft
snapshot that still match a retained receipt.

Consumed, historical,
unknown and unsupported-runtime messages offer Copy only. A nonempty draft or
attachments block recall before any native mutation, with an explanation to send
or clear that draft first. While waiting, only that agent's empty composer is
read-only; agent switching remains available. Late results restore only the
captured generation, never another agent or an intervening draft. Restore occurs
once per client/agent generation, and resend is an explicit user action.

ArrowUp is the default `composer.recall` context action in the shared shortcuts
registry, using the same recall path for this agent's newest reliably pending item.
It applies only to an empty native composer with no attachments and a collapsed
selection at position zero. IME composition, held-key repeat, pending image/send/
recall, and visible dialog/menu/listbox/recording consumers take priority. Settings
can replace or remove this context binding; no competing textarea ArrowUp handler
remains. Palette navigation and ordinary multi-line cursor movement never recall.

Copy reads canonical raw text, preserving Markdown and line breaks, through the
existing clipboard bridge. Pending original text is retrieved from the bounded
payload cache, so long submissions can be copied completely. Complete short
receipts and native transcript text are also copyable. Truncated historical
receipts and image-only inputs disable text copying with explicit labels. Images
are not copied to the clipboard. No history rewrite, fork or response rerun exists.

## Validation boundaries

- `check-queue-runtime.mjs`: actual managed CLI/SDK/native queues/RPC, production
  yesman extension plus third-party transform/context audit and local provider;
  both kinds/modes, concurrent writers, middle cancellation, duplicate retries,
  epoch mismatch, survivor order, post-drain failure and metadata isolation.
- `check-queue-append.mjs`: actual managed CLI/Session/native queue/RPC with the
  production yesman extension, third-party hook audit and a per-call gated local
  provider. It checks one merged occurrence and one provider introduction, text and four images
  in order, duplicate append, every independent outcome, the checkpoint taking the
  occurrence first, the idle-preparation claim with no stall, and Stop during preparation.
- `check-queue-sdk.mjs`: actual managed public core SDK; same-object repeated
  enqueue, 100 interleaved occurrences, ordering/race and bounded terminal handles.
- `TestRecallWithManagedPi`: actual Go Manager and runtime, complete text/images,
  cancelled-ID replay, new-intent resend, durability failure/recovery, full Manager
  restart, archive/same-name generation and no full-payload receipt persistence.
- `check-recall-browser.mjs`: production Chromium/WebKit → controlled bridge
  transport → real Go service/Manager → actual managed pi, deterministic provider;
  toolbar, original bytes, new intent, protected draft, ArrowUp priorities,
  cross-agent late result, pure images, Copy and reload, English/Chinese.
- Controlled bridge-only fixtures still test broader visual/clipboard-error and
  legacy delivery cases. These are not OS clipboard/native MyGo GUI or external
  paid-provider evidence. All new runtime/native tests use synthetic data and
  isolated settings/workspaces; no real user's queue or session is exercised.

The earlier `check-queue-capabilities-pi.mjs` and `probe-queue-cancel-core.mjs`
remain diagnostic/history tools for unmodified installed pi1.5.2. The production
implementation and acceptance evidence use the reproducibly prepared runtime,
not that test-only loader prototype.
