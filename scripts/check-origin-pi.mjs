// Optional integration check: node scripts/check-origin-pi.mjs
// Runs the installed pi with the production extension, isolated configuration
// and a local pi-messages backend: no real model, key or user settings. A fake
// yesman API plays the app's in-flight prompt claims (internal/agent/origin.go).
// It checks that user messages record who wrote them only when that is known,
// live and in history after a restart, and that prompt text, images,
// third-party input handlers and the requests sent to the model are unchanged.
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, readFile, writeFile, mkdir, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";

const repo = fileURLToPath(new URL("..", import.meta.url));
const temp = await mkdtemp(`${tmpdir()}/yesman-origin-`);
const extension = fileURLToPath(new URL("../pi/yesman.ts", import.meta.url));
const hooks = fileURLToPath(new URL("./fixtures/origin-hooks.ts", import.meta.url));

// The app's own fingerprints of a few inputs, to compare with the extension's.
const generated = spawnSync("go", ["test", "./internal/agent", "-run", "^TestOriginFingerprintFixture$", "-count=1"], {
  cwd: repo,
  env: { ...process.env, YESMAN_TEST_ORIGIN_FIXTURE: `${temp}/vectors.json` },
  encoding: "utf8",
});
assert.equal(generated.status, 0, generated.stderr || generated.stdout);
const vectors = JSON.parse(await readFile(`${temp}/vectors.json`, "utf8"));

// The fake app: claims[fingerprint] holds the origins of prompts "in flight",
// and next answers the next lookup of another fingerprint, as the app would
// for the one prompt it has in flight. slow outlives the extension's timeout.
const claims = new Map();
const asked = [];
let next = null;
let slow = false;
const api = createServer((req, res) => {
  let body = "";
  req.on("data", (d) => (body += d));
  req.on("end", () => {
    if (req.url === "/v1/origin") {
      assert.equal(req.headers.authorization, "Bearer fixture");
      const { fingerprint } = JSON.parse(body);
      asked.push(fingerprint);
      let origin = claims.get(fingerprint)?.shift();
      if (!origin && next) [origin, next] = [next, null];
      const answer = () => {
        res.setHeader("Content-Type", "application/json");
        res.end(JSON.stringify(origin ? { origin } : {}));
      };
      if (slow) [slow] = [false, setTimeout(answer, 3000)];
      else answer();
      return;
    }
    res.setHeader("Content-Type", req.url === "/v1/context" ? "text/plain" : "application/json");
    res.end(req.url === "/v1/context" ? "Fixture context" : JSON.stringify({ env: {}, hide: [] }));
  });
}).listen(0, "127.0.0.1");

// The model: pi's own pi-messages protocol, held until the gate opens.
const requests = [];
let open = false;
const held = [];
const reply = (res) => {
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  const send = (e) => res.write(`data: ${JSON.stringify(e)}\n\n`);
  const usage = { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } };
  send({ type: "start" });
  send({ type: "text_start", contentIndex: 0 });
  send({ type: "text_delta", contentIndex: 0, delta: "fixture" });
  send({ type: "text_end", contentIndex: 0, content: "fixture" });
  send({ type: "done", reason: "stop", usage });
  res.end();
};
const model = createServer((req, res) => {
  let body = "";
  req.on("data", (d) => (body += d));
  req.on("end", () => {
    requests.push(body);
    if (open) reply(res);
    else held.push(res);
  });
}).listen(0, "127.0.0.1");
await Promise.all([api, model].map((s) => new Promise((resolve) => s.listening ? resolve() : s.on("listening", resolve))));

