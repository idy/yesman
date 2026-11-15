// Optional integration check: node scripts/check-usage-pi.mjs
// Runs the installed pi in RPC mode with isolated configuration against a
// local, deterministic OpenAI-compatible server, so pi's own
// openai-completions code normalizes the usage and times the responses. It
// never calls a configured real model. Then Go reads the session pi wrote
// (internal/usage TestRealPiSession) and checks the fields Model usage uses:
// model, requested effort, tokens, request start and duration.
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";

const repo = fileURLToPath(new URL("..", import.meta.url));
const temp = await mkdtemp(`${tmpdir()}/yesman-usage-`);
const sessions = `${temp}/sessions/w1`;
await mkdir(`${temp}/config`, { recursive: true });

// The server waits before its first chunk and between chunks, so a real
// duration is measurable, and records what pi asked for.
const FIRST = 400, BETWEEN = 150;
const requests = [];
const server = createServer(async (req, res) => {
  let body = "";
  for await (const chunk of req) body += chunk;
  const ask = JSON.parse(body);
  requests.push({ model: ask.model, reasoning_effort: ask.reasoning_effort, stream: ask.stream, include_usage: ask.stream_options?.include_usage });
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  const chunk = (o) => res.write(`data: ${JSON.stringify({ id: "c1", object: "chat.completion.chunk", created: 1, model: ask.model, ...o })}\n\n`);
  await new Promise((r) => setTimeout(r, FIRST));
  chunk({ choices: [{ index: 0, delta: { role: "assistant", content: "fixture " }, finish_reason: null }] });
  await new Promise((r) => setTimeout(r, BETWEEN));
  chunk({ choices: [{ index: 0, delta: { content: "answer" }, finish_reason: "stop" }] });
  await new Promise((r) => setTimeout(r, BETWEEN));
  // OpenAI counts cached tokens inside prompt_tokens and reasoning tokens
  // inside completion_tokens.
  chunk({ choices: [], usage: { prompt_tokens: 120, completion_tokens: 30, total_tokens: 150, prompt_tokens_details: { cached_tokens: 100 }, completion_tokens_details: { reasoning_tokens: 10 } } });
  res.end("data: [DONE]\n\n");
}).listen(0, "127.0.0.1");
await new Promise((resolve) => server.on("listening", resolve));
const model = (id) => ({ id, name: `Usage ${id}`, reasoning: true, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 100000, maxTokens: 1000 });
await writeFile(`${temp}/config/models.json`, JSON.stringify({
  providers: { "usage-test": { api: "openai-completions", baseUrl: `http://127.0.0.1:${server.address().port}/v1`, apiKey: "fixture", models: [model("m1"), model("m2")] } },
}));
await writeFile(`${temp}/config/settings.json`, JSON.stringify({
  packages: [], extensions: [], skills: [], promptTemplates: [], themes: [],
  compaction: { enabled: false }, retry: { enabled: false }, defaultProjectTrust: "no",
}));

// Do not forward the caller's credentials to the child process.
const env = {
  PATH: process.env.PATH,
  HOME: process.env.HOME,
  TMPDIR: tmpdir(),
  ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
  PI_CODING_AGENT_DIR: `${temp}/config`,
};
const pi = spawn(process.env.YESMAN_TEST_PI || "pi",
  ["--mode", "rpc", "--provider", "usage-test", "--model", "m1", "--session-dir", sessions, "--no-builtin-tools", "--no-extensions"],
  { cwd: temp, env });
const records = [];
let buffer = "", stderr = "", spawnError;
pi.on("error", (err) => { spawnError = err; });
pi.stdout.on("data", (data) => {
  buffer += data;
  let end;
  while ((end = buffer.indexOf("\n")) >= 0) {
    const line = buffer.slice(0, end);
    buffer = buffer.slice(end + 1);
    if (line.trim()) records.push(JSON.parse(line));
  }
});
pi.stderr.on("data", (data) => { stderr += data; });
async function until(find) {
  const deadline = Date.now() + 30000;
  while (!find()) {
    if (spawnError) throw spawnError;
    if (Date.now() > deadline) throw new Error(`pi timed out: ${stderr}\n${JSON.stringify(records.slice(-5))}`);
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  return find();
}
let sequence = 0;
async function call(command) {
  const id = `usage-${++sequence}`;
  pi.stdin.write(JSON.stringify({ ...command, id }) + "\n");
  const result = await until(() => records.find((r) => r.type === "response" && r.id === id));
  assert.equal(result.success, true, JSON.stringify(result));
  return result;
}
// One prompt, until pi settles; the wall-clock window around it.
async function run(text) {
  const settled = records.filter((r) => r.type === "agent_settled").length;
  const start = Date.now();
  await call({ type: "prompt", message: text });
  await until(() => records.filter((r) => r.type === "agent_settled").length > settled);
  return { start, end: Date.now() };
}

try {
  await call({ type: "set_thinking_level", level: "high" });
  const first = await run("one");
  await call({ type: "set_model", provider: "usage-test", modelId: "m2" });
  await call({ type: "set_thinking_level", level: "low" });
  const second = await run("two");
  pi.kill();
  await new Promise((resolve) => pi.once("exit", resolve));
  // pi requested each effort it recorded.
  assert.deepEqual(requests.map((r) => [r.model, r.reasoning_effort, r.stream, r.include_usage]), [["m1", "high", true, true], ["m2", "low", true, true]]);
  const go = spawnSync("go", ["test", "./internal/usage", "-run", "^TestRealPiSession$", "-count=1", "-v"], {
    cwd: repo,
    stdio: "inherit",
    env: { ...process.env, YESMAN_USAGE_PI_SESSIONS: sessions, YESMAN_USAGE_PI_RUNS: JSON.stringify({ first, second, minMs: FIRST + 2 * BETWEEN }) },
  });
  assert.equal(go.status, 0, "go test failed");
  console.log(`pi ${spawnSync(process.env.YESMAN_TEST_PI || "pi", ["--version"], { env }).stdout.toString().trim()}: session usage, effort and timing checked`);
} finally {
  pi.kill();
  server.close();
  await rm(temp, { recursive: true, force: true });
}
