// Optional browser check of failed runs in a conversation and in the
// sidebar, in the production page against a controlled bridge, in Chromium
// and WebKit:
//
//   YESMAN_TEST_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node scripts/check-error-flow-browser.mjs
//
// The history is synthetic, shaped like a lead out of provider quota: a
// person's question with an image, then seven worker reports that each start
// a run of thinking and tool calls ending on the same 429 error, with a
// queued and a failed submission. The runs fold into one closed row that
// opens, by keyboard, to the error in full and each run; people, images,
// replies, part of a reply and receipts stay as they were; history read again
// and new runs keep one row. The sidebar shows a red exclamation mark only
// for an agent whose last finished run failed, never for one at work, asking,
// needing you or asleep without an error. Set YESMAN_ERROR_FLOW_BEFORE to an
// earlier page's web directory to draw the same history there; screenshots
// and a JSON summary go to YESMAN_ERROR_FLOW_OUT (default: the temp dir).
// A browser bridge is not the native window, pi or a real provider.
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || "playwright");
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { createServer } from "node:http";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import assert from "node:assert/strict";

const out = process.env.YESMAN_ERROR_FLOW_OUT || tmpdir();
await mkdir(out, { recursive: true });
const serve = (root) =>
  createServer(async (req, res) => {
    try {
      const p = new URL(req.url, "http://localhost").pathname;
      const type = p.endsWith(".js") ? "application/javascript" : p.endsWith(".css") ? "text/css" : p.endsWith(".woff2") ? "font/woff2" : p.endsWith(".png") ? "image/png" : p.endsWith(".svg") ? "image/svg+xml" : "text/html";
      res.setHeader("Content-Type", type);
      res.end(await readFile(root + (p === "/" ? "/index.html" : p)));
    } catch {
      res.writeHead(404).end();
    }
  }).listen(0, "127.0.0.1");
const after = serve(fileURLToPath(new URL("../web", import.meta.url)));
const before = process.env.YESMAN_ERROR_FLOW_BEFORE ? serve(process.env.YESMAN_ERROR_FLOW_BEFORE) : null;
await Promise.all([after, before].filter(Boolean).map((s) => new Promise((r) => (s.listening ? r() : s.on("listening", r)))));

const png = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==";
const sub = (n) => n.toString(16).padStart(48, "0");
const LIMIT = "server_error: Codex Magpie: Codex: The usage limit has been reached (429: you can retry your request)";
const RATE = "rate_limit_exceeded: Too many requests; retry after 20s (429) <b>not markup</b>";
const T0 = 1_794_700_000_000;

function builder() {
  let ts = T0;
  const at = () => (ts += 7000);
  let calls = 0;
  const user = (text, yesman, images = 0) => ({ role: "user", timestamp: at(), ...(yesman ? { yesman } : {}), content: [{ type: "text", text }, ...Array.from({ length: images }, () => ({ type: "image", mimeType: "image/png", data: png }))] });
  const assistant = (content, stopReason = "stop", extra = {}) => ({ role: "assistant", timestamp: at(), stopReason, content, api: "openai-codex", provider: "codex", model: "gpt-5.1-codex", usage: {}, ...extra });
  const th = (thinking) => ({ type: "thinking", thinking, thinkingSignature: "opaque" });
  const tx = (text) => ({ type: "text", text });
  const call = (name) => ({ type: "toolCall", id: `c${++calls}`, name, arguments: {} });
  const result = (c) => ({ role: "toolResult", timestamp: at(), toolCallId: c.id, toolName: c.name, isError: false, content: [{ type: "text", text: "ok" }] });
  const report = (worker) => user(`[yesman] Worker "${worker}" finished a run. Its final message:\n\nserver_error: usage limit (429) — a report that mentions an error`, { source: "yesman", kind: "report", worker });
  // The run of the screenshot: a report, five thoughts, two tools, the error.
  const failedRun = (worker, message = LIMIT) => {
    const a = call("yesman_board"), b = call("yesman_agents");
    return [
      report(worker),
      assistant([th("**Reading the report**"), th("**Checking the board**"), th("c"), a], "toolUse"),
      result(a),
      assistant([th("d"), th("e"), b], "toolUse"),
      result(b),
      assistant([], "error", { errorMessage: message }),
    ];
  };
  return { user, assistant, th, tx, report, failedRun };
}

function quotaHistory() {
  const b = builder();
  return [
    b.user("这些 worker 都报 429 了，帮我看看？", { source: "user", submission: sub(1) }, 1),
    b.assistant([b.tx("我先看一下各 worker 的报告。")]),
    ...Array.from({ length: 7 }, (_, i) => b.failedRun(`w${i + 1}`)).flat(),
  ];
}

