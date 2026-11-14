// Optional integration check: node scripts/check-delivery-pi.mjs
// Runs the installed pi with the production extension, isolated configuration,
// and a deterministic local provider. It never calls a configured real model.
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import {
  mkdtemp,
  readFile,
  writeFile,
  mkdir,
  readdir,
  rm,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";

const temp = await mkdtemp(`${tmpdir()}/yesman-delivery-`);
const extension = fileURLToPath(new URL("../pi/yesman.ts", import.meta.url));
const provider = fileURLToPath(
  new URL("./fixtures/delivery-provider.ts", import.meta.url),
);
await mkdir(`${temp}/config`);
await writeFile(
  `${temp}/config/settings.json`,
  JSON.stringify({
    packages: [],
    extensions: [],
    skills: [],
    promptTemplates: [],
    themes: [],
    compaction: { enabled: false },
    retry: { enabled: false },
    defaultProjectTrust: "no",
  }),
);
const api = createServer((req, res) => {
  res.setHeader(
    "Content-Type",
    req.url === "/v1/context" ? "text/plain" : "application/json",
  );
  res.end(
    req.url === "/v1/context"
      ? "Fixture context"
      : JSON.stringify({ env: {}, hide: [] }),
  );
}).listen(0, "127.0.0.1");
await new Promise((resolve) => api.on("listening", resolve));
// Do not forward the caller's credentials to the child process.
const env = {
  PATH: process.env.PATH,
  HOME: process.env.HOME,
  TMPDIR: tmpdir(),
  ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
  PI_CODING_AGENT_DIR: `${temp}/config`,
  YESMAN_API: `http://127.0.0.1:${api.address().port}`,
  YESMAN_TOKEN: "fixture",
  YESMAN_AGENT: "fixture/worker",
  YESMAN_ROLE: "worker",
  DELIVERY_AUDIT: `${temp}/audit`,
  DELIVERY_GATE: `${temp}/gate`,
};
const pi = spawn(
  process.env.YESMAN_TEST_PI || "pi",
  [
    "--mode",
    "rpc",
    "--provider",
    "delivery-test",
    "--model",
    "m",
    "--session-dir",
    `${temp}/sessions`,
    "--no-builtin-tools",
    "-e",
    extension,
    "-e",
    provider,
  ],
  { cwd: temp, env },
);
const records = [];
let buffer = "",
  stderr = "",
  spawnError;
pi.on("error", (err) => {
  spawnError = err;
});
pi.stdout.on("data", (data) => {
  buffer += data;
  let end;
  while ((end = buffer.indexOf("\n")) >= 0) {
    const line = buffer.slice(0, end);
    buffer = buffer.slice(end + 1);
    if (line.trim()) records.push(JSON.parse(line));
  }
});
pi.stderr.on("data", (data) => {
  stderr += data;
});
let sequence = 0;
async function until(find) {
  const deadline = Date.now() + 20000;
  while (!find()) {
    if (spawnError) throw spawnError;
    if (Date.now() > deadline)
      throw new Error(
        `pi timed out: ${stderr}\n${JSON.stringify(records.slice(-5))}`,
      );
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  return find();
}
async function call(command) {
  const id = `test-${++sequence}`;
  pi.stdin.write(JSON.stringify({ ...command, id }) + "\n");
  const result = await until(() =>
    records.find((r) => r.type === "response" && r.id === id),
  );
  assert.equal(result.success, true, JSON.stringify(result));
  return result;
}
try {
  await call({ type: "get_state" });
  // A third-party exact input handler retains its native behavior. No receipt
  // metadata enters text, image blocks, or the extension's input event.
  const handled = await call({
    type: "prompt",
    message: "fixture-ping",
    streamingBehavior: "steer",
  });
  assert.equal(handled.data.disposition, "handled");
  const image = {
    type: "image",
    mimeType: "image/png",
    data: "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==",
  };
  const first = await call({
    type: "prompt",
    message: "same",
    streamingBehavior: "steer",
  });
  assert.equal(first.data.disposition, "started");
  await until(() =>
    records.find((r) => r.type === "message_end" && r.message?.role === "user"),
  );
  for (const [index, behavior] of [
    [1, "steer"],
    [2, "followUp"],
    [3, "steer"],
    [4, "steer"],
  ]) {
    // This malformed-but-recognized PNG exercises pi's appended image hints.
    const omitted = {
      ...image,
      data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=",
    };
    const result = await call({
      type: "prompt",
      message: index >= 3 ? "" : "same",
      images:
        index === 3 ? [image, image] : index === 4 ? [omitted] : undefined,
      streamingBehavior: behavior,
    });
    assert.equal(result.data.disposition, "queued");
  }
  let history = await call({ type: "get_messages" });
  assert.equal(
    history.data.messages.filter((m) => m.role === "user").length,
    1,
  );
  assert.equal(
    records.filter(
      (r) => r.type === "message_end" && r.message?.role === "user",
    ).length,
    1,
  );
  assert(
    records.some(
      (r) =>
        r.type === "queue_update" &&
        r.steering.length === 3 &&
        r.followUp.length === 1,
    ),
  );
  await writeFile(`${temp}/gate`, "release");
  await until(() => records.find((r) => r.type === "agent_settled"));
  history = await call({ type: "get_messages" });
  const users = history.data.messages.filter((m) => m.role === "user");
  assert.equal(users.length, 5);
  assert(users.every((m) => m.yesmanDeliveryId === undefined));
  assert.equal(
    users.filter((m) => m.content.some((block) => block.type === "image"))
      .length,
    1,
  );
  assert.equal(JSON.stringify(users).includes("<!--yesman-delivery:"), false);
  const audit = (await readFile(`${temp}/audit`, "utf8"))
    .trim()
    .split("\n")
    .map(JSON.parse);
  assert(audit.length >= 3);
  assert.equal(JSON.stringify(audit).includes("<!--yesman-delivery:"), false);
  for (const name of (await readdir(`${temp}/sessions`)).filter((file) =>
    file.endsWith(".jsonl"),
  )) {
    const log = await readFile(`${temp}/sessions/${name}`, "utf8");
    assert.equal(log.includes("<!--yesman-delivery:"), false);
    assert.equal(log.includes("yesmanDeliveryId"), false);
  }
  console.log(
    "PASS: real pi acceptance/queues, unchanged input hooks, duplicate text, images and native history",
  );
} finally {
  pi.stdin.end();
  const kill = setTimeout(() => pi.kill("SIGKILL"), 1000);
  await new Promise((resolve) => {
    if (pi.exitCode !== null || pi.signalCode !== null || spawnError) resolve();
    else pi.once("exit", resolve);
  });
  clearTimeout(kill);
  await new Promise((resolve) => api.close(resolve));
  await rm(temp, { recursive: true, force: true });
}
