// Optional browser check of how receipts read beside a long conversation, in
// the production page against a controlled bridge, in Chromium and WebKit:
//
//   YESMAN_TEST_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node scripts/check-receipts-browser.mjs
//
// The history is synthetic, shaped like a lead's after an upgrade and a
// summary of earlier conversation: two dozen questions and answers whose
// messages carry no submission, the receipts the server still keeps for them,
// and messages tied to their receipts. It checks the earlier receipts are one
// folded line that opens to all of them while every message, image and reply
// stays as it was, and that what is sent now reads as the person's message
// with its state: sending, accepted, refused, uncertain and retried, two with
// the same text, images, a native message tied late, another agent, repeated
// records and snapshots, reloading, Chinese, dark and narrow windows, and the
// local icon font with outside requests blocked. Set YESMAN_RECEIPTS_WEB to
// another page's web directory (such as an earlier release's) to check it
// instead. A browser bridge is not the native window, pi or a real provider.
const { chromium, webkit } = await import(process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || "playwright");
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
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
const LEGACY = 24;
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
    ...Array.from({ length: LEGACY }, (_, i) => d(i + 1, "unconfirmed", i === 6 ? { preview: true } : {})),
    d(25, "unconfirmed", { text: XSS, imageCount: 0 }),
    d(26, "handled", { text: "/status", imageCount: 0 }),
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
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.addInitScript(({ history, deliveries }) => {
    localStorage.setItem("yesman.info", "hidden");
    const saved = JSON.parse(sessionStorage.getItem("fixture") ?? "null") ?? {
      seq: 0, order: 200,
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
            const d = { id, session: "s", revision: 1, state: "sending", text: text.slice(0, 512), imageCount: images.length, preview: text.length > 512, order: ++saved.order };
            emit(agent, { type: "delivery_update", delivery: d });
            return new Promise((resolve, reject) => (pending[id] = { resolve, reject }));
          }
          case "TakeFocus": return "";
          default: return {};
        }
      },
    };
  }, { history: history(), deliveries: deliveries() });
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.locator('[data-agent="alice/lead"]').first().click();
  await page.locator("#thread .msg-user").first().waitFor();
  await page.waitForTimeout(150);
  return { page, errors, outside, context };
}

// What a reader sees at the foot of the thread and in it: the receipts drawn
// on their own, the folded line, and every person's message and reply.
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
      people: people.map((el) => ({ text: el.firstChild?.nodeType === 3 ? el.firstChild.textContent : "", images: [...el.querySelectorAll("img")].map((i) => i.getAttribute("src").slice(-12)) })),
      replies: thread.querySelectorAll(":scope > .msg-assistant").length,
      nodes: thread.querySelectorAll("*").length,
    };
  });
const frame = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
const shot = (page, name, engine) => page.screenshot({ path: `${tmpdir()}/yesman-receipts-${name}-${engine}.png` });