// Distinct errors, a person's message, a real reply and part of a reply.
function mixedHistory() {
  const b = builder();
  return [
    ...b.failedRun("w1"), ...b.failedRun("w2", RATE), ...b.failedRun("w3"),
    b.user("error 429 again? Check the quota.", { source: "user" }),
    ...b.failedRun("w4"), ...b.failedRun("w5"),
    b.assistant([b.tx("Recovered: the board is up to date.")]),
    ...b.failedRun("w6"),
    b.report("w7"), b.assistant([b.tx("Here is what I found before it failed: …")], "error", { errorMessage: LIMIT }),
    ...b.failedRun("w8"), ...b.failedRun("w9"),
  ];
}

const deliveries = () => [
  { id: sub(1), revision: 2, state: "accepted", text: "这些 worker 都报 429 了，帮我看看？", imageCount: 1, order: 1 },
  { id: sub(2), revision: 1, state: "queued", text: "a message queued while the lead fails", imageCount: 0, order: 2, nativePending: true },
  { id: sub(3), revision: 2, state: "failed", text: "a submission that failed now", imageCount: 0, order: 3, error: "rejected", agentSince: "1" },
];

const runError = { message: LIMIT, provider: "codex", model: "gpt-5.1-codex", at: new Date(T0 + 400_000).toISOString() };
const agents = () => [
  { id: "alice/lead", yesman: "alice", name: "lead", role: "lead", since: "1", status: "idle", runError, model: "m", provider: "p", effort: "high", effortLevels: ["high"] },
  { id: "alice/w1", yesman: "alice", name: "w1", role: "worker", number: 1, title: "Ready, never failed", since: "1", status: "idle" },
  { id: "alice/w2", yesman: "alice", name: "w2", role: "worker", number: 2, title: "Working again after an error", since: "1", status: "working", runError, runStartedAt: new Date().toISOString() },
  { id: "alice/w3", yesman: "alice", name: "w3", role: "worker", number: 3, title: "Finished on an error", since: "1", status: "stopped", runError, finishedAt: new Date(T0).toISOString() },
  { id: "alice/w4", yesman: "alice", name: "w4", role: "worker", number: 4, title: "Asking its lead", since: "1", status: "waiting" },
  { id: "alice/w5", yesman: "alice", name: "w5", role: "worker", number: 5, title: "Done", since: "1", status: "stopped", finishedAt: new Date(T0).toISOString() },
  { id: "bob/lead", yesman: "bob", name: "lead", role: "lead", since: "1", status: "waiting", model: "m", provider: "p" },
  { id: "carol/lead", yesman: "carol", name: "lead", role: "lead", since: "1", status: "stopped", model: "m", provider: "p" },
];

async function open(browser, server, { colorScheme = "light", width = 1000, height = 900, locale = "en", reducedMotion = "no-preference", history = quotaHistory(), receipts = deliveries() } = {}) {
  const page = await browser.newPage({ viewport: { width, height }, colorScheme, reducedMotion });
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.addInitScript(({ history, deliveries, agents, locale }) => {
    localStorage.setItem("yesman.info", "hidden");
    const saved = JSON.parse(sessionStorage.getItem("fixture") ?? "null") ?? { seq: 0, messages: { "alice/lead": history }, deliveries: { "alice/lead": deliveries }, agents };
    window.events = {};
    window.copied = [];
    const save = () => sessionStorage.setItem("fixture", JSON.stringify(saved));
    window.fixture = {
      saved,
      emit(agent, data) {
        if (data.type === "message_end") (saved.messages[agent] ??= []).push(data.message);
        save();
        window.events["agent-event"]?.({ agent, seq: ++saved.seq, data });
      },
      agent(id, patch) {
        const a = saved.agents.find((x) => x.id === id);
        for (const [k, v] of Object.entries(patch)) v === undefined ? delete a[k] : (a[k] = v);
        save();
        window.events.agents?.(structuredClone(saved.agents));
      },
    };
    window.mygo = {
      locale,
      on: (n, f) => (window.events[n] = f),
      call: async (method, ...args) => {
        switch (method.replace("Yesman.", "")) {
          case "State": return { yesmen: [{ name: "alice", displayName: "yesman" }, { name: "bob", displayName: "bob" }, { name: "carol", displayName: "carol" }], agents: structuredClone(saved.agents) };
          case "Repos": case "Clones": return [];
          case "Messages": return structuredClone({ messages: saved.messages[args[0]] ?? [], live: [], seq: saved.seq, deliveries: saved.deliveries[args[0]] ?? [], queue: { known: false } });
          case "Models": return { agent: saved.agents.find((a) => a.id === args[0]), models: [{ id: "m", provider: "p", name: "Model" }] };
          case "CopyValue": window.copied.push(args[0]); return "";
          case "TakeFocus": return "";
          default: return {};
        }
      },
    };
  }, { history, deliveries: receipts, agents: agents(), locale });
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.locator('[data-agent="alice/lead"]').first().click();
  await page.locator("#thread .msg-user, #thread .work").first().waitFor();
  await page.waitForTimeout(150);
  return { page, errors };
}

