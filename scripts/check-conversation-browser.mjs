// Optional browser check of how a conversation reads, in the production page
// against a controlled bridge, in Chromium and WebKit:
//
//   YESMAN_TEST_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node scripts/check-conversation-browser.mjs
//
// The history is synthetic, shaped like a lead's real one: a person's question
// with an image, replies, thinking, tools, a long worker report, receipts. It
// checks the work between replies folds into one line each time, people and
// replies stay in full, and live streaming, steering, keyboard, scrolling,
// switching agents, reloading, Chinese, dark and narrow windows behave. Set
// YESMAN_CONVERSATION_BEFORE to another page's web directory (such as an
// earlier release's) to draw the same history there for comparison.
// A browser bridge is not the native window, pi or a real provider.
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || "playwright");
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import assert from "node:assert/strict";

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
const before = process.env.YESMAN_CONVERSATION_BEFORE ? serve(process.env.YESMAN_CONVERSATION_BEFORE) : null;
await Promise.all([after, before].filter(Boolean).map((s) => new Promise((r) => (s.listening ? r() : s.on("listening", r)))));

const png = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==";
const sub = (n) => n.toString(16).padStart(48, "0");
const MARK = "REPORT-BODY-MARKER";

function history() {
  let ts = 1_794_652_000_000;
  const at = () => (ts += 1000);
  const user = (text, yesman, images = 0) => ({ role: "user", timestamp: at(), ...(yesman ? { yesman } : {}), content: [{ type: "text", text }, ...Array.from({ length: images }, () => ({ type: "image", mimeType: "image/png", data: png }))] });
  const assistant = (content, stopReason = "stop") => ({ role: "assistant", timestamp: at(), stopReason, content, api: "x", provider: "p", model: "m", usage: {} });
  const th = (thinking) => ({ type: "thinking", thinking, thinkingSignature: "opaque" });
  const tx = (text) => ({ type: "text", text });
  const call = (id, name, args) => ({ type: "toolCall", id, name, arguments: args });
  const result = (id, name, text = "ok") => ({ role: "toolResult", timestamp: at(), toolCallId: id, toolName: name, isError: false, content: [{ type: "text", text }] });
  const longReport = `已完成 t58。**主要原因**：每个非空 thinking 块单独成框。\n\n${MARK}\n\n` + Array.from({ length: 60 }, (_, i) => `- 证据 ${i + 1}：app.js 第 ${300 + i} 行`).join("\n");
  return [
    user("yesman 这里为什么会把 thinking 分成这么多次显示？", { source: "user", submission: sub(1) }, 1),
    assistant([tx("我会核对 thinking 的消息数据和渲染方式。"), call("c1", "yesman_board", { add: [{ title: "核实 Thinking" }] })], "toolUse"),
    result("c1", "yesman_board"),
    assistant([th("**Planning the audit**\n\nI need a worker to read the renderer."), th("**Choosing the crew**"), call("c2", "yesman_worker_start", { name: "thinking-display-audit", title: "核实 Thinking 显示" })], "toolUse"),
    result("c2", "yesman_worker_start"),
    assistant([th(""), tx("**多个 Thinking 框不一定对应多次模型调用。** 我已安排核实。")]),
    user(`[yesman] Worker "thinking-display-audit" finished a run. Its final message:\n\n${longReport}`, { source: "yesman", kind: "report", worker: "thinking-display-audit" }),
    assistant([th("**Reading the report**"), th("**Checking against the board**"), th("**Summarizing for the user**"), tx("原因查清了：**yesman 目前给每个独立的 `thinking` 内容块都画一个框。**")]),
    user("我希望把背后的工作合并起来，对话保留。", { source: "user", submission: sub(2) }),
    assistant([th("a"), th("b"), th("c"), th("d"), tx("可以这样组织。**你希望默认收起，还是只合并 Thinking？**")]),
    user('[yesman] Worker "legacy" finished a run. Its final message:\n\nAn old report from before origins were recorded.'),
  ];
}
const deliveries = () => [
  { id: sub(1), revision: 2, state: "accepted", text: "yesman 这里为什么会把 thinking 分成这么多次显示？", imageCount: 1, order: 1 },
  { id: sub(2), revision: 2, state: "unconfirmed", text: "我希望把背后的工作合并起来，对话保留。", imageCount: 0, order: 2 },
  { id: sub(3), revision: 3, state: "unconfirmed", text: "an older submission with no message tied to it", imageCount: 0, order: 3 },
  { id: sub(4), revision: 3, state: "failed", text: "a rejected submission", imageCount: 0, order: 4, error: "rejected" },
];
const workerHistory = () => [
  { role: "user", timestamp: 1_794_652_100_000, yesman: { source: "lead", kind: "task" }, content: [{ type: "text", text: "Read app.js and report why Thinking shows several boxes." }] },
  { role: "assistant", timestamp: 1_794_652_101_000, stopReason: "toolUse", content: [{ type: "thinking", thinking: "**Reading**" }, { type: "toolCall", id: "w1", name: "read", arguments: { path: "web/app.js" } }] },
  { role: "toolResult", timestamp: 1_794_652_102_000, toolCallId: "w1", toolName: "read", isError: false, content: [{ type: "text", text: "..." }] },
  { role: "assistant", timestamp: 1_794_652_103_000, stopReason: "stop", content: [{ type: "text", text: "Done: each thinking block gets its own box." }] },
];

