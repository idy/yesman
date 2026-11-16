# Managed pi and native message recall

yesman's default supported runtime is **pi 1.5.2 with queue protocol patch 1**
(`1.5.2-yesman.4`). It provides actual per-occurrence pending identity and atomic
cancellation. Original upstream pi 1.5.2 lacks that API; external runtimes retain
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
needs only Node. Failed preparation is reported and can be retried.

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
sole Session owner. Full process retirement still invalidates volatile queue and
payload authority rather than replaying old intents. The message footer offers **Send now** only for a confirmed
follow-up whose live runtime admits promotion. Already-steering messages show
that they are waiting for an input checkpoint. A pending action reserves that
item in the page, so repeated clicks cannot issue another mutation; generation
checks discard late responses after switching to a replacement agent.

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
ID before starting pi. Full Manager restart retains the tombstone but cannot offer
full-payload restoration after the volatile cache is lost.

Original payloads are released when taken, definitively failed/handled, retired,
evicted, or explicitly acknowledged after the UI restores them. A cancelled
payload can survive a normal pi-process stop for same-agent recovery, but not
workspace/worker retirement. Archive/removal continue sealing and joining writers;
old callbacks cannot attach to a same-name replacement. If the UI disconnects
before acknowledging restoration, the cancelled row offers **Return recalled
message to input** while its original payload remains available. Once acknowledged,
restored drafts have the same in-memory lifetime as other composer drafts.

## User behavior

Reliable pending human receipt rows show **Cancel and edit**. Their footer uses
**Waiting · follow-up** or **Steering · waiting for input checkpoint**, never
Sending after native acceptance. The conversation is pi's messages; a receipt
draws a row only while it is open: sending/accepted/queued/steering or native
pending, cancelled with a restorable original, the latest `failed`/`unconfirmed`
submission, or `interrupted` until a later submission is bound to another pi
session. Taken (`dispatched`), `handled` and other finished receipts draw
nothing, including after compaction removes their messages from context; their
receipts stay in Go for idempotence and audit. Visibility does not depend on the
eight-entry `Convo.sent` display cache, which only supplies a visible row's exact
text and images, so live and reloaded windows agree. Receipt binding uses only an
explicit recorded submission identity, including identical text/images, so late
events and reloading do not add a second bubble. A recorded message reads as an
ordinary message without a delivery label; being taken does not promise provider
execution has finished.

`SubmissionPayload` reads complete text/images from the existing bounded volatile
Go cache, without changing native membership. A visible unbound row may hydrate
from it after display-cache eviction or page reload. Only that DOM row holds the
result; there is no additional full-payload map or disk image store. Missing
payloads stay previews with an image count. The read rejects retired/sealed
owners, old generations and worktree cleanup. Full-app restarts cannot restore
payloads that no longer exist.

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