const shot = (page, name) => page.screenshot({ path: `${out}/error-flow-${name}.png`, fullPage: false });
const thread = (page) =>
  page.evaluate(() => {
    const t = document.querySelector("#thread");
    const top = (sel) => t.querySelectorAll(`:scope > ${sel}`).length;
    return {
      failures: top(".failures"), errorNotes: top(".note--error"), errorWork: top('.work[data-state="error"]'),
      people: top(".msg-user") + top(".msg-delivery[data-bound]"), images: t.querySelectorAll(".message-images img").length,
      replies: top(".msg-assistant"), receipts: top(".msg-delivery:not([data-bound])"),
    };
  });
const sidebar = (page) =>
  page.evaluate(() => [...document.querySelectorAll("#yesmen .agent-row")].map((el) => ({
    id: el.dataset.agent, entry: el.dataset.sidebarEntry, status: el.dataset.status,
    red: !!el.querySelector(".status-error-mark"), label: el.querySelector(".dot-wrap")?.getAttribute("aria-label") ?? null,
    color: getComputedStyle(el.querySelector(".dot-wrap > *")).color, title: el.title,
  })));

const results = {};
const engines = { chromium, webkit };
for (const [engine, type] of Object.entries(engines)) {
  const browser = await type.launch();
  try {
    if (before) {
      for (const locale of ["en", "zh-Hans"]) {
        const { page } = await open(browser, before, { locale });
        await page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
        results[`${engine}-before-${locale}`] = { thread: await thread(page), lead: (await sidebar(page)).find((r) => r.entry === "yesman" && r.id === "alice/lead") };
        await shot(page, `before-${locale}-${engine}`);
        await page.close();
      }
    }

    const { page, errors } = await open(browser, after);
    // One closed row for the seven runs; the person, image, reply and receipts stay.
    let shape = await thread(page);
    assert.deepEqual(shape, { failures: 1, errorNotes: 0, errorWork: 0, people: 1, images: 1, replies: 1, receipts: 2 }, JSON.stringify(shape));
    const row = page.locator("#thread > .failures");
    assert.equal(await row.evaluate((el) => el.open), false);
    const summary = (await row.locator(":scope > summary").innerText()).replace(/\s+/g, " ").trim();
    assert.equal(summary, `Ended with an error 7 times ${LIMIT}`);
    // It is history, not an alert: nothing around it is a live region.
    assert.equal(await row.evaluate((el) => !!el.closest('[aria-live], [role="alert"], [role="status"]') || !!el.querySelector('[aria-live], [role="alert"], [role="status"]')), false);
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(page, `after-en-${engine}`);

    // Opened by keyboard: the error in full, once, with each run and its work.
    await row.locator(":scope > summary").focus();
    await page.keyboard.press("Enter");
    await row.locator(".failure").first().waitFor();
    const opened = await row.evaluate((el) => ({
      errors: el.querySelectorAll(".failure").length,
      count: el.querySelector(".failure-count").textContent,
      source: el.querySelector(".failure-source").textContent,
      text: el.querySelector(".failure-text").textContent,
      runs: [...el.querySelectorAll(".failure-run")].map((r) => [r.querySelector("time").textContent, r.querySelector(".work-label").textContent, r.querySelector(".work-facts").textContent]),
    }));
    assert.equal(opened.errors, 1);
    assert.equal(opened.count, "7 times");
    assert.equal(opened.source, "codex · gpt-5.1-codex");
    assert.equal(opened.text, LIMIT);
    assert.equal(opened.runs.length, 7);
    for (const [time, label, facts] of opened.runs) {
      assert(time.length > 0);
      assert.equal(label, "Ended with an error");
      assert.equal(facts, "5 thoughts · 2 tool calls · 1 worker report");
    }
    // A run's work opens in place; the raw error copies exactly.
    const firstRun = row.locator(".failure-run").first();
    await firstRun.locator(".work > summary").focus();
    await page.keyboard.press("Enter");
    await firstRun.locator(".work-report").waitFor();
    await row.locator(".failure-head .message-copy").focus();
    assert.equal(await row.locator(".failure-head .message-copy").getAttribute("aria-label"), "Copy error text");
    await page.keyboard.press("Enter");
    await page.waitForFunction(() => window.copied.length === 1);
    assert.deepEqual(await page.evaluate(() => window.copied), [LIMIT]);
    await shot(page, `after-open-en-${engine}`);

    // An eighth run joins the same row, which stays open, in place, with focus.
    await row.evaluate((el) => (el.dataset.kept = "1"));
    await row.locator(":scope > summary").focus();
    const emit = (data) => page.evaluate((d) => window.fixture.emit("alice/lead", d), data);
    const T = T0 + 900_000;
    await emit({ type: "message_end", message: { role: "user", timestamp: T, yesman: { source: "yesman", kind: "report", worker: "w8" }, content: [{ type: "text", text: '[yesman] Worker "w8" finished a run. Its final message:\n\nquota' }] } });
    await emit({ type: "message_end", message: { role: "assistant", timestamp: T + 1, stopReason: "error", errorMessage: LIMIT, provider: "codex", model: "gpt-5.1-codex", content: [{ type: "thinking", thinking: "x" }] } });
    shape = await thread(page);
    assert.equal(shape.failures, 1);
    assert.equal(shape.errorNotes, 0);
    assert.equal(await page.locator("#thread > .failures[data-kept]").count(), 1, "the row was redrawn");
    assert.equal(await row.evaluate((el) => el.open), true);
    assert.equal(await page.evaluate(() => document.activeElement?.parentElement?.classList.contains("failures")), true, "focus left the row");
    assert.equal(await row.locator(".failure-count").innerText(), "8 times");
    assert.equal(await row.locator(":scope > summary .failures-label").innerText(), "Ended with an error 8 times");
    // Read again from history, the count is the same.
    await page.reload();
    await page.locator('[data-agent="alice/lead"]').first().click();
    await page.locator("#thread > .failures").waitFor();
    await page.waitForTimeout(100);
    assert.equal(await page.locator("#thread > .failures").count(), 1);
    assert.equal(await page.locator("#thread > .failures .failures-label").innerText(), "Ended with an error 8 times");

    // The sidebar: red only for a failure at rest, named, and the tooltip
    // keeps the error while a later run works.
    const initial = await sidebar(page);
    let side = Object.fromEntries(initial.filter((r) => r.entry !== "needs-you").map((r) => [r.id, r]));
    assert.equal(side["alice/lead"].status, "error");
    assert.equal(side["alice/lead"].red, true);
    assert.equal(side["alice/lead"].label, "Last run ended with an error");
    assert.match(side["alice/lead"].title, /Last run ended with an error[\s\S]*usage limit has been reached/);
    assert.deepEqual(["alice/w1", "alice/w2", "alice/w3", "alice/w4", "alice/w5", "bob/lead", "carol/lead"].map((id) => [id, side[id].status, side[id].red]), [
      ["alice/w1", "idle", false], ["alice/w2", "working", false], ["alice/w3", "error", true], ["alice/w4", "asking", false],
      ["alice/w5", "stopped", false], ["bob/lead", "waiting", false], ["carol/lead", "stopped", false],
    ]);
    assert.match(side["alice/w2"].title, /The last run ended with an error: server_error/);
    assert.notEqual(side["alice/lead"].color, side["alice/w1"].color, "red and green look the same");
    const sideShot = async (name) => page.locator("#yesmen").screenshot({ path: `${out}/error-flow-${name}.png` });
    await sideShot(`sidebar-after-en-${engine}`);
    // A new run is not a recovery: it works, the tooltip keeps the error.
    await page.evaluate(() => window.fixture.agent("alice/lead", { status: "working", runStartedAt: new Date().toISOString() }));
    side = Object.fromEntries((await sidebar(page)).map((r) => [r.id + r.entry, r]));
    assert.deepEqual([side["alice/leadyesman"].status, side["alice/leadyesman"].red], ["working", false]);
    assert.match(side["alice/leadyesman"].title, /The last run ended with an error/);
    // Asleep without recovering stays red; the run that ends otherwise clears it.
    await page.evaluate(() => window.fixture.agent("alice/lead", { status: "stopped", runStartedAt: undefined }));
    assert.equal((await sidebar(page)).find((r) => r.id === "alice/lead" && r.entry === "yesman").red, true);
    await page.evaluate(() => window.fixture.agent("alice/lead", { status: "idle", runError: undefined }));
    const recovered = (await sidebar(page)).find((r) => r.id === "alice/lead" && r.entry === "yesman");
    assert.deepEqual([recovered.status, recovered.red, recovered.label], ["idle", false, null]);
    assert(!/error/i.test(recovered.title));
    results[`${engine}-after-en`] = { opened: { ...opened, runs: opened.runs.length }, sidebar: initial.map(({ id, entry, status, red, label }) => ({ id, entry, status, red, label })) };
    assert.deepEqual(errors, []);
    await page.close();

    // Distinct errors, a person, a reply and part of a reply.
    const mixed = await open(browser, after, { history: mixedHistory(), receipts: [] });
    const rows = await mixed.page.evaluate(() => [...document.querySelectorAll("#thread > [data-row]")].map((el) =>
      el.classList.contains("failures") ? `failures:${el.querySelector(".failures-label").textContent}|${el.querySelector(".failures-facts").textContent}`
        : el.classList.contains("work") ? `work:${el.dataset.state}` : el.className.split(" ")[0]));
    assert.deepEqual(rows, [
      "failures:Ended with an error 3 times|2 different errors", "msg-user",
      `failures:Ended with an error 2 times|${LIMIT}`, "msg-assistant",
      'work:error', 'note',
      "work:done", "msg-assistant", "note",
      `failures:Ended with an error 2 times|${LIMIT}`,
    ]);
    const first = mixed.page.locator("#thread > .failures").first();
    await first.locator(":scope > summary").click();
    await first.locator(".failure").first().waitFor();
    const kinds = await first.evaluate((el) => [...el.querySelectorAll(".failure")].map((f) => [f.querySelector(".failure-count").textContent, f.querySelector(".failure-text").textContent, f.querySelector(".failure-text").children.length]));
    assert.deepEqual(kinds, [["2 times", LIMIT, 0], ["Once", RATE, 0]]);
    await mixed.page.locator("#scroll").evaluate((el) => (el.scrollTop = 0));
    await shot(mixed.page, `mixed-en-${engine}`);
    results[`${engine}-mixed`] = { rows, kinds };
    assert.deepEqual(mixed.errors, []);
    await mixed.page.close();

    // Chinese, dark, narrow, reduced motion: no overflow, named in Chinese.
    const zh = await open(browser, after, { locale: "zh-Hans", colorScheme: "dark", width: 560, height: 820, reducedMotion: "reduce" });
    const zrow = zh.page.locator("#thread > .failures");
    assert.equal(await zrow.locator(".failures-label").innerText(), "出错结束 7 次");
    await zrow.locator(":scope > summary").click();
    await zrow.locator(".failure").first().waitFor();
    assert.equal(await zrow.locator(".failure-count").innerText(), "7 次");
    assert.equal(await zrow.locator(".failures-caret").evaluate((el) => getComputedStyle(el).transitionDuration), "0s");
    const lead = (await sidebar(zh.page)).find((r) => r.id === "alice/lead" && r.entry === "yesman");
    assert.equal(lead.label, "上次运行出错");
    const overflow = await zh.page.evaluate(() => {
      const scroll = document.querySelector("#scroll");
      const edge = scroll.getBoundingClientRect().right;
      const wide = [...scroll.querySelectorAll("*")].filter((el) => el.getBoundingClientRect().right > edge + 0.5);
      return { scroll: scroll.scrollWidth - scroll.clientWidth, wide: wide.map((el) => `${el.tagName.toLowerCase()}.${el.className}`).slice(0, 8) };
    });
    assert.deepEqual(overflow, { scroll: 0, wide: [] });
    await zh.page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(zh.page, `after-zh-dark-narrow-${engine}`);
    await zrow.locator(":scope > summary").click();
    await shot(zh.page, `after-zh-dark-narrow-closed-${engine}`);
    results[`${engine}-zh`] = { label: "出错结束 7 次", sidebar: lead.label, overflow };
    assert.deepEqual(zh.errors, []);
    await zh.page.close();
    console.log(engine, "seven failed runs fold into one closed row/keyboard/copy/new run joins in place/reload/distinct errors/people, replies, partial reply and receipts kept/sidebar red only at rest/zh/dark/narrow/reduced motion PASS");
  } finally {
    await browser.close();
  }
}
await writeFile(`${out}/error-flow-results.json`, JSON.stringify(results, null, 2));
console.log(JSON.stringify(results));
console.log(`screenshots: ${out}/error-flow-*.png`);
after.close();
before?.close();
