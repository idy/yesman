// Optional browser check of what a person sees of their submissions beside a
// long conversation, in the production page against a controlled bridge, in
// Chromium and WebKit:
//
//   YESMAN_TEST_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node scripts/check-receipts-browser.mjs
//
// The history is synthetic, shaped like a lead's after an upgrade and a
// summary of earlier conversation: 42 retained receipts, 13 of them taken by pi
// and 27 never confirmed, beside questions and answers whose messages carry no
// submission, and messages tied to their receipts. The conversation is pi's
// messages: finished receipts draw no list, count, row or label, and every
// message, image and reply stays as it was. A submission shows at the foot,
// with its state and actions, only while it is open: sending or accepted,
// waiting in pi's queue (Send now, Cancel and edit), recalled with its
// original to return to the input, uncertain while it is the latest, or
// refused, interrupted or answered with an error until the person hides it or
// sends its draft again unchanged; whatever else is sent after it, a refusal
// that comes late, to a page reloaded since, stays. It also
// covers two with the same text, images, a native message tied late, taken
// ones leaving one by one while the reader's place, focus and draft stay, late
// lower revisions, another agent, the command palette, keys and Settings
// pages, repeated records and snapshots, reloading (the same rows as live),
// Chinese, dark and narrow windows, and the local icon font with outside
// requests blocked. Set YESMAN_RECEIPTS_WEB to another page's web directory
// (such as an earlier release's) to check it instead. A browser bridge is not
// the native window, pi or a real provider.
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || "playwright");
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { createServer } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
const out = process.env.YESMAN_RECEIPTS_OUT || tmpdir();
await mkdir(out, { recursive: true });
import assert from "node:assert/strict";

const root = process.env.YESMAN_RECEIPTS_WEB || fileURLToPath(new URL("../web", import.meta.url));
const server = createServer(async (req, res) => {
  try {
    const p = new URL(req.url, "http://localhost").pathname;
    const type = p.endsWith(".js") ? "application/javascript" : p.endsWith(".css") ? "text/css" : p.endsWith(".woff2") ? "font/woff2" : p.endsWith(".svg") ? "image/svg+xml" : "text/html";
    res.setHeader("Content-Type", type);
    res.end(await readFile(root + (p === "/" ? "/index.html" : p)));
  } catch {
    res.writeHead(404).end();
  }
}).listen(0, "127.0.0.1");
await new Promise((r) => (server.listening ? r() : server.on("listening", r)));

const png = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==";
const png1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";
const sub = (n) => n.toString(16).padStart(48, "0");
const LEGACY = 39;
const EARLIER = 42;
const XSS = '<img src=x onerror="window.pwned=1"> **not bold** & 历史问题';
const imagesOf = (n) => (n === 18 ? 2 : n % 6 === 0 ? 1 : 0);
const textOf = (n) => (n === 18 ? "" : `历史问题摘要 #${n}：为什么这里会有 Submission？`);

function history() {
  let ts = 1_794_660_000_000;
  const at = () => (ts += 1000);
  const image = (data) => ({ type: "image", data, mimeType: "image/png" });
  const user = (text, images, yesman) => ({ role: "user", timestamp: at(), ...(yesman ? { yesman } : {}), content: [...(text ? [{ type: "text", text }] : []), ...Array.from({ length: images }, (_, i) => image(i ? png1 : png))] });
  const reply = (text) => ({ role: "assistant", timestamp: at(), stopReason: "stop", api: "x", provider: "p", model: "m", usage: {}, content: [{ type: "text", text }] });
  const messages = [{ role: "compactionSummary", summary: "earlier", tokensBefore: 1, timestamp: at() }];
  for (let n = 1; n <= LEGACY; n++) messages.push(user(textOf(n), imagesOf(n)), reply(`**回答 ${n}**：这条已经回复过了。`));
  messages.push(user(XSS, 0), reply("an answer to the odd one"));
  messages.push(user("a question tied to its receipt", 1, { source: "user", submission: sub(101) }), reply("tied answer"));
  messages.push(user("another tied one", 0, { source: "user", submission: sub(102) }), reply("**最后的回答**"));
  return messages;
}
function deliveries() {
  const d = (n, state, extra = {}) => ({ id: sub(n), session: "s", revision: 3, state, text: textOf(n), imageCount: imagesOf(n), order: n, ...extra });
  return [
    ...Array.from({ length: LEGACY }, (_, i) => d(i + 1, i < 27 ? "unconfirmed" : "dispatched", i === 6 ? { preview: true } : {})),
    d(40, "cancelled", { text: XSS, imageCount: 0 }),
    d(41, "dispatched", { text: "/status", imageCount: 0 }),
    d(42, "cancelled", { text: "an earlier recalled input", imageCount: 0 }),
    d(101, "unconfirmed", { text: "a question tied to its receipt", imageCount: 1, order: 101 }),
    d(102, "accepted", { text: "another tied one", imageCount: 0, order: 102 }),
  ];
}