async function open(browser, server, { colorScheme = "light", width = 900, height = 900, locale = "en" } = {}) {
  const page = await browser.newPage({ viewport: { width, height }, colorScheme });
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.addInitScript(({ history, deliveries, workerHistory, locale }) => {
    localStorage.setItem("yesman.info", "hidden");
    const saved = JSON.parse(sessionStorage.getItem("fixture") ?? "null") ?? {
      seq: 0,
      messages: { "alice/lead": history, "alice/w1": workerHistory },
      deliveries: { "alice/lead": deliveries },
    };
    const agents = [
      { id: "alice/lead", yesman: "alice", name: "lead", role: "lead", since: "1", status: "idle", model: "m", provider: "p", effort: "high", effortLevels: ["high"] },
      { id: "alice/w1", yesman: "alice", name: "w1", role: "worker", number: 1, title: "核实 Thinking 显示", since: "1", status: "idle", model: "m", provider: "p", effort: "high", effortLevels: ["high"] },
    ];
    window.events = {};
    const save = () => sessionStorage.setItem("fixture", JSON.stringify(saved));
    window.fixture = {
      saved,
      agents,
      emit(agent, data, extra = {}) {
        // pi's history holds what the agent's current instance recorded.
        if (data.type === "message_end" && !extra.agentSince) (saved.messages[agent] ??= []).push(data.message);
        if (data.type === "delivery_update") {
          const list = (saved.deliveries[agent] ??= []);
          const i = list.findIndex((d) => d.id === data.delivery.id);
          i < 0 ? list.push(data.delivery) : (list[i] = data.delivery);
          data.retained = list.map((d) => d.id);
        }
        save();
        window.events["agent-event"]?.({ agent, seq: ++saved.seq, data, ...extra });
      },
      status(name, status, activity = "") {
        for (const a of agents) if (a.name === name) Object.assign(a, { status, activity, runStartedAt: status === "working" ? new Date().toISOString() : undefined });
        window.events.agents?.(structuredClone(agents));
      },
    };
    window.mygo = {
      locale,
      on: (n, f) => (window.events[n] = f),
      call: async (method, ...args) => {
        switch (method.replace("Yesman.", "")) {
          case "State": return { yesmen: [{ name: "alice", displayName: "Alice" }], agents: structuredClone(agents) };
          case "Repos": case "Clones": return [];
          case "Messages": return structuredClone({ messages: saved.messages[args[0]] ?? [], live: [], seq: saved.seq, deliveries: saved.deliveries[args[0]] ?? [], queue: { known: false } });
          case "Models": return { agent: agents.find((a) => a.id === args[0]), models: [{ id: "m", provider: "p", name: "Model" }] };
          case "TakeFocus": return "";
          default: return {};
        }
      },
    };
  }, { history: history(), deliveries: deliveries(), workerHistory: workerHistory(), locale });
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.locator('[data-agent="alice/lead"]').first().click();
  await page.locator("#thread .msg-user").first().waitFor();
  await page.waitForTimeout(150);
  return { page, errors };
}