const results = {};
const engines = process.env.YESMAN_TEST_ENGINES?.split(",") ?? ["chromium", "webkit"];
for (const [engine, type] of Object.entries({ chromium, webkit }).filter(([e]) => engines.includes(e))) {
  const browser = await type.launch({ headless: true });
  try {
    const { page, errors, outside, context } = await open(browser);
    const emit = (data, agent = "alice/lead") => page.evaluate(([a, d]) => window.fixture.emit(a, d), [agent, data]);
    const input = page.getByRole("textbox", { name: "Message", exact: true });
    const sentID = () => page.evaluate(() => window.sent.at(-1).id);
    const settled = () => page.waitForFunction(() => !document.querySelector(".composer textarea").readOnly);
    const row = (id) => page.locator(`#thread > [data-delivery="${id}"]`);
    const folded = page.locator("#thread > .receipts");

    // History: two dozen answered questions and the receipts kept for them.
    const first = await seen(page);
    results[`${engine}-history`] = { ...first, people: first.people.length };
    console.log(engine, "history", JSON.stringify(results[`${engine}-history`]));
    await page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(page, "history", engine);
    // Every message, image and reply as it was, in order.
    const expected = [...Array.from({ length: LEGACY }, (_, i) => ({ text: textOf(i + 1), images: Array.from({ length: imagesOf(i + 1) }, (_, k) => (k ? png1 : png).slice(-12)) })),
      { text: XSS, images: [] }, { text: "a question tied to its receipt", images: [png.slice(-12)] }, { text: "another tied one", images: [] }];
    assert.deepEqual(first.people, expected);
    assert.equal(first.replies, LEGACY + 3);
    assert.equal(await page.locator("#thread .msg-assistant strong", { hasText: "回答 7" }).count(), 1, "replies keep their Markdown");
    // The kept receipts: no line of their own, one folded line saying what they are.
    assert.equal(first.submissionLines, 0, `${first.submissionLines} Submission lines in the conversation`);
    assert.equal(first.ownReceipts, 0);
    assert.equal(first.folded, 1);
    assert.equal(first.foldedSummary, "26 earlier submissions | 25 delivery unconfirmed · 1 handled by an extension");
    assert.match(await folded.locator(":scope > summary").getAttribute("title"), /cannot tie to a message above/);
    assert.equal(await folded.evaluate((el) => el.open), false);
    assert.equal(await folded.locator(".receipts-body > *").count(), 0, "a folded line draws what it folds");
    for (const id of [sub(101), sub(102)]) assert.equal(await page.locator(`[data-delivery="${id}"][data-bound="true"] .delivery-status`).innerText(), "Taken by pi");
    // The local icon font, decorative; nothing fetched from outside.
    const caret = folded.locator(":scope > summary [data-icon=chevron]");
    assert.equal(await caret.getAttribute("aria-hidden"), "true");
    assert.match(await caret.evaluate((el) => getComputedStyle(el).fontFamily), /yesman Phosphor/);
    assert.equal(await page.evaluate(() => document.fonts.check('12px "yesman Phosphor"')), true);
    const box = await caret.boundingBox();
    assert(box.width <= 12.5 && box.height <= 12.5, `caret is ${box.width}x${box.height}`);

    // Opened by keyboard: every one of them, in order, with what was kept.
    await folded.locator(":scope > summary").focus();
    await page.keyboard.press("Enter");
    await page.waitForFunction(() => document.querySelector("#thread > .receipts")?.open && document.querySelector("#thread > .receipts .receipts-body > [data-delivery]"));
    const items = folded.locator(".receipts-body > [data-delivery]");
    assert.equal(await items.count(), 26);
    assert.deepEqual(await items.evaluateAll((els) => els.map((el) => el.dataset.delivery)), Array.from({ length: 26 }, (_, i) => sub(i + 1)));
    const lines = await items.allInnerTexts();
    assert.match(lines[0], /^Submission: 历史问题摘要 #1：为什么这里会有 Submission？\s*Delivery unconfirmed$/);
    assert.match(lines[5], /Submission: 历史问题摘要 #6[^\n]* · 1 image submitted\s*Delivery unconfirmed/);
    assert.match(lines[17], /^Submission: Attached images · 2 images submitted\s*Delivery unconfirmed$/);
    assert.match(lines[24], /Submission: <img src=x onerror="window.pwned=1"> \*\*not bold\*\*/);
    assert.match(lines[25], /Submission: \/status\s*Handled by an extension/);
    assert.equal(await items.nth(6).locator(".delivery-compact").getAttribute("title"), `${textOf(7)}…`, "a truncated preview says so");
    assert.equal(await folded.locator("img, strong").count(), 0, "a preview is text");
    assert.equal(await page.evaluate(() => window.pwned), undefined);
    assert.equal(await folded.locator(":scope > summary").evaluate((el) => document.activeElement === el), true);
    await shot(page, "opened", engine);

    // The same records and snapshots again and again add nothing.
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
    assert.equal(await items.count(), 26);
    assert.equal(await folded.locator(":scope > summary").evaluate((el) => document.activeElement === el), true, "repeated records moved focus");

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
    assert.equal(await row(pics).locator('.msg-user[data-kept="1"]').count(), 1, "the message was redrawn for a state");
    assert.equal(await row(pics).locator(".delivery-status").innerText(), "Accepted · delivery unconfirmed");
    assert.equal((await seen(page)).ownReceipts, 1);
    // pi's own message tied to it takes its place, once.
    await page.evaluate(([id, a, b]) => window.fixture.native("alice/lead", id, "看一下这两张图", [{ type: "image", data: a, mimeType: "image/png" }, { type: "image", data: b, mimeType: "image/png" }]), [pics, png, png1]);
    await frame(page);
    const now = await seen(page);
    assert.equal(now.ownReceipts, 0, "a receipt tied to a message keeps a row of its own");
    assert.deepEqual(now.people.filter((p) => p.text === "看一下这两张图"), [{ text: "看一下这两张图", images: [png.slice(-12), png1.slice(-12)] }]);
    assert.equal(await page.locator(`[data-delivery="${pics}"][data-bound="true"] .delivery-status`).innerText(), "Taken by pi");

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
    assert.notEqual(sameIDs[0], sameIDs[1]);
    for (const id of sameIDs) assert.match(await row(id).innerText(), /^same\s*Accepted for steering · delivery unconfirmed$/);
    await page.evaluate((id) => window.fixture.native("alice/lead", id, "same"), sameIDs[1]);
    await frame(page);
    assert.equal(await row(sameIDs[0]).count(), 1, "the other same-text receipt was taken for this message");
    assert.equal(await page.locator(`[data-delivery="${sameIDs[1]}"][data-bound="true"]`).count(), 1);
    await page.evaluate((id) => window.fixture.native("alice/lead", id, "same"), sameIDs[0]);
    await frame(page);
    assert.equal((await seen(page)).ownReceipts, 0);

    // The bridge fails before the server hears of it: uncertain, kept as sent;
    // retrying the unchanged draft is the same submission and the same row.
    await page.evaluate(() => (window.fixture.bridgeDown = true));
    await input.fill("did this arrive?");
    await input.press("Enter");
    await settled();
    const lost = await sentID();
    assert.equal(await row(lost).locator(".msg-user").innerText(), "did this arrive?");
    assert.equal(await row(lost).locator(".delivery-status").innerText(), "Delivery unconfirmed");
    assert.equal(await input.inputValue(), "did this arrive?");
    await input.press("Enter");
    await page.waitForFunction(() => window.sent.length === 5);
    assert.equal(await sentID(), lost);
    await row(lost).locator(".delivery-status", { hasText: "Sending" }).waitFor();
    assert.equal(await page.locator("#thread > .msg-delivery", { hasText: "did this arrive?" }).count(), 1);
    // ...its answer lost too: still the same row, uncertain, the draft kept.
    await page.evaluate((id) => window.fixture.lose(id), lost);
    await settled();
    assert.equal(await page.locator("#thread > .msg-delivery", { hasText: "did this arrive?" }).count(), 1);
    assert.equal(await row(lost).locator(".delivery-status").innerText(), "Delivery unconfirmed");
    assert.equal(await row(lost).locator(".delivery-status").getAttribute("title"), "the bridge lost its answer");
    assert.equal(await input.inputValue(), "did this arrive?");
    // Editing is a new intent. The current uncertain failure remains visible
    // even after the payload display cache lets go of it.
    await input.fill("did this arrive? (edited)");
    await input.press("Enter");
    await page.waitForFunction(() => window.sent.length === 6);
    const edited = await sentID();
    assert.notEqual(edited, lost);
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), edited);
    await settled();
    assert.match(await row(edited).innerText(), /^did this arrive\? \(edited\)\s*Accepted for steering/);
    assert.equal(await row(lost).count(), 1);
    assert.equal(await folded.locator(`[data-delivery="${lost}"]`).count(), 0);
    // One the server never heard of, superseded by an edited draft the server
    // answers for, folds too: it is no longer the latest.
    await page.evaluate(() => (window.fixture.bridgeDown = true));
    await input.fill("never heard of");
    await input.press("Enter");
    await settled();
    const unheard = await sentID();
    assert.match(await row(unheard).innerText(), /^never heard of\s*Delivery unconfirmed$/);
    let count = await page.evaluate(() => window.sent.length);
    await input.fill("never heard of (edited)");
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const reworded = await sentID();
    assert.notEqual(reworded, unheard);
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "steering"), reworded);
    await settled();
    assert.equal(await row(unheard).count(), 0, "a submission the server never heard of stays the latest");
    assert.match(await folded.locator(`[data-delivery="${unheard}"]`).innerText(), /Submission: never heard of\s*Delivery unconfirmed/);
    assert.deepEqual(await folded.locator(".receipts-body > [data-delivery]").evaluateAll((els) => els.slice(-1).map((el) => el.dataset.delivery)), [unheard]);
    assert.match(await row(reworded).innerText(), /^never heard of \(edited\)\s*Accepted for steering/);

    // A current refusal stays visible with its error after later submissions.
    await input.fill("this will be refused");
    count = await page.evaluate(() => window.sent.length);
    await input.press("Enter");
    await page.waitForFunction((n) => window.sent.length === n + 1, count);
    const refused = await sentID();
    await page.evaluate((id) => window.fixture.ack("alice/lead", id, "failed", { error: "pi refused the prompt" }), refused);
    await settled();
    assert.match(await row(refused).innerText(), /^this will be refused\s*Not sent$/);
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
    assert.equal(await row(alone).locator(".delivery-status").innerText(), "Accepted for follow-up · delivery unconfirmed");
    assert.equal(await row(refused).count(), 1, "a later submission hid the current refusal");
    assert.equal((await seen(page)).foldedSummary, "27 earlier submissions | 26 delivery unconfirmed · 1 handled by an extension");
    // The uncertain answer from the server, the latest now, is in view.
    await page.evaluate((id) => window.fixture.set("alice/lead", id, "unconfirmed", { error: "pi stopped before answering" }), alone);
    await frame(page);
    assert.match(await row(alone).innerText(), /Delivery unconfirmed/);
    const live = await seen(page);
    results[`${engine}-live`] = { ...live, people: live.people.length };

    // Another agent's conversation is its own; coming back keeps what was open.
    await page.locator('[data-agent="alice/w1"]').click();
    await page.locator("#thread .thread-empty").waitFor();
    assert.equal(await page.locator("#thread [data-delivery]").count(), 0);
    await input.fill("worker draft");
    await page.locator('[data-agent="alice/lead"]').first().click();
    await folded.waitFor();
    assert.equal(await folded.evaluate((el) => el.open), true);
    assert.equal(await input.inputValue(), "");
    assert.deepEqual(await seen(page), live);

    // History reads the same, without what only this window had: the folded
    // line closed again, the latest uncertain one in view from its preview.
    await page.reload();
    await page.locator('[data-agent="alice/lead"]').first().click();
    await folded.waitFor();
    await page.waitForTimeout(100);
    const reloaded = await seen(page);
    assert.deepEqual(reloaded.people, live.people);
    assert.equal(reloaded.replies, live.replies);
    // The one the server never heard of was only ever this window's.
    assert.equal(reloaded.foldedSummary, "28 earlier submissions | 26 delivery unconfirmed · 1 handled by an extension · 1 not sent");
    assert.equal(await folded.evaluate((el) => el.open), false);
    assert.equal(await row(alone).locator(".delivery-image-count").innerText(), "1 image submitted");
    assert.equal(await row(alone).locator("img").count(), 0, "an image was made up from a receipt");
    assert.match(await row(alone).innerText(), /Delivery unconfirmed/);
    // The steering one, still to come, from what its receipt keeps.
    assert.match(await row(edited).innerText(), /^did this arrive\? \(edited\)\s*Accepted for steering/);
    assert.match(await row(reworded).innerText(), /^never heard of \(edited\)\s*Accepted for steering/);
    assert.equal(reloaded.ownReceipts, 3);

    // Chinese.
    await page.evaluate(() => window.events["mygo:locale"]({ locale: "zh-Hans" }));
    await page.waitForTimeout(50);
    assert.equal((await seen(page)).foldedSummary, "较早的提交记录 · 28 条 | 26 条交付状态尚未确认 · 1 条已由扩展处理 · 1 条未发送");
    assert.deepEqual(errors, []);
    assert.deepEqual(outside, []);
    await context.close();

    // Dark and narrow, opened.
    const dark = await open(browser, { colorScheme: "dark", width: 480, height: 800 });
    await dark.page.locator("#thread > .receipts > summary").click();
    await dark.page.waitForFunction(() => document.querySelectorAll("#thread > .receipts [data-delivery]").length === 26);
    const overflow = await dark.page.evaluate(() => {
      const scroll = document.querySelector("#scroll");
      const edge = scroll.getBoundingClientRect().right;
      const wide = [...scroll.querySelectorAll("*")].filter((el) => el.getBoundingClientRect().right > edge + 0.5);
      return { scroll: scroll.scrollWidth - scroll.clientWidth, wide: wide.map((el) => `${el.tagName.toLowerCase()}.${el.className}`).slice(0, 8) };
    });
    assert.deepEqual(overflow, { scroll: 0, wide: [] });
    await dark.page.locator("#scroll").evaluate((el) => (el.scrollTop = el.scrollHeight));
    await shot(dark.page, "dark-narrow", engine);
    assert.deepEqual(dark.errors, []);
    await dark.context.close();
    console.log(engine, "earlier receipts folded/messages and replies whole/opened in order/repeats/sent as sent/states/same text/late native/retry/edit/refusal/images/agents/reload/i18n/dark/narrow/local icons PASS");
  } finally {
    await browser.close();
  }
}
console.log(JSON.stringify(results));
console.log(`screenshots: ${tmpdir()}/yesman-receipts-*.png`);
server.close();