async function open(browser, { colorScheme = "light", width = 900, height = 900 } = {}) {
  const context = await browser.newContext({ viewport: { width, height }, colorScheme });
  const outside = [];
  await context.route("**/*", (route) => {
    const url = route.request().url();
    if (url.startsWith(`http://127.0.0.1:${server.address().port}/`) || url.startsWith("data:")) return route.continue();
    outside.push(url);
    return route.abort();
  });
  const page = await context.newPage();
  const errors = [], unsupported = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.exposeFunction("unsupportedCall", (method) => unsupported.push(method));
  await page.addInitScript(({ history, deliveries }) => {
    localStorage.setItem("yesman.info", "hidden");
    const saved = JSON.parse(sessionStorage.getItem("fixture") ?? "null") ?? {
      seq: 0, order: 200, payloads: {},
      messages: { "alice/lead": history, "alice/w1": [] },
      receipts: { "alice/lead": deliveries, "alice/w1": [] },
    };
    const agents = [
      { id: "alice/lead", yesman: "alice", name: "lead", role: "lead", since: "1", status: "idle", model: "m", provider: "p", effort: "high", effortLevels: ["high"] },
      { id: "alice/w1", yesman: "alice", name: "w1", role: "worker", number: 1, title: "worker", since: "1", status: "idle", model: "m", provider: "p", effort: "high", effortLevels: ["high"] },
    ];
    const pending = {};
    window.events = {};
    window.sent = [];
    window.payloadReads = [];
    window.promoted = [];
    const save = () => sessionStorage.setItem("fixture", JSON.stringify(saved));
    const emit = (agent, data) => {
      if (data.type === "message_end") (saved.messages[agent] ??= []).push(data.message);
      if (data.type === "delivery_update") {
        const list = (saved.receipts[agent] ??= []);
        const i = list.findIndex((d) => d.id === data.delivery.id);
        i < 0 ? list.push(data.delivery) : (list[i] = data.delivery);
        data.retained ??= list.map((d) => d.id);
      }
      save();
      window.events["agent-event"]?.({ agent, seq: ++saved.seq, data });
    };
    const receipt = (agent, id) => saved.receipts[agent].find((d) => d.id === id);
    window.fixture = {
      saved, agents, emit,
      // set gives a receipt its next state, as the server would.
      set(agent, id, state, extra = {}) {
        const d = { ...receipt(agent, id), state, ...extra };
        d.revision++;
        emit(agent, { type: "delivery_update", delivery: d });
        return d;
      },
      ack(agent, id, state, extra) { pending[id]?.resolve({ ...this.set(agent, id, state, extra) }); },
      // lose: the bridge loses the answer; the server, as the Manager does
      // when dispatch fails after binding, says it is uncertain.
      lose(id) {
        pending[id]?.reject(new Error("the bridge lost its answer"));
        this.set("alice/lead", id, "unconfirmed", { error: "the bridge lost its answer" });
      },
      // bridgeDown makes the next Submit fail before the server hears of it.
      bridgeDown: false,
      // native: pi's message for a submission, tied to it as yesman's
      // extension records it.
      native(agent, id, text, images = []) {
        emit(agent, { type: "message_end", message: { role: "user", timestamp: Date.now(), yesman: { source: "user", submission: id }, content: [...(text ? [{ type: "text", text }] : []), ...images] } });
      },
    };
    window.mygo = {
      locale: "en",
      // macOS, so ⌘ is the shortcuts' modifier whatever system runs the check.
      platform: "darwin",
      on: (n, f) => (window.events[n] = f),
      call: async (method, ...args) => {
        switch (method.replace("Yesman.", "")) {
          case "State": return { yesmen: [{ name: "alice", displayName: "Alice" }], agents: structuredClone(agents) };
          case "Repos": case "Clones": return [];
          case "Messages": return structuredClone({ messages: saved.messages[args[0]] ?? [], live: [], seq: saved.seq, deliveries: saved.receipts[args[0]] ?? [], queue: { known: false } });
          case "Models": return { agent: agents.find((a) => a.id === args[0]), models: [{ id: "m", provider: "p", name: "Model" }] };
          case "Submit": {
            const [agent, id, text, images] = args;
            window.sent.push({ agent, id, text, images: images.length });
            if (window.fixture.bridgeDown) {
              window.fixture.bridgeDown = false;
              throw new Error("the bridge is down");
            }
            const known = receipt(agent, id);
            if (known) return { ...known };
            saved.payloads[id] = { text, images };
            const d = { id, agentSince: "1", session: "s", revision: 1, state: "sending", text: text.slice(0, 512), imageCount: images.length, preview: text.length > 512, order: ++saved.order };
            emit(agent, { type: "delivery_update", delivery: d });
            return new Promise((resolve, reject) => (pending[id] = { resolve, reject }));
          }
          case "SubmissionPayload": {
            window.payloadReads.push(args[1]);
            if (!saved.payloads[args[1]]) throw new Error("original payload unavailable");
            return structuredClone(saved.payloads[args[1]]);
          }
          case "SubmissionText": return saved.payloads[args[1]]?.text ?? "";
          case "CancelSubmission": {
            const [agent, id] = args;
            return { delivery: window.fixture.set(agent, id, "cancelled", { nativePending: false, canCancel: false, canPromote: false, canRestore: true }), payload: structuredClone(saved.payloads[id]) };
          }
          case "AckRecall": {
            window.fixture.set(args[0], args[1], "cancelled", { canRestore: false, originalPayload: false, originalText: false });
            return {};
          }
          case "PromoteSubmission": {
            window.promoted.push(args[1]);
            return { delivery: window.fixture.set(args[0], args[1], "steering", { canPromote: false }) };
          }
          case "Settings": return { appearance: "system", locale: "en" };
          case "Context": return { available: false };
          case "NeedsYou": return { items: [] };
          case "Update": return {};
          case "TakeFocus": return "";
          case "CopyValue": window.copied = args[0]; return {};
          case "Shortcuts": return {};
          case "Grants": return { platform: "darwin", items: [] };
          case "ModelUsage": return { rows: [], coverage: {}, totals: {} };
          default: { await window.unsupportedCall(method); throw new Error(`Unsupported bridge call: ${method}`); }
        }
      },
    };
  }, { history: history(), deliveries: deliveries() });
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.locator('[data-agent="alice/lead"]').first().click();
  await page.locator("#thread .msg-user").first().waitFor();
  await page.waitForTimeout(150);
  return { page, errors, outside, context, unsupported };
}

// What a reader sees at the foot of the thread and in it: the receipts drawn
// on their own, any folded record list (there should be none), and every
// person's message and reply.
const seen = (page) =>
  page.evaluate(() => {
    const thread = document.querySelector("#thread");
    const own = [...thread.querySelectorAll(":scope > .msg-delivery:not([data-bound])")];
    const people = [...thread.querySelectorAll(":scope > .msg-user, :scope > [data-bound] > .msg-user")];
    return {
      threadHeight: Math.round(thread.getBoundingClientRect().height),
      ownReceipts: own.length,
      submissionLines: own.filter((el) => el.querySelector(".delivery-compact")).length,
      folded: thread.querySelectorAll(":scope > .receipts").length,
      foldedSummary: thread.querySelector(":scope > .receipts > summary")?.innerText.trim().split(/\s*\n\s*/).join(" | ") ?? null,
      people: people.map((el) => ({ text: el.firstChild?.nodeType === 3 ? el.firstChild.textContent : "", images: [...el.querySelectorAll("img")].map((i) => i.getAttribute("src")) })),
      replies: thread.querySelectorAll(":scope > .msg-assistant").length,
      nodes: thread.querySelectorAll("*").length,
    };
  });