await mkdir(`${temp}/config`);
await writeFile(`${temp}/config/settings.json`, JSON.stringify({
  packages: [], extensions: [], skills: [], promptTemplates: [], themes: [],
  compaction: { enabled: false }, retry: { enabled: false }, defaultProjectTrust: "no",
}));
await writeFile(`${temp}/config/models.json`, JSON.stringify({
  providers: {
    fixture: {
      baseUrl: `http://127.0.0.1:${model.address().port}`,
      api: "pi-messages",
      apiKey: "fixture",
      models: [{ id: "m", name: "Fixture", input: ["text", "image"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 100000, maxTokens: 1000 }],
    },
  },
}));
// Do not forward the caller's credentials to the child process.
const env = {
  PATH: process.env.PATH,
  HOME: process.env.HOME,
  TMPDIR: tmpdir(),
  ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
  PI_CODING_AGENT_DIR: `${temp}/config`,
  YESMAN_API: `http://127.0.0.1:${api.address().port}`,
  YESMAN_TOKEN: "fixture",
  YESMAN_AGENT: "fixture/lead",
  YESMAN_ROLE: "lead",
};

function startPi(...extra) {
  const pi = spawn(process.env.YESMAN_TEST_PI || "pi", [
    "--mode", "rpc", "--provider", "fixture", "--model", "m",
    "--session-dir", `${temp}/sessions`, "--no-builtin-tools",
    "-e", extension, "-e", hooks, ...extra,
  ], { cwd: temp, env });
  const records = [];
  let buffer = "", stderr = "", spawnError, sequence = 0;
  pi.on("error", (err) => (spawnError = err));
  pi.stdout.on("data", (data) => {
    buffer += data;
    let end;
    while ((end = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, end);
      buffer = buffer.slice(end + 1);
      if (line.trim()) records.push(JSON.parse(line));
    }
  });
  pi.stderr.on("data", (data) => (stderr += data));
  async function until(find, ms = 20000) {
    const deadline = Date.now() + ms;
    while (!find()) {
      if (spawnError) throw spawnError;
      if (Date.now() > deadline) throw new Error(`pi timed out: ${stderr}\n${JSON.stringify(records.slice(-5))}`);
      await new Promise((resolve) => setTimeout(resolve, 5));
    }
    return find();
  }
  async function call(command) {
    const id = `test-${++sequence}`;
    pi.stdin.write(JSON.stringify({ ...command, id }) + "\n");
    const result = await until(() => records.find((r) => r.type === "response" && r.id === id));
    assert.equal(result.success, true, JSON.stringify(result));
    return result;
  }
  async function stop() {
    pi.stdin.end();
    const kill = setTimeout(() => pi.kill("SIGKILL"), 1000);
    await new Promise((resolve) => (pi.exitCode !== null || pi.signalCode !== null || spawnError ? resolve() : pi.once("exit", resolve)));
    clearTimeout(kill);
  }
  return { records, until, call, stop };
}

const id = (n) => n.toString(16).padStart(48, "0");
// Inputs with the app's own fingerprints are claimed by them.
const claim = (vector, origin) => claims.set(vector.fingerprint, [...(claims.get(vector.fingerprint) ?? []), origin]);
const firstText = (m) => (typeof m.content === "string" ? m.content : m.content.find((b) => b.type === "text")?.text ?? "");

let pi = startPi();
try {
  await pi.call({ type: "get_state" });
  // A third-party handler still takes its own input; nothing is recorded.
  assert.equal((await pi.call({ type: "prompt", message: "fixture-ping", streamingBehavior: "steer" })).data.disposition, "handled");

  // An idle user submission starts a run and is bound to its receipt.
  const [hello, blank, two, report] = vectors;
  claim(hello, { source: "user", submission: id(1) });
  assert.equal((await pi.call({ type: "prompt", message: hello.text })).data.disposition, "started");
  const first = await pi.until(() => pi.records.find((r) => r.type === "message_end" && r.message?.role === "user"));
  assert.deepEqual(first.message.yesman, { source: "user", submission: id(1) });
  assert.equal(asked.at(-1), hello.fingerprint, "the extension's fingerprint differs from the app's");

  // Queued while the model is held: a report, identical user texts, a person
  // typing yesman's own prefix, images, a slow lookup, the same text from two
  // writers, and an input another handler rewrites.
  // Worker names may start with a digit.
  claim(report, { source: "yesman", kind: "report", worker: "2nd-pass" });
  const queued = [
    { message: report.text, streamingBehavior: "followUp" },
    { message: "same", streamingBehavior: "steer", origin: { source: "user", submission: id(2) } },
    { message: "same", streamingBehavior: "steer", origin: { source: "user", submission: id(3) } },
    { message: '[yesman] Worker "w9" finished a run. Its final message:\n\ntyped by a person', streamingBehavior: "steer" },
    { message: two.text, images: two.images, streamingBehavior: "steer" },
    { message: blank.text, images: blank.images, streamingBehavior: "steer" },
    { message: "dup", streamingBehavior: "steer", origin: { source: "user", submission: id(7) } },
    { message: "dup", streamingBehavior: "followUp", origin: { source: "yesman", kind: "tasks" } },
    { message: "fixture-transform", streamingBehavior: "steer", origin: { source: "user", submission: id(8) } },
    { message: "slow", streamingBehavior: "steer", origin: { source: "user", submission: id(9) }, slow: true },
  ];
  claim(two, { source: "user", submission: id(4) });
  claim(blank, { source: "user", submission: id(5) });
  for (const q of queued) {
    const before = asked.length;
    [next, slow] = [q.origin ?? null, !!q.slow];
    const started = Date.now();
    const result = await pi.call({ type: "prompt", message: q.message, images: q.images, streamingBehavior: q.streamingBehavior });
    assert.equal(result.data.disposition, "queued");
    if (q.slow) assert(Date.now() - started < 2900, "a slow lookup held the prompt past its timeout");
    assert.equal(asked.length, before + 1, `no lookup for ${q.message}`);
  }
  assert(pi.records.some((r) => r.type === "queue_update" && r.steering.length === 8 && r.followUp.length === 2));
  for (const v of [two, blank, report]) assert(asked.includes(v.fingerprint), "the extension's image/report fingerprint differs from the app's");

  // Let the model answer everything, then read the history.
  open = true;
  for (const res of held.splice(0)) reply(res);
  await pi.until(() => pi.records.find((r) => r.type === "agent_settled"), 30000);
  const live = pi.records.filter((r) => r.type === "message_end" && r.message?.role === "user").map((r) => r.message);
  const users = (await pi.call({ type: "get_messages" })).data.messages.filter((m) => m.role === "user");
  assert.deepEqual(users.map((m) => m.yesman ?? null), live.map((m) => m.yesman ?? null), "live and history disagree");
  const byText = (text) => users.filter((m) => firstText(m) === text || firstText(m).startsWith(`${text}\n\n`));
  assert.deepEqual(byText(report.text).map((m) => m.yesman), [{ source: "yesman", kind: "report", worker: "2nd-pass" }]);
  // Two submissions of the same text are the user's, bound to neither receipt.
  assert.deepEqual(byText("same").map((m) => m.yesman), [{ source: "user" }, { source: "user" }]);
  // A person's text is never taken for yesman's by its prefix.
  assert.deepEqual(byText('[yesman] Worker "w9" finished a run. Its final message:\n\ntyped by a person').map((m) => m.yesman), [undefined]);
  assert.deepEqual(byText(two.text).map((m) => m.yesman), [{ source: "user", submission: id(4) }]);
  assert.equal(byText(two.text)[0].content.filter((b) => b.type === "image").length, 2);
  const oneImage = users.filter((m) => m.content.filter((b) => b.type === "image").length === 1);
  assert.deepEqual(oneImage.map((m) => [firstText(m).split("\n\n")[0], m.yesman]), [["", { source: "user", submission: id(5) }]]);
  // The same text from a person and from yesman: neither is said to be yesman's.
  assert.deepEqual(byText("dup").map((m) => m.yesman), [undefined, undefined]);
  // An input another handler rewrote stays unattributed; the hook's text is kept.
  assert.deepEqual(byText("transformed by a hook").map((m) => m.yesman), [undefined]);
  assert.equal(byText("fixture-transform").length, 0);
  // A lookup that timed out leaves the message as a person's, unbound.
  assert.deepEqual(byText("slow").map((m) => m.yesman), [undefined]);

  // The model's requests carry the same text and images, never the field.
  assert(requests.length >= 2);
  for (const body of requests) {
    const payload = JSON.parse(body);
    for (const m of payload.context.messages) assert.equal(m.yesman, undefined, "a request carried the origin field");
  }
  const last = JSON.parse(requests.at(-1)).context.messages.filter((m) => m.role === "user");
  assert.deepEqual(last.map(firstText), users.map(firstText));
  assert.deepEqual(last.map((m) => m.content), users.map((m) => m.content));

  // The session log keeps the origins, and a restarted process reads them back.
  const logs = (await readdir(`${temp}/sessions`)).filter((f) => f.endsWith(".jsonl"));
  assert.equal(logs.length, 1);
  const log = await readFile(`${temp}/sessions/${logs[0]}`, "utf8");
  assert(log.includes('"yesman":{"source":"yesman","kind":"report","worker":"2nd-pass"}'));
  await pi.stop();
  pi = startPi("--continue");
  const resumed = (await pi.call({ type: "get_messages" })).data.messages.filter((m) => m.role === "user");
  assert.deepEqual(resumed.map((m) => m.yesman ?? null), users.map((m) => m.yesman ?? null));
  console.log("PASS: real pi records user message origins only when known, live and after restart; text, images, input hooks, queue and model requests unchanged");
} finally {
  await pi.stop();
  open = true;
  for (const res of held.splice(0)) res.destroy();
  await Promise.all([api, model].map((s) => new Promise((resolve) => s.close(resolve))));
  await rm(temp, { recursive: true, force: true });
}
