# Final .50 to .51 score CSV package acceptance

QA-only runner for a disposable GitHub-hosted ARM64 Mac. It never builds,
modifies or resigns an application, imports credentials, asks for permissions,
or uses a workstation profile. Product source and metadata PR112 are unchanged.

The QA branch overrides the already-registered `macos-draft-smoke.yml` workflow
only on `qa/model-score-release-051`. There is no push trigger or automatic run.
The default branch's ordinary ARM/Intel final-draft startup workflow is unchanged
and remains a separate release gate.

## Inputs and bounded execution

After the immutable .51 tag and final asset validation exist, the publisher
supplies `expected_binding` as JSON, containing:

- `qaSource`: the independently reviewed full commit of this QA branch.
- `candidate.source`: exact immutable .51 tag commit, whose tree must equal
  `c970394849af0fca550f0150ef14bc1661cb0a60`.
- `candidate.releaseID`: exact unpublished final .51 release.
- `candidate.archive` and `candidate.manifest`: each exactly
  `{id,name,size,digest}`, from the validated final draft. Names must be
  `yesman-0.1.51-darwin-arm64.tar.gz` and `update-darwin-arm64.json`.
- `candidate.binarySHA256`: independently extracted final ARM64 main image hash.
- `candidate.assetSetSHA256`: SHA-256 of all final draft asset identities,
  sorted by name and serialized with JSON sorted keys and compact separators.
  Each identity includes only `id,name,size,digest`.

No placeholders or missing final values are accepted. Old .50 source, release,
archive, manifest and image identities are fixed in `validation.py`. The first
job has `contents:write` for draft visibility but only performs fixed GitHub
GET/downloads and writes transfer data. It never checks out or executes repo or
App code. It verifies immutable refs, whole source trees, exact assets, complete
final asset-set identity and exact production web/config/CSV data, then rechecks
remote identity. The separate native job checks same-run transfer and exact QA
checkout, excludes acquisition/signing/model credentials, verifies Ed25519 over
SHA-256 per pinned MyGo v0.9.0, and checks strict bundle signing, binary source,
architecture, version and embedded production bytes before any App launch.

One dispatch, attempt 1 only: download budget 8 minutes, native budget 12 minutes.
No automatic retry, rerun or matrix. Failed runs retain proof and cannot pass via
forced cleanup. The final binding and request receipt must be saved before the
publisher sends the one dispatch. Unknown effects are reconciled before retry.

## Observed route and proof

The route is **manual full archive replacement** at one private installation
path, retaining one fresh profile and external data directory. Seed the
compatible CSV and `scorePatchDisabled:false` outside the bundle. .50 has no
patch import/application support: its real Models/Go values must show the real
unpatched AA baseline. After normal .50 exit, retain its old bundle and copy the
exact .51 bundle with `symlinks=True`. Strict signing and full entry type/mode/
link/byte inventories remain unchanged. Start .51, then normal exit and cold
reopen .51 without replacing its image again.

All three real native processes must show visible windows and return the exact
version/source/profile and numeric/provenance assertions through the real MyGo
bridge and production Models renderer. The observer is the only addition to a
copy of the package-exact `index.html` through `YESMAN_WEB_DIR`; all remaining web
bytes are identical and checked inside each binary. It uses only read methods.
The deterministic catalog supplies three models and rejects prompts; no agent
or model service is started. A rejecting loopback proxy blocks updater network.

The CSV SHA is `77ead3382573b39ccd120c87d80d342567472a216237e3895d71247a6770c190`.
The settings SHA is `c935d4526c8f7f2c098d24c8e02793703ac0b267a35b3300cc586ae0c7b67223`.
Astra high keeps CSV intelligence/cost zero and inherits 60.7 tok/s, 53.6% coding
and 4.9 minutes. Sol high overrides speed to 123.5 and time to zero, inheriting
54.9 intelligence, 49.4% coding and 0.56 USD. Both xhigh options are untouched
effort controls; the partial CSV-only row retains unavailable intelligence,
37.125 tok/s and explicit zero minutes. Values and bytes must survive cold open.

Both actual releases contain the same AA snapshot. Changed embedded values are
covered only by the accepted R2 staged test, not this release pair. This runner
does not exercise the automatic updater or delta path, native import dialog,
actual Lead agents, Apple/TCC continuity or any production profile. Standard
all-platform final assets/installers/scripts/checksums/signatures/six manifests
and downloaded ARM/Intel startup remain separate release requirements.

## Reconstruct and validate

Run `python3 -B qa/score-release051/generate-workflow.py` to reproduce the inline
acquisition workflow exactly. `validate-offline.py PRIVATE_TASK_ROOT` checks
binding, acquisition refusal, archive safety, contract discrimination, credential
filtering and hosted-only admission. Its root needs the exact .50 archive and
manifest under `official050/`; it extracts/copies them and calls codesign only.
It never calls the native entry with fake hosted flags. Use a new private task
root per test; no production app is launched. The release evidence directory
records additional actual signature tamper and loopback/catalog controls.