const frame = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
const shot = (page, name, engine) => page.screenshot({ path: `${out}/${engine}-${name}.png` });

// The rows a reader sees for submissions not tied to a message, in order.
const ownIDs = (page) => page.evaluate(() => [...document.querySelectorAll("#thread > .msg-delivery:not([data-bound])")].map((el) => el.dataset.delivery));
// Words of the removed record list and labels, in either language.
const RECORDS = /Submission:|Earlier submission|Recorded as|Taken by pi|No confirmation recorded|较早的提交记录|提交记录：|当时记录|pi 已取走/;
// Where the reader is: the scroll offset and the first row in view, with its place.
const anchorOf = (page) => page.evaluate(() => {
  const scroll = document.querySelector("#scroll");
  const top = scroll.getBoundingClientRect().top;
  const first = [...document.querySelectorAll("#thread > [data-row]")].find((el) => el.getBoundingClientRect().bottom > top);
  return { scrollTop: Math.round(scroll.scrollTop), row: first?.dataset.row, offset: Math.round(first.getBoundingClientRect().top - top) };
});
const composerState = (page) => page.evaluate(() => {
  const el = document.querySelector(".composer textarea");
  return { focused: document.activeElement === el, value: el.value, selection: [el.selectionStart, el.selectionEnd] };
});