// geometry is what a reader sees: how tall the thread is, and what it shows.
const geometry = (page) =>
  page.evaluate(() => {
    const thread = document.querySelector("#thread");
    const shown = [...thread.children].filter((el) => el.getBoundingClientRect().height > 0);
    return {
      threadHeight: Math.round(thread.getBoundingClientRect().height),
      rows: shown.length,
      thinkingBoxes: thread.querySelectorAll("details.thinking").length,
      toolRows: [...thread.querySelectorAll(".tool")].filter((el) => el.getBoundingClientRect().height > 0).length,
      workRows: thread.querySelectorAll(":scope > .work").length,
      submissionRows: thread.querySelectorAll(".delivery-receipt").length,
    };
  });
const shot = (page, name, engine) => page.screenshot({ path: `${tmpdir()}/yesman-conversation-${name}-${engine}.png`, fullPage: false });
const summaries = async (page) => (await page.locator("#thread > .work > summary").allInnerTexts()).map((s) => s.trim().split(/\s*\n\s*/).join(" | "));

const results = {};
for (const [engine, type] of Object.entries({ chromium, webkit })) {
  const browser = await type.launch({ headless: true });
  try {
    if (before) {
      const { page } = await open(browser, before);
      results[`${engine}-before`] = await geometry(page);
      await page.locator("#scroll").evaluate((el) => (el.scrollTop = 0));
      await shot(page, "before", engine);
      await page.close();
    }
    const { page, errors } = await open(browser, after);
    const thread = page.locator("#thread");
    const g = await geometry(page);
    results[`${engine}-after`] = g;
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = 0));
    await shot(page, "after", engine);

    // Folded by default: one line for each stretch of work, nothing inside.
    assert.equal(g.workRows, 3);
    assert.equal(g.thinkingBoxes, 0);
    assert.equal(g.submissionRows, 0);
    assert.equal(await page.locator("#thread > .work[open]").count(), 0);
    assert.equal(await page.locator("#thread > .work .work-body > *").count(), 0);
    assert.deepEqual(await summaries(page), [
      "Done | 2 thoughts · 2 tool calls · 1 worker started",
      "Done | 3 thoughts · 1 worker report",
      "Done | 4 thoughts",
    ]);
    for (const h of await page.locator("#thread > .work > summary").evaluateAll((els) => els.map((el) => el.getBoundingClientRect().height))) assert(h <= 32, `a work row is ${h}px tall`);
    assert(!(await thread.innerText()).includes(MARK), "the report is shown before it is opened");
    // People and replies in full, in order; the question to the user shows.
    const order = await page.locator("#thread > *").evaluateAll((els) => els.map((el) => el.classList.contains("work") ? "work" : el.matches("[data-bound]") ? "user" : el.classList.contains("msg-assistant") ? "text" : el.className.split(" ")[0]));
    assert.deepEqual(order, ["user", "text", "work", "text", "work", "text", "user", "work", "text", "note", "msg-delivery", "msg-delivery"]);
    assert.match(await page.locator("[data-bound]").first().innerText(), /为什么会把 thinking 分成这么多次显示/);
    assert.equal(await page.locator("[data-bound]").first().locator(".message-images img").count(), 1);
    assert.deepEqual(await page.locator("[data-bound] .delivery-status").allInnerTexts(), ["Delivered", "Delivered"]);
    assert(await page.getByText("你希望默认收起，还是只合并 Thinking？").isVisible());
    // A message from before origins were recorded is shown as it was.
    assert.equal(await page.locator("#thread > details.note[open] > summary", { hasText: "Report from worker legacy" }).count(), 1);
    // Receipts without a message: a light line, or the message when it needs a look.
    assert.match(await page.locator(`[data-delivery="${sub(3)}"]`).innerText(), /Submission: an older submission[\s\S]*Delivery unconfirmed/);
    assert.equal(await page.locator(`[data-delivery="${sub(3)}"] .msg-user`).count(), 0);
    assert.match(await page.locator(`[data-delivery="${sub(4)}"]`).innerText(), /a rejected submission[\s\S]*Not sent/);

    // Keyboard: open a stretch of work and read it in order, focus kept.
    const report = page.locator("#thread > .work").nth(1);
    await report.locator("summary").first().focus();
    await page.keyboard.press("Enter");
    await page.waitForFunction(() => document.querySelectorAll("#thread > .work")[1].querySelector(".work-body > *"));
    assert.equal(await report.locator(".work-body > *").evaluateAll((els) => els.map((el) => el.className.split(" ")[0])).then((x) => x.join()), "work-report,work-thinking,work-thinking,work-thinking");
    assert.equal(await report.locator(".work-thinking strong").first().innerText(), "Reading the report");
    assert.equal(await report.locator("summary").first().evaluate((el) => document.activeElement === el), true);
    assert(!(await thread.innerText()).includes(MARK), "the report body opened with its work");
    await report.locator(".work-report > summary").click();
    await page.waitForFunction((mark) => document.querySelector("#thread").innerText.includes(mark), MARK);
    await shot(page, "opened", engine);
    await report.locator("summary").first().focus();
    await page.keyboard.press("Space");
    await page.waitForFunction(() => !document.querySelectorAll("#thread > .work")[1].open);
    assert.equal(await report.locator("summary").first().evaluate((el) => document.activeElement === el), true);
    await page.keyboard.press("Enter"); // leave it open for the checks below
    await page.waitForFunction(() => document.querySelectorAll("#thread > .work")[1].open);

    // Live: work streams into one line that says what is happening.
    const T = 1_794_653_000_000;
    const emit = (data, extra) => page.evaluate(([d, x]) => window.fixture.emit("alice/lead", d, x), [data, extra]);
    const update = (u, message = { role: "assistant", timestamp: T }) => emit({ type: "message_update", message, assistantMessageEvent: u });
    const frame = () => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = 0)); // reading earlier conversation
    await page.evaluate(() => window.fixture.status("lead", "working", "Thinking"));
    await emit({ type: "agent_start" });
    await emit({ type: "message_start", message: { role: "assistant", timestamp: T, content: [] } });
    await update({ type: "thinking_start", contentIndex: 0 });
    for (const piece of ["**Looking", " at the", " renderer**\n\nfirst"]) await update({ type: "thinking_delta", contentIndex: 0, delta: piece });
    await frame();
    const live = page.locator("#thread > .work").nth(3);
    assert.equal(await live.getAttribute("data-state"), "running");
    assert.equal(await live.locator(".work-label").innerText(), "Thinking…");
    assert.equal(await page.locator(".working-live").count(), 0, "the foot line repeats the running work");
    assert.equal(await page.locator("#scroll").evaluate((el) => el.scrollTop), 0, "streaming moved a reader");
    await live.locator(":scope > summary").focus();
    await page.keyboard.press("Enter");
    await page.waitForFunction(() => document.querySelectorAll("#thread > .work")[3]?.querySelector(".work-body > *"));
    await live.evaluate((el) => (el.dataset.kept = "1"));
    await update({ type: "thinking_end", contentIndex: 0, content: "**Looking at the renderer**\n\nfirst pass" });
    await update({ type: "toolcall_start", contentIndex: 1, id: "c9", toolName: "read" });
    await update({ type: "toolcall_end", contentIndex: 1, toolCall: { type: "toolCall", id: "c9", name: "read", arguments: { path: "web/app.js" } } });
    await emit({ type: "message_end", message: { role: "assistant", timestamp: T, stopReason: "toolUse", content: [{ type: "thinking", thinking: "**Looking at the renderer**\n\nfirst pass" }, { type: "toolCall", id: "c9", name: "read", arguments: { path: "web/app.js" } }] } });
    await page.evaluate(() => window.fixture.status("lead", "working", "Reading web/app.js"));
    await emit({ type: "tool_execution_start", toolCallId: "c9", toolName: "read", args: { path: "web/app.js" } });
    await emit({ type: "tool_execution_update", toolCallId: "c9", toolName: "read", args: { path: "web/app.js" }, partialResult: { content: [{ type: "text", text: "partial" }] } });
    await frame();
    assert.equal(await live.locator(".work-label").innerText(), "Reading web/app.js…");
    assert.equal(await live.getAttribute("data-kept"), "1", "the open work was redrawn");
    assert.equal(await live.evaluate((el) => el.open), true);
    assert.equal(await live.locator(":scope > summary").evaluate((el) => document.activeElement === el), true);
    assert.equal(await live.locator(".tool").getAttribute("data-status"), "running");
    // A message sent meanwhile shows as sent, then where pi took it.
    await emit({ type: "delivery_update", delivery: { id: sub(5), revision: 1, state: "steering", text: "also check the CSS", imageCount: 0, order: 5 } });
    assert.match(await page.locator(`[data-delivery="${sub(5)}"]`).innerText(), /also check the CSS[\s\S]*Accepted for steering/);
    await emit({ type: "tool_execution_end", toolCallId: "c9", toolName: "read", result: { content: [{ type: "text", text: "file" }] }, isError: false });
    await emit({ type: "message_end", message: { role: "toolResult", timestamp: T + 1, toolCallId: "c9", toolName: "read", isError: false, content: [{ type: "text", text: "file" }] } });
    await emit({ type: "message_end", message: { role: "user", timestamp: T + 2, yesman: { source: "user", submission: sub(5) }, content: [{ type: "text", text: "also check the CSS" }] } });
    assert.equal(await page.locator(`[data-delivery="${sub(5)}"]`).getAttribute("data-bound"), "true");
    assert.equal(await live.getAttribute("data-state"), "done", "work before a person's message is done, not running");
    assert.equal(await live.getAttribute("data-kept"), "1");
    // Late records of a replaced agent never reach this conversation.
    await emit({ type: "message_end", message: { role: "user", timestamp: T + 3, content: [{ type: "text", text: "from an old instance" }] } }, { agentSince: "0" });
    await emit({ type: "message_start", message: { role: "assistant", timestamp: T + 4, content: [] } });
    await update({ type: "thinking_start", contentIndex: 0 }, { role: "assistant", timestamp: T + 4 });
    await update({ type: "thinking_delta", contentIndex: 0, delta: "**Checking the CSS**" }, { role: "assistant", timestamp: T + 4 });
    await frame();
    assert.equal(await page.locator("#thread > .work").last().getAttribute("data-state"), "running");
    assert.equal(await page.locator("#thread > .work").count(), 5);
    await shot(page, "live", engine);
    // Reading at the bottom follows the reply.
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await page.evaluate(() => document.querySelector("#scroll").dispatchEvent(new Event("scroll")));
    await update({ type: "text_start", contentIndex: 1 }, { role: "assistant", timestamp: T + 4 });
    for (let i = 0; i < 30; i++) await update({ type: "text_delta", contentIndex: 1, delta: `line ${i}\n\n` }, { role: "assistant", timestamp: T + 4 });
    await frame();
    assert(await page.locator("#scroll").evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight < 80), "a reader at the bottom was left behind");
    assert.equal(await page.locator("#thread > .work").last().getAttribute("data-state"), "done");
    assert.equal(await page.locator(".working-live").count(), 1, "a reply being written still shows the agent working");
    const final = { role: "assistant", timestamp: T + 4, stopReason: "stop", content: [{ type: "thinking", thinking: "**Checking the CSS**" }, { type: "text", text: Array.from({ length: 30 }, (_, i) => `line ${i}`).join("\n\n") }] };
    await emit({ type: "message_end", message: final });
    await emit({ type: "agent_settled" });
    await page.evaluate(() => window.fixture.status("lead", "idle"));
    assert.equal(await page.locator(".working-live").count(), 0);
    assert.equal(await page.locator("#thread > .work[data-state=running]").count(), 0);
    assert.equal(await page.locator("#thread").getByText("from an old instance").count(), 0);
    const liveShape = await summaries(page);

    // Another agent's conversation is its own; coming back keeps what was open.
    await page.locator('[data-agent="alice/w1"]').click();
    await page.locator("#thread .note--lead").waitFor();
    assert.match(await page.locator("#thread > .note--lead").innerText(), /From the lead[\s\S]*Read app.js/);
    assert.deepEqual(await summaries(page), ["Done | 1 thought · 1 tool call"]);
    await emit({ type: "message_end", message: { role: "assistant", timestamp: T + 9, stopReason: "stop", content: [{ type: "text", text: "lead reply while hidden" }] } });
    assert.equal(await page.locator("#thread").getByText("lead reply while hidden").count(), 0);
    await page.locator('[data-agent="alice/lead"]').first().click();
    await page.locator("#thread > .work").first().waitFor();
    assert.equal(await page.locator("#thread > .work").nth(1).evaluate((el) => el.open), true);
    assert.equal(await page.locator("#thread").getByText("lead reply while hidden").count(), 1);

    // History reads the same as the live run did.
    await page.reload();
    await page.locator('[data-agent="alice/lead"]').first().click();
    await page.locator("#thread > .work").first().waitFor();
    await page.waitForTimeout(100);
    assert.deepEqual(await summaries(page), liveShape);
    assert.equal(await page.locator(`[data-delivery="${sub(5)}"]`).getAttribute("data-bound"), "true");

    // Chinese, then dark and narrow.
    await page.evaluate(() => window.events["mygo:locale"]({ locale: "zh-Hans" }));
    await page.waitForTimeout(50);
    assert.equal((await summaries(page))[0], "已完成 | 思考 2 次 · 工具调用 2 次 · 启动 1 个助手");
    assert.deepEqual(await page.locator("[data-bound] .delivery-status").allInnerTexts(), ["已送达", "已送达", "已送达"]);
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = 0));
    await shot(page, "zh", engine);
    assert.deepEqual(errors, []);
    await page.close();

    const dark = await open(browser, after, { colorScheme: "dark", width: 640, height: 800 });
    await dark.page.locator("#thread > .work").first().locator("summary").click();
    const overflow = await dark.page.evaluate(() => {
      const scroll = document.querySelector("#scroll");
      const edge = scroll.getBoundingClientRect().right;
      const wide = [...scroll.querySelectorAll("*")].filter((el) => el.getBoundingClientRect().right > edge + 0.5);
      return { scroll: scroll.scrollWidth - scroll.clientWidth, wide: wide.map((el) => `${el.tagName.toLowerCase()}.${el.className}`).slice(0, 8) };
    });
    assert.deepEqual(overflow, { scroll: 0, wide: [] });
    await dark.page.locator("#scroll").evaluate((el) => (el.scrollTop = 0));
    await shot(dark.page, "dark-narrow", engine);
    assert.deepEqual(dark.errors, []);
    console.log(engine, "folded work/people and replies in full/receipts/keyboard/live/steering/scroll/agents/reload/i18n/dark/narrow PASS");
  } finally {
    await browser.close();
  }
}
console.log(JSON.stringify(results));
console.log(`screenshots: ${tmpdir()}/yesman-conversation-*.png`);
after.close();
before?.close();