const results = {};
const engines = process.env.YESMAN_TEST_ENGINES?.split(",") ?? ["chromium", "webkit"];
for (const [engine, type] of Object.entries({ chromium, webkit }).filter(([e]) => engines.includes(e))) {
  const browser = await type.launch({ headless: true });
  try {
    const { page, errors, outside, context, unsupported } = await open(browser);
    const emit = (data, agent = "alice/lead") => page.evaluate(([a, d]) => window.fixture.emit(a, d), [agent, data]);
    const input = page.getByRole("textbox", { name: "Message", exact: true });
    const sentID = () => page.evaluate(() => window.sent.at(-1).id);
    const settled = () => page.waitForFunction(() => !document.querySelector(".composer textarea").readOnly);
    const row = (id) => page.locator(`#thread > [data-delivery="${id}"]`);

    // The reported situation: receipts kept for messages a summary replaced or
    // from before messages were tied to receipts, many of them taken by pi.
    assert.equal(deliveries().filter((d) => d.id < sub(100)).length, EARLIER);
    assert.equal(deliveries().filter((d) => d.id < sub(100) && d.state === "dispatched").length, 13);
    assert.equal(deliveries().filter((d) => d.id < sub(100) && d.state === "unconfirmed").length, 27);
    const first = await seen(page);
    results[`${engine}-history`] = { ...first, people: first.people.length };
    console.log(engine, "history", JSON.stringify(results[`${engine}-history`]));
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(page, "history", engine);
    await writeFile(`${out}/${engine}-history.json`, JSON.stringify(first, null, 2));
    // Every message, image and reply as it was, in order.
    const expected = [...Array.from({ length: LEGACY }, (_, i) => ({ text: textOf(i + 1), images: Array.from({ length: imagesOf(i + 1) }, (_, k) => `data:image/png;base64,${k ? png1 : png}`) })),
      { text: XSS, images: [] }, { text: "a question tied to its receipt", images: [`data:image/png;base64,${png}`] }, { text: "another tied one", images: [] }];
    assert.deepEqual(first.people, expected);
    assert.equal(first.replies, LEGACY + 3);
    assert.equal(await page.locator("#thread .msg-assistant strong", { hasText: "回答 7" }).count(), 1, "replies keep their Markdown");
    // The kept receipts: no list, no count, no row and no label of their own.
    assert.equal(first.folded, 0, "finished receipts are folded into a record list");
    assert.equal(first.foldedSummary, null);
    assert.equal(first.ownReceipts, 0, `${first.ownReceipts} finished receipts have rows`);
    assert.equal(first.submissionLines, 0);
    assert.doesNotMatch(await page.locator("#thread").innerText(), RECORDS);
    for (const id of [sub(101), sub(102)]) {
      assert.equal(await page.locator(`#thread > [data-delivery="${id}"][data-bound="true"]`).count(), 1);
      assert.equal(await page.locator(`[data-delivery="${id}"] .delivery-status`).count(), 0, "a message pi has carries a delivery label");
    }
    assert.equal(await page.evaluate(() => window.pwned), undefined);
    // The local icon font, decorative; nothing fetched from outside.
    const glyph = page.locator(`[data-delivery="${sub(102)}"] .message-copy [data-icon=copy]`);
    assert.equal(await glyph.getAttribute("aria-hidden"), "true");
    assert.match(await glyph.evaluate((el) => getComputedStyle(el).fontFamily), /yesman Phosphor/);
    assert.equal(await page.evaluate(() => document.fonts.check('12px "yesman Phosphor"')), true);

    // The same records and snapshots again and again add nothing, and leave the
    // reader's place, focus and draft as they were.
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = Math.round((el.scrollHeight - el.clientHeight) / 2)));
    await input.focus();
    await input.fill("a draft in progress");
    await input.evaluate((el) => el.setSelectionRange(2, 7));
    const place = await anchorOf(page);
    const before = await seen(page);
    const legacy = await page.evaluate((id) => window.fixture.saved.receipts["alice/lead"].find((d) => d.id === id), sub(5));
    for (let i = 0; i < 40; i++) {
      await emit({ type: "delivery_update", delivery: legacy });
      await emit({ type: "delivery_retained", retained: (await page.evaluate(() => window.fixture.saved.receipts["alice/lead"].map((d) => d.id))), removed: [] });
      await emit({ type: "queue_update", steering: [], followUp: [] });
      await emit({ type: "agent_settled" });
      await page.evaluate(() => window.events.agents?.(structuredClone(window.fixture.agents)));
    }
    await frame(page);
    assert.deepEqual(await seen(page), before);
    assert.deepEqual(await anchorOf(page), place, "repeated records moved the reader");
    assert.deepEqual(await composerState(page), { focused: true, value: "a draft in progress", selection: [2, 7] }, "repeated records moved focus or the draft");
    await input.fill("");

    // Sent now, with two images: the person's message as sent, its state under it.
    await input.click();
    await input.fill("看一下这两张图");
    await page.locator("input[type=file]").setInputFiles([
      { name: "a.png", mimeType: "image/png", buffer: Buffer.from(png, "base64") },
      { name: "b.png", mimeType: "image/png", buffer: Buffer.from(png1, "base64") },
    ]);
    await page.waitForFunction(() => document.querySelectorAll(".composer-image").length === 2);
    await input.press("Enter");
    await page.waitForFunction(() => window.sent.length === 1);
    const pics = await sentID();
    await row(pics).waitFor();
    assert.equal(await row(pics).locator(".msg-user").evaluate((el) => el.firstChild.textContent), "看一下这两张图");
    assert.deepEqual(await row(pics).locator("img").evaluateAll((els) => els.map((el) => el.getAttribute("src").slice(-12))), [png.slice(-12), png1.slice(-12)]);
    assert.equal(await row(pics).locator(".delivery-status").innerText(), "Sending…");
    assert.equal(await row(pics).locator(".delivery-image-count").count(), 0);
    await row(pics).locator(".msg-user").evaluate((el) => (el.dataset.kept = "1"));
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "accepted"), pics);
    await settled();
    await frame(page);
    assert.equal(await row(pics).locator('.msg-user[data-kept="1"]').count(), 1, "the message was redrawn for a state");
    assert.equal(await row(pics).locator(".delivery-status").innerText(), "Accepted · delivery unconfirmed");
    assert.equal((await seen(page)).ownReceipts, 1);
    // pi's own message tied to it takes its place, once, as any message.
    await page.evaluate(([id, a, b]) => window.fixture.native("alice/lead", id, "看一下这两张图", [{ type: "image", data: a, mimeType: "image/png" }, { type: "image", data: b, mimeType: "image/png" }]), [pics, png, png1]);
    await frame(page);
    const now = await seen(page);
    assert.equal(now.ownReceipts, 0, "a receipt tied to a message keeps a row of its own");
    assert.deepEqual(now.people.filter((p) => p.text === "看一下这两张图"), [{ text: "看一下这两张图", images: [`data:image/png;base64,${png}`, `data:image/png;base64,${png1}`] }]);
    assert.equal(await page.locator(`[data-delivery="${pics}"][data-bound="true"]`).count(), 1);
    assert.equal(await page.locator(`[data-delivery="${pics}"] .delivery-status`).count(), 0, "a message pi has carries a delivery label");

    // Two with the same text are two, each tied by its own ID, never by text.
    await page.evaluate(() => window.fixture.agents[0].status = "working");
    await page.evaluate(() => window.events.agents(structuredClone(window.fixture.agents)));
    const sameIDs = [];
    for (let i = 0; i < 2; i++) {
      await input.fill("same");
      await input.press("Enter");
      await page.waitForFunction((n) => window.sent.length === n, 2 + i);
      sameIDs.push(await sentID());
      await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), sameIDs[i]);
      await settled();
    }
    await frame(page);
    assert.notEqual(sameIDs[0], sameIDs[1]);
    for (const id of sameIDs) assert.match(await row(id).innerText(), /^same\s*Accepted for steering · delivery unconfirmed$/);
    await page.evaluate((id) => window.fixture.native("alice/lead", id, "same"), sameIDs[1]);
    await frame(page);
    assert.equal(await row(sameIDs[0]).count(), 1, "the other same-text receipt was taken for this message");
    assert.equal(await page.locator(`[data-delivery="${sameIDs[1]}"][data-bound="true"]`).count(), 1);
    await page.evaluate((id) => window.fixture.native("alice/lead", id, "same"), sameIDs[0]);
    await frame(page);
    assert.equal((await seen(page)).ownReceipts, 0);

    // One still to come when the next is sent, which pi then stops before
    // taking, leaves as it finishes: nothing keeps it as it was sent.
    let count = await page.evaluate(() => window.sent.length);
    await input.fill("first while working");
    await page.locator("input[type=file]").setInputFiles([{ name: "d.png", mimeType: "image/png", buffer: Buffer.from(png, "base64") }]);
    await page.waitForFunction(() => document.querySelectorAll(".composer-image").length === 1);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const early = await sentID();
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), early);
    await settled();
    await input.fill("second while working");
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 2, count);
    const later = await sentID();
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), later);
    await settled();
    await frame(page);
    assert.equal(await row(early).locator(".msg-user img").count(), 1);
    assert.match(await row(early).innerText(), /^first while working\s*Accepted for steering/);
    await page.evaluate((id) => window.fixture.set("alice/lead", id, "unconfirmed"), early);
    await frame(page);
    assert.equal(await page.locator(`#thread [data-delivery="${early}"]`).count(), 0, "a submission a later one followed kept a row once it finished");
    assert.match(await row(later).innerText(), /^second while working\s*Accepted for steering/);
    await page.evaluate((id) => window.fixture.native("alice/lead", id, "second while working"), later);
    await frame(page);
    assert.equal((await seen(page)).ownReceipts, 0);

    // The bridge fails before the server hears of it: uncertain, kept as sent;
    // retrying the unchanged draft is the same submission and the same row.
    await page.evaluate(() => (window.fixture.bridgeDown = true));
    await input.fill("did this arrive?");
    await input.press("Enter");
    await settled();
    await frame(page);
    const lost = await sentID();
    assert.equal(await row(lost).locator(".msg-user").innerText(), "did this arrive?");
    assert.equal(await row(lost).locator(".delivery-status").innerText(), "Delivery unconfirmed");
    assert.equal(await input.inputValue(), "did this arrive?");
    count = await page.evaluate(() => window.sent.length);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    assert.equal(await sentID(), lost);
    await row(lost).locator(".delivery-status", { hasText: "Sending" }).waitFor();
    assert.equal(await page.locator("#thread > .msg-delivery", { hasText: "did this arrive?" }).count(), 1);
    // ...its answer lost too: still the same row, uncertain, the draft kept.
    await page.evaluate((id) => window.fixture.lose(id), lost);
    await settled();
    await frame(page);
    assert.equal(await page.locator("#thread > .msg-delivery", { hasText: "did this arrive?" }).count(), 1);
    assert.equal(await row(lost).locator(".delivery-status").innerText(), "Delivery unconfirmed");
    assert.equal(await row(lost).locator(".delivery-status").getAttribute("title"), "the bridge lost its answer");
    assert.equal(await input.inputValue(), "did this arrive?");
    // Editing and sending is a new intent; it does not say the uncertain one
    // was dealt with, which stays; nothing resends it.
    await input.fill("did this arrive? (edited)");
    count = await page.evaluate(() => window.sent.length);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const edited = await sentID();
    assert.notEqual(edited, lost);
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), edited);
    await settled();
    await frame(page);
    assert.match(await row(edited).innerText(), /^did this arrive\? \(edited\)\s*Accepted for steering/);
    assert.match(await row(lost).innerText(), /^did this arrive\?\s*Delivery unconfirmed/, "an uncertain answer with an error left when the person sent another");
    // One the server never heard of, superseded by an edited draft the server
    // answers for, leaves too: it is no longer the latest.
    await page.evaluate(() => (window.fixture.bridgeDown = true));
    await input.fill("never heard of");
    await input.press("Enter");
    await settled();
    await frame(page);
    const unheard = await sentID();
    assert.match(await row(unheard).innerText(), /^never heard of\s*Delivery unconfirmed\s*Hide$/);
    count = await page.evaluate(() => window.sent.length);
    await input.fill("never heard of (edited)");
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const reworded = await sentID();
    assert.notEqual(reworded, unheard);
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), reworded);
    await settled();
    await frame(page);
    assert.equal(await row(unheard).count(), 0, "a submission the server never heard of stays the latest");
    assert.match(await row(reworded).innerText(), /^never heard of \(edited\)\s*Accepted for steering/);

    // A refusal stays in view, with its error and the draft, whatever is sent
    // after it.
    await input.fill("this will be refused");
    count = await page.evaluate(() => window.sent.length);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const refused = await sentID();
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "failed", { error: "pi refused the prompt" }), refused);
    await settled();
    await frame(page);
    assert.match(await row(refused).innerText(), /^this will be refused\s*Not sent\s*Hide$/);
    assert.equal(await row(refused).locator(".delivery-status").getAttribute("title"), "pi refused the prompt");
    assert.equal(await input.inputValue(), "this will be refused");
    // Images alone, sent now.
    await input.fill("");
    await page.locator("input[type=file]").setInputFiles([{ name: "c.png", mimeType: "image/png", buffer: Buffer.from(png, "base64") }]);
    await page.waitForFunction(() => document.querySelectorAll(".composer-image").length === 1);
    count = await page.evaluate(() => window.sent.length);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const alone = await sentID();
    assert.equal(await row(alone).locator(".msg-user img").count(), 1);
    assert.equal(await row(alone).locator(".msg-user").evaluate((el) => el.firstChild?.nodeType === 3 ? el.firstChild.textContent : ""), "");
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "queued"), alone);
    await settled();
    await frame(page);
    assert.equal(await row(alone).locator(".delivery-status").innerText(), "Accepted for follow-up · delivery unconfirmed");
    assert.match(await row(refused).innerText(), /^this will be refused\s*Not sent/, "a refusal left when the person sent another");
    // The uncertain answer from the server, the latest now, is in view.
    await page.evaluate((id) => window.fixture.set("alice/lead", id, "unconfirmed", { error: "pi stopped before answering" }), alone);
    await frame(page);
    assert.match(await row(alone).innerText(), /Delivery unconfirmed/);
    assert.equal(await row(alone).locator(".msg-user img").count(), 1, "the latest let go of what it was sent as");
    const live = await seen(page);
    const liveRows = await ownIDs(page);
    assert.deepEqual(liveRows, [lost, edited, reworded, refused, alone]);
    assert.equal(live.folded, 0);
    assert.doesNotMatch(await page.locator("#thread").innerText(), RECORDS);
    results[`${engine}-live`] = { ...live, people: live.people.length, rows: liveRows.length };
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(page, "live", engine);

    // Another agent's conversation is its own; coming back keeps what was there.
    await page.locator('[data-agent="alice/w1"]').click();
    await page.locator("#thread .thread-empty").waitFor();
    assert.equal(await page.locator("#thread [data-delivery]").count(), 0);
    await input.fill("worker draft");
    await page.locator('[data-agent="alice/lead"]').first().click();
    await row(alone).waitFor();
    assert.equal(await input.inputValue(), "");
    assert.deepEqual(await seen(page), live);

    // Keys, the command palette and Settings around it: focus comes back and
    // the conversation is as it was.
    const copy = row(edited).locator(".message-copy").first();
    const focused = (locator) => locator.evaluate((el) => document.activeElement === el);
    await copy.focus();
    await page.keyboard.press("Meta+KeyK");
    await page.locator("dialog.palette-dialog[open]").waitFor();
    await page.keyboard.press("Escape");
    await page.waitForFunction(() => !document.querySelector("dialog.palette-dialog[open]"));
    await frame(page);
    assert.equal(await focused(copy), true, "the palette gave focus back elsewhere");
    await page.keyboard.press("Meta+KeyL");
    assert.equal(await focused(input), true);
    // Settings › Grant and Settings › Model usage, then back by the sidebar.
    await page.keyboard.press("Meta+Comma");
    await page.locator('#settings-nav [data-page="grant"]').click();
    await page.locator(".grant-page").waitFor();
    await page.locator('#settings-nav [data-page="usage"]').click();
    await page.locator('#settings-nav [data-page="usage"][aria-current="page"]').waitFor();
    await page.waitForTimeout(50);
    await page.locator('[data-agent="alice/lead"]').first().click();
    await row(alone).waitFor();
    assert.deepEqual(await seen(page), live);
    // The next conversation and back by keys.
    await copy.focus();
    await page.keyboard.press("Meta+Alt+ArrowDown");
    await page.locator("#thread .thread-empty").waitFor();
    await page.keyboard.press("Meta+Alt+ArrowUp");
    await row(alone).waitFor();
    assert.deepEqual(await seen(page), live);
    assert.deepEqual(await ownIDs(page), liveRows);

    // History reads the same, without what only this window had: the same
    // rows as the live window, from their previews.
    await page.reload();
    await page.locator('[data-agent="alice/lead"]').first().click();
    await row(alone).waitFor();
    await page.waitForTimeout(100);
    const reloaded = await seen(page);
    assert.deepEqual(reloaded.people, live.people);
    assert.equal(reloaded.replies, live.replies);
    assert.equal(reloaded.folded, 0);
    assert.deepEqual(await ownIDs(page), liveRows, "a reloaded window shows other rows than the live one");
    assert.equal(await row(alone).locator(".delivery-image-count").innerText(), "1 image submitted");
    assert.equal(await row(alone).locator("img").count(), 0, "an image was made up from a receipt");
    assert.match(await row(alone).innerText(), /Delivery unconfirmed/);
    // The steering ones, still to come, from what their receipts keep.
    assert.match(await row(edited).innerText(), /^did this arrive\? \(edited\)\s*Accepted for steering/);
    assert.match(await row(reworded).innerText(), /^never heard of \(edited\)\s*Accepted for steering/);

    // The person hides a refusal: it leaves, focus moves on to the next one
    // to hide, and nothing is sent. Hidden ones stay hidden through a reload.
    const hide = (id) => row(id).getByRole("button", { name: "Hide", exact: true });
    count = await page.evaluate(() => window.sent.length);
    assert.equal(await row(edited).locator(".message-dismiss").count(), 0, "a submission still on its way can be hidden");
    await hide(refused).focus();
    await page.keyboard.press("Enter");
    await frame(page);
    assert.equal(await row(refused).count(), 0);
    assert.equal(await hide(alone).evaluate((el) => document.activeElement === el), true, "focus did not move on to the next one");
    await hide(lost).click();
    await frame(page);
    assert.deepEqual(await ownIDs(page), [edited, reworded, alone]);
    assert.equal(await page.evaluate(() => window.sent.length), count, "hiding sent something");
    await page.reload();
    await page.locator('[data-agent="alice/lead"]').first().click();
    await row(alone).waitFor();
    await page.waitForTimeout(100);
    assert.deepEqual(await ownIDs(page), [edited, reworded, alone]);
    // A refusal sent again unchanged, from the draft it left: once the server
    // has the new one, the refused one has been dealt with, live and reloaded.
    await input.fill("refused, then sent again as it was");
    count = await page.evaluate(() => window.sent.length);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const refusedOnce = await sentID();
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "failed", { error: "pi refused the prompt" }), refusedOnce);
    await settled();
    await frame(page);
    assert.equal(await input.inputValue(), "refused, then sent again as it was");
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 2, count);
    const again = await sentID();
    assert.notEqual(again, refusedOnce);
    assert.match(await row(refusedOnce).innerText(), /Not sent/, "the refusal left before the server had its retry");
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), again);
    await settled();
    await frame(page);
    assert.equal(await row(refusedOnce).count(), 0, "a refusal sent again unchanged stayed");
    assert.match(await row(again).innerText(), /^refused, then sent again as it was\s*Accepted for steering/);
    // A refusal that comes late, to a page reloaded since, after a newer one
    // was sent (the shape a real Manager gives when durable registration
    // fails): both stay, the earlier one's text copyable, live and reloaded.
    const late = sub(0x10a);
    await page.evaluate((id) => window.fixture.emit("alice/lead", { type: "delivery_update", delivery: { id, agentSince: "1", session: "", revision: 1, state: "sending", text: "late A original", imageCount: 0, order: ++window.fixture.saved.order } }), late);
    await frame(page);
    await input.fill("new B also fails");
    count = await page.evaluate(() => window.sent.length);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const newer = await sentID();
    await page.evaluate((id) => window.fixture.set("alice/lead", id, "failed", { error: "disk I/O failure" }), late);
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "failed", { error: "disk I/O failure" }), newer);
    await settled();
    await frame(page);
    for (let pass = 0; pass < 2; pass++) {
      assert.match(await row(late).innerText(), /^late A original\s*Not sent/);
      assert.match(await row(newer).innerText(), /^new B also fails\s*Not sent/);
      await row(late).locator(".message-copy").click();
      await page.waitForFunction(() => window.copied === "late A original");
      await page.evaluate(() => (window.copied = null));
      if (!pass) {
        await page.reload();
        await page.locator('[data-agent="alice/lead"]').first().click();
        await row(late).waitFor();
      }
    }
    // Hidden, it shows again if it is waiting again: hiding never hides what pi may still take.
    await hide(late).click();
    await frame(page);
    assert.equal(await row(late).count(), 0);
    await page.evaluate((id) => window.fixture.set("alice/lead", id, "queued", { nativePending: true }), late);
    await frame(page);
    assert.match(await row(late).innerText(), /^late A original\s*Waiting · follow-up/);
    assert.equal(await row(late).locator(".message-dismiss").count(), 0);
    await page.evaluate((id) => window.fixture.set("alice/lead", id, "dispatched", { nativePending: false }), late);
    await hide(newer).click();
    await frame(page);
    assert.deepEqual(await ownIDs(page), [edited, reworded, alone, again]);

    // Chinese.
    await page.evaluate(() => window.events["mygo:locale"]({ locale: "zh-Hans" }));
    await page.waitForTimeout(50);
    assert.equal(await row(alone).locator(".delivery-status").innerText(), "交付状态尚未确认");
    assert.equal(await row(alone).locator(".message-dismiss").innerText(), "隐藏");
    assert.doesNotMatch(await page.locator("#thread").innerText(), RECORDS);
    assert.deepEqual(errors, []);
    assert.deepEqual(outside, []);
    assert.deepEqual(unsupported, []);
    await context.close();

    // Dark and narrow, in both languages: nothing at the foot, nothing wider than the window.
    const dark = await open(browser, { colorScheme: "dark", width: 480, height: 800 });
    const overflow = (p) => p.evaluate(() => {
      const scroll = document.querySelector("#scroll");
      const edge = scroll.getBoundingClientRect().right;
      const wide = [...scroll.querySelectorAll("*")].filter((el) => el.getBoundingClientRect().right > edge + 0.5);
      return { scroll: scroll.scrollWidth - scroll.clientWidth, wide: wide.map((el) => `${el.tagName.toLowerCase()}.${el.className}`).slice(0, 8) };
    });
    assert.deepEqual(await overflow(dark.page), { scroll: 0, wide: [] });
    await dark.page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(dark.page, "dark-narrow", engine);
    await dark.page.evaluate(() => window.events["mygo:locale"]({ locale: "zh-Hans" }));
    await frame(dark.page);
    const zh = await seen(dark.page);
    assert.equal(zh.folded + zh.ownReceipts, 0);
    assert.doesNotMatch(await dark.page.locator("#thread").innerText(), RECORDS);
    await shot(dark.page, "zh-dark-narrow", engine);
    await writeFile(`${out}/${engine}-zh-dark-narrow.json`, JSON.stringify(zh, null, 2));
    assert.deepEqual(dark.errors, []);
    assert.deepEqual(dark.outside, []);
    assert.deepEqual(dark.unsupported, []);
    await dark.context.close();

    // Twelve real composer submissions wait in pi's queue, beyond the
    // eight-payload display cache, after one interrupted before a restart and
    // beside a receipt in a state this page does not know. Controlled DTOs
    // describe native membership and recoverable payloads.
    const mixed = await open(browser);
    const mp = mixed.page;
    const mi = mp.getByRole("textbox", { name: "Message", exact: true });
    const mr = (id) => mp.locator(`#thread > [data-delivery="${id}"]`);
    const canonical = await seen(mp);
    const interrupted = "e".repeat(48), unknown = "f".repeat(48);
    await mp.evaluate((id) => window.fixture.emit("alice/lead", { type: "delivery_update", delivery: { id, session: "s-before-restart", revision: 3, order: 150, state: "interrupted", text: "an interrupted follow-up", imageCount: 0 } }), interrupted);
    await frame(mp);
    assert.deepEqual(await ownIDs(mp), [interrupted]);
    assert.equal(await mr(interrupted).locator(".delivery-status").innerText(), "Delivery interrupted");
    assert.deepEqual(await mr(interrupted).locator("button:not(:disabled)").evaluateAll((els) => els.map((el) => el.getAttribute("aria-label"))), ["Copy message text", "Hide"], "an interrupted submission offers more than Copy and Hide");
    const ids = [], texts = [];
    const fullText = "  original current input\n" + "全文 ".repeat(200);
    for (let i = 0; i < 12; i++) {
      texts.push(i === 0 ? fullText : `current input ${i}`);
      await mi.fill(texts[i]);
      if (i === 0 || i === 3) {
        await mp.locator("input[type=file]").setInputFiles([
          { name: "current.png", mimeType: "image/png", buffer: Buffer.from(png, "base64") },
          { name: "second.png", mimeType: "image/png", buffer: Buffer.from(png1, "base64") },
        ]);
        await mp.waitForFunction(() => document.querySelectorAll(".composer-image").length === 2);
      }
      await mi.press("Enter");
      await mp.waitForFunction((n) => window.sent.length === n, i + 1);
      const id = await mp.evaluate(() => window.sent.at(-1).id);
      ids.push(id);
      await mp.evaluate((id) => window.fixture.ack("alice/lead", id, "queued", {
        nativePending: true, canCancel: true, canPromote: true, originalPayload: true, originalText: true,
      }), id);
      await mp.waitForFunction(() => !document.querySelector(".composer textarea").readOnly);
    }
    await mp.evaluate(([ids, unknown]) => {
      window.fixture.set("alice/lead", ids[3], "cancelled", { nativePending: false, canCancel: false, canPromote: false, canRestore: true });
      window.fixture.emit("alice/lead", { type: "delivery_update", delivery: { id: unknown, revision: 1, order: 199, state: "future-state", text: "unknown older receipt", preview: true, imageCount: 1 } });
    }, [ids, unknown]);
    await frame(mp);
    // A submission to the restarted session does not say the interruption was dealt with.
    assert.deepEqual(await ownIDs(mp), [interrupted, unknown, ...ids]);
    assert.equal(await mr(ids[3]).getByRole("button", { name: "Return recalled message to input", exact: true }).count(), 1);
    assert.equal(await mr(unknown).locator("img, button:not(:disabled)").count(), 0);
    for (const id of ids.filter((_, i) => i !== 3)) {
      assert.equal(await mr(id).locator(".delivery-status").innerText(), "Waiting · follow-up");
      assert.equal(await mr(id).getByRole("button", { name: "Send now", exact: true }).count(), 1);
      assert.equal(await mr(id).getByRole("button", { name: "Cancel and edit", exact: true }).count(), 1);
    }
    // Reloaded: the same rows, originals read back beyond the display cache.
    await mp.reload();
    await mp.locator('[data-agent="alice/lead"]').first().click();
    await mr(ids[0]).locator('[data-original], .msg-user img').first().waitFor();
    await mp.waitForFunction(() => document.querySelectorAll('#thread > [data-original="true"]').length === 12);
    const afterReload = await seen(mp);
    assert.deepEqual(await ownIDs(mp), [interrupted, unknown, ...ids]);
    assert.equal(afterReload.folded, 0);
    assert.deepEqual(afterReload.people, canonical.people);
    assert.equal(afterReload.replies, 42);
    assert.equal(await mr(ids[0]).locator(".msg-user").evaluate((el) => el.firstChild.textContent), fullText);
    assert.deepEqual(await mr(ids[0]).locator("img").evaluateAll((els) => els.map((el) => el.src)), [`data:image/png;base64,${png}`, `data:image/png;base64,${png1}`]);
    for (const id of [ids[0], ids[3]]) assert((await mp.evaluate(() => window.payloadReads)).includes(id), "originals outside the display cache were not read");
    await mr(ids[0]).getByRole("button", { name: "Send now", exact: true }).click();
    await frame(mp);
    assert.deepEqual(await mp.evaluate(() => window.promoted), [ids[0]]);
    assert.equal(await mr(ids[0]).locator(".delivery-status").innerText(), "Steering · waiting for input checkpoint");
    results[`${engine}-mixed-current`] = { ...afterReload, people: afterReload.people.length, rows: 14, payloadsRestored: 12, exactPromotion: ids[0] };
    await mp.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(mp, "mixed-current", engine);
    // Narrow, in Chinese.
    await mp.setViewportSize({ width: 480, height: 800 });
    await mp.evaluate(() => window.events["mygo:locale"]({ locale: "zh-Hans" }));
    await frame(mp);
    assert.equal(await mr(ids[1]).locator(".delivery-status").innerText(), "等待后续处理");
    assert.equal(await mr(ids[3]).getByRole("button", { name: "将已撤回消息放回输入框", exact: true }).count(), 1);
    assert.deepEqual(await overflow(mp), { scroll: 0, wide: [] });
    await mp.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(mp, "mixed-zh-narrow", engine);
    await mp.evaluate(() => window.events["mygo:locale"]({ locale: "en" }));
    await mp.setViewportSize({ width: 900, height: 900 });
    await frame(mp);

    // pi takes them one by one: half tied to pi's own message, half with none
    // in view (a summary replaced it, as in the reported case). Each row
    // leaves as it is taken, the rest stay, and the reader's place, focus and
    // draft stay as they were.
    await mp.locator("#scroll").evaluate((el) => (el.scrollTop = Math.round((el.scrollHeight - el.clientHeight) / 3)));
    await mi.focus();
    await mi.fill("a draft while they are taken");
    await mi.evaluate((el) => el.setSelectionRange(2, 7));
    const mplace = await anchorOf(mp);
    const queued = ids.filter((_, i) => i !== 3);
    for (const [n, id] of queued.entries()) {
      await mp.evaluate(([id, tie, text]) => {
        window.fixture.set("alice/lead", id, "dispatched", { nativePending: false, canCancel: false, canPromote: false, originalPayload: false, originalText: false });
        if (tie) window.fixture.native("alice/lead", id, text);
      }, [id, n % 2 === 0, texts[ids.indexOf(id)]]);
      await frame(mp);
      assert.deepEqual(await ownIDs(mp), [interrupted, unknown, ids[3], ...queued.slice(n + 1)].sort((a, b) => [interrupted, unknown, ...ids].indexOf(a) - [interrupted, unknown, ...ids].indexOf(b)), `after ${n + 1} taken`);
      assert.deepEqual(await anchorOf(mp), mplace, "a taken submission moved the reader");
    }
    assert.deepEqual(await composerState(mp), { focused: true, value: "a draft while they are taken", selection: [2, 7] });
    const tied = queued.filter((_, n) => n % 2 === 0);
    assert.equal(await mp.locator('#thread > [data-bound="true"]').evaluateAll((els, tied) => els.filter((el) => tied.includes(el.dataset.delivery)).length, tied), tied.length);
    assert.equal(await mp.locator('[data-bound="true"] .delivery-status, [data-bound="true"] .message-recall, [data-bound="true"] .message-promote').count(), 0);
    // A late answer with a lower revision brings none of them back.
    await mp.evaluate((id) => {
      const d = window.fixture.saved.receipts["alice/lead"].find((x) => x.id === id);
      window.events["agent-event"]({ agent: "alice/lead", seq: ++window.fixture.saved.seq, data: { type: "delivery_update", delivery: { ...d, revision: 2, state: "queued", nativePending: true, canCancel: true, canPromote: true } } });
    }, queued[1]);
    await frame(mp);
    assert.deepEqual(await ownIDs(mp), [interrupted, unknown, ids[3]]);
    // The recalled one goes back to the input with its text and images, then leaves.
    await mi.fill("");
    await mr(ids[3]).getByRole("button", { name: "Return recalled message to input", exact: true }).click();
    await mp.waitForFunction((text) => document.querySelector(".composer textarea").value === text, texts[3]);
    await mp.waitForFunction(() => document.querySelectorAll(".composer-image").length === 2);
    await frame(mp);
    assert.deepEqual(await ownIDs(mp), [interrupted, unknown]);
    // The person hides the interruption; a state this page does not know cannot be hidden.
    await mr(interrupted).getByRole("button", { name: "Hide", exact: true }).click();
    await frame(mp);
    assert.deepEqual(await ownIDs(mp), [unknown]);
    // Reloaded once more: the same rows; every message pi has is there.
    await mp.reload();
    await mp.locator('[data-agent="alice/lead"]').first().click();
    await mr(unknown).waitFor();
    await mp.waitForTimeout(100);
    assert.deepEqual(await ownIDs(mp), [unknown]);
    const end = await seen(mp);
    assert.equal(end.people.length, canonical.people.length + tied.length);
    assert.equal(end.folded, 0);
    assert.doesNotMatch(await mp.locator("#thread").innerText(), RECORDS);
    results[`${engine}-mixed-taken`] = { rows: (await ownIDs(mp)).length, tied: tied.length, untied: queued.length - tied.length };
    await mp.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(mp, "mixed-taken", engine);
    assert.deepEqual(mixed.errors, []);
    assert.deepEqual(mixed.unsupported, []);
    assert.deepEqual(mixed.outside, []);
    await mixed.context.close();
    console.log(engine, "finished receipts draw nothing/messages and replies whole/no labels/repeats keep place, focus and draft/sent as sent/states/same text/late native/retry/edit/refusal stays/hide and focus/unchanged resend/late refusal after reload/hidden but waiting again/images/agents/palette/keys/Settings/reload equals live/i18n/dark/narrow/local icons/cache8/queue actions/taken one by one/late revision/interruption until hidden/restore PASS");
  } finally {
    await browser.close();
  }
}
console.log(JSON.stringify(results));
await writeFile(`${out}/results.json`, JSON.stringify(results, null, 2));
console.log(`screenshots and JSON: ${out}`);
server.close();
