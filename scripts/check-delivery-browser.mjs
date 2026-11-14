// Optional browser regression check. Install Playwright and both browser engines,
// or set YESMAN_TEST_PLAYWRIGHT_MODULE to its absolute index.mjs path.
const { chromium, webkit } = await import(
  process.env.YESMAN_TEST_PLAYWRIGHT_MODULE || "playwright"
);
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import assert from "node:assert/strict";
const root = fileURLToPath(new URL("../web", import.meta.url));
const server = createServer(async (req, res) => {
  try {
    const p = new URL(req.url, "http://localhost").pathname;
    res.setHeader(
      "Content-Type",
      p.endsWith(".js")
        ? "application/javascript"
        : p.endsWith(".css")
          ? "text/css"
          : p.endsWith(".png")
            ? "image/png"
            : "text/html",
    );
    res.end(await readFile(root + (p === "/" ? "/index.html" : p)));
  } catch {
    res.writeHead(404).end();
  }
}).listen(0, "127.0.0.1");
for (const [name, engine] of Object.entries({ chromium, webkit })) {
  const browser = await engine.launch({ headless: true });
  try {
    const page = await browser.newPage({
      viewport: { width: 900, height: 700 },
    });
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.addInitScript(() => {
      localStorage.setItem("yesman.info", "hidden");
      const agents = ["lead", "worker"].map((name) => ({
        id: `alice/${name}`,
        yesman: "alice",
        name,
        role: name === "lead" ? "lead" : "worker",
        since: "1",
        status: "working",
        model: "m",
        provider: "p",
        effort: "high",
        effortLevels: ["high"],
      }));
      const saved = JSON.parse(sessionStorage.getItem("fixture") ?? "null") ?? {
        seq: 0,
        receipts: {},
        messages: {},
        queues: {},
        dispatches: 0,
      };
      const pending = {};
      window.events = {};
      window.sent = [];
      const save = () =>
        sessionStorage.setItem("fixture", JSON.stringify(saved));
      const emit = (agent, data) => {
        window.events["agent-event"]?.({ agent, seq: ++saved.seq, data });
        save();
      };
      const update = (agent, id, state) => {
        const d = saved.receipts[agent][id];
        d.state = state;
        d.revision++;
        emit(agent, { type: "delivery_update", delivery: { ...d } });
        return d;
      };
      window.fixture = {
        saved,
        emit,
        update,
        queue(agent, steering = [], followUp = []) {
          saved.queues[agent] = {
            known: true,
            steering: steering.length,
            followUp: followUp.length,
          };
          emit(agent, { type: "queue_update", steering, followUp });
        },
        ack(agent, id, state = "steering") {
          const d = update(agent, id, state);
          pending[id]?.resolve({ ...d });
        },
        reject(id) {
          pending[id]?.reject(new Error("network unavailable"));
        },
        consume(agent, id) {
          const d = saved.receipts[agent][id];
          const m = {
            role: "user",
            timestamp: 123,
            content: [{ type: "text", text: d.text }, ...(d.images ?? [])],
          };
          saved.messages[agent] ??= [];
          saved.messages[agent].push(m);
          emit(agent, { type: "message_start", message: m });
          emit(agent, { type: "message_end", message: m });
          save();
        },
      };
      window.mygo = {
        locale: "en",
        on: (n, f) => (window.events[n] = f),
        call: async (method, ...args) => {
          switch (method.replace("Yesman.", "")) {
            case "State":
              return { yesmen: [{ name: "alice" }], agents };
            case "Repos":
            case "Clones":
              return [];
            case "Messages":
              return structuredClone({
                messages: saved.messages[args[0]] ?? [],
                live: [],
                seq: saved.seq,
                deliveries: Object.values(saved.receipts[args[0]] ?? {}),
                queue: saved.queues[args[0]] ?? { known: false },
              });
            case "Models":
              return {
                agent: agents.find((a) => a.id === args[0]),
                models: [{ id: "m", provider: "p", name: "Model" }],
              };
            case "Submit": {
              window.sent.push({ method, args });
              const [agent, id, text, images] = args;
              saved.receipts[agent] ??= {};
              if (saved.receipts[agent][id])
                return { ...saved.receipts[agent][id] };
              saved.dispatches++;
              const d = {
                id,
                session: "fixture",
                revision: 1,
                state: "sending",
                text,
                images,
                order: saved.dispatches,
              };
              saved.receipts[agent][id] = d;
              emit(agent, { type: "delivery_update", delivery: { ...d } });
              if (text.startsWith("/"))
                return { ...update(agent, id, "handled") };
              return new Promise((resolve, reject) => {
                pending[id] = { resolve, reject };
              });
            }
            case "Abort":
              window.sent.push({ method, args });
              return;
            case "TakeFocus":
              return "";
            default:
              return {};
          }
        },
      };
    });
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    const input = page.getByRole("textbox", { name: "Message", exact: true });
    await input.waitFor();
    await page.waitForTimeout(100);
    const idAt = async (i) =>
      page.evaluate(
        (i) =>
          window.sent.filter((s) => s.method === "Yesman.Submit")[i].args[1],
        i,
      );
    const row = (id) => page.locator(`[data-delivery="${id}"]`);
    const ack = async (id, state = "steering") => {
      await page.evaluate(
        ({ id, state }) => window.fixture.ack("alice/lead", id, state),
        { id, state },
      );
      await page.waitForFunction(
        () => !document.querySelector(".composer textarea").readOnly,
      );
    };
    await input.fill("same");
    await input.press("Enter");
    await page.waitForFunction(() => window.sent.length === 1);
    const one = await idAt(0);
    assert.match(await row(one).innerText(), /Sending/);
    assert.equal(
      await input.evaluate((el) => document.activeElement === el),
      true,
    );
    await ack(one, "accepted");
    assert.match(await row(one).innerText(), /Accepted · delivery unconfirmed/);
    assert.equal(await row(one).locator("details").count(), 1);
    assert.equal(await input.inputValue(), "");
    await row(one).locator("summary").click();
    assert.equal(
      await row(one)
        .locator("details")
        .evaluate((el) => el.open),
      true,
    );
    assert.equal(
      await row(one).locator(".delivery-original .msg-user").innerText(),
      "same",
    );
    await page.evaluate(
      (id) => window.fixture.update("alice/lead", id, "steering"),
      one,
    );
    assert.equal(
      await row(one)
        .locator("details")
        .evaluate((el) => el.open),
      true,
    );
    await row(one).locator("summary").click();
    await input.fill("same");
    await input.press("Control+Enter");
    await page.waitForFunction(() => window.sent.length === 2);
    const two = await idAt(1);
    assert.notEqual(one, two);
    await ack(two);
    await page.locator("#settings-button").focus();
    await page.evaluate(() =>
      window.fixture.queue("alice/lead", ["same", "same"], ["later"]),
    );
    assert.equal(
      await page
        .locator("#settings-button")
        .evaluate((el) => document.activeElement === el),
      true,
    );
    assert.match(
      await page.locator(".pi-queue-status").innerText(),
      /2 steering · 1 follow-up/,
    );
    await page.evaluate((id) => {
      const d = window.fixture.saved.receipts["alice/lead"][id];
      window.fixture.emit("alice/lead", {
        type: "delivery_update",
        delivery: { ...d, revision: 1, state: "sending" },
      });
    }, one);
    assert.match(await row(one).innerText(), /Accepted for steering/);
    await page.evaluate((id) => window.fixture.consume("alice/lead", id), two);
    assert.equal(await page.locator("#thread > .msg-user").count(), 1);
    assert.equal(await row(two).count(), 1);
    assert.match(await row(two).innerText(), /delivery unconfirmed/);
    await page.evaluate((id) => window.fixture.consume("alice/lead", id), one);
    assert.equal(await page.locator("#thread > .msg-user").count(), 2); // identical text and timestamps remain two real messages
    await page.evaluate(() =>
      window.fixture.emit("alice/lead", {
        type: "delivery_update",
        delivery: {
          id: "f".repeat(48),
          revision: 3,
          state: "queued",
          text: "stale instance",
          agentSince: "old-instance",
        },
      }),
    );
    assert.equal(await page.locator(".msg-delivery").count(), 2);
    await page.reload();
    await input.waitFor();
    await page.waitForTimeout(100);
    assert.equal(await page.locator(".msg-delivery").count(), 2);
    assert.equal(await page.locator("#thread > .msg-user").count(), 2);
    assert.match(
      await page.locator(".pi-queue-status").innerText(),
      /2 steering/,
    );
    await page.locator('[data-agent="alice/worker"]').click();
    assert.equal(await page.locator(".msg-delivery").count(), 0);
    assert.equal(await page.locator(".pi-queue-status").count(), 0);
    await input.fill("worker draft");
    await page.locator('[data-agent="alice/lead"]').click();
    const png = Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg==",
      "base64",
    );
    await page.locator("input[type=file]").setInputFiles([
      { name: "one.png", mimeType: "image/png", buffer: png },
      { name: "two.png", mimeType: "image/png", buffer: png },
    ]);
    await page.waitForFunction(
      () => document.querySelectorAll(".composer-image").length === 2,
    );
    await input.press("Enter");
    await page.waitForFunction(
      () =>
        window.sent.filter((s) => s.method === "Yesman.Submit").length === 1,
    );
    const imageID = await idAt(0);
    assert.equal(await row(imageID).locator("img").count(), 2);
    await ack(imageID);
    assert.equal(await page.locator(".composer-image").count(), 0);
    await page.evaluate(
      (id) => window.fixture.consume("alice/lead", id),
      imageID,
    );
    assert.equal(
      await page.locator("#thread > .msg-user .message-images img").count(),
      2,
    );
    assert.match(await row(imageID).innerText(), /delivery unconfirmed/);
    await input.fill("retry");
    await input.press("Enter");
    await page.waitForFunction(
      () =>
        window.sent.filter((s) => s.method === "Yesman.Submit").length === 2,
    );
    const retryID = await idAt(1);
    await page.evaluate((id) => window.fixture.reject(id), retryID);
    await page.waitForFunction(
      () => !document.querySelector(".composer textarea").readOnly,
    );
    assert.equal(await input.inputValue(), "retry");
    const before = await page.evaluate(() => window.fixture.saved.dispatches);
    await input.press("Enter");
    await page.waitForFunction(
      () =>
        window.sent.filter((s) => s.method === "Yesman.Submit").length === 3,
    );
    await page.waitForFunction(
      () => !document.querySelector(".composer textarea").readOnly,
    );
    assert.equal(await idAt(2), retryID);
    assert.equal(
      await page.evaluate(() => window.fixture.saved.dispatches),
      before,
    );
    await page.evaluate(
      (id) => window.fixture.update("alice/lead", id, "interrupted"),
      retryID,
    );
    assert.match(await row(retryID).innerText(), /interrupted/);
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    assert.equal(
      (await page.evaluate(() => window.sent.at(-1))).method,
      "Yesman.Abort",
    );
    await input.fill("/example");
    await input.press("Enter");
    await page.waitForFunction(
      () => !document.querySelector(".composer textarea").readOnly,
    );
    assert.equal(
      (await page.evaluate(() => window.sent.at(-1))).args[2],
      "/example",
    );
    await input.fill("line");
    await input.press("Shift+Enter");
    assert.equal(await input.inputValue(), "line\n");
    // A definite pi rejection retains the editable draft and images. Retrying
    // that rejected submission gets a new ID; uncertain transport retries above
    // reuse the original ID and cannot dispatch a second prompt.
    await input.fill("known rejection");
    await page
      .locator("input[type=file]")
      .setInputFiles({ name: "retry.png", mimeType: "image/png", buffer: png });
    await page.waitForFunction(
      () => document.querySelectorAll(".composer-image").length === 1,
    );
    await input.press("Enter");
    await page.waitForFunction(
      () => document.querySelector(".composer textarea").readOnly,
    );
    const rejectedID = await page.evaluate(
      () =>
        window.sent.filter((s) => s.method === "Yesman.Submit").at(-1).args[1],
    );
    await ack(rejectedID, "failed");
    assert.equal(await input.inputValue(), "known rejection");
    assert.equal(await page.locator(".composer-image").count(), 1);
    assert.match(await row(rejectedID).innerText(), /Not sent/);
    await input.press("Enter");
    await page.waitForFunction(
      () => document.querySelector(".composer textarea").readOnly,
    );
    const newID = await page.evaluate(
      () =>
        window.sent.filter((s) => s.method === "Yesman.Submit").at(-1).args[1],
    );
    assert.notEqual(newID, rejectedID);
    await ack(newID);
    assert.equal(await input.inputValue(), "");
    assert.equal(await page.locator(".composer-image").count(), 0);
    await page.evaluate(() => {
      const thread = document.querySelector("#thread");
      for (let i = 0; i < 70; i++) {
        const p = document.createElement("p");
        p.textContent = "Older conversation " + i;
        thread.prepend(p);
      }
      document.querySelector("#scroll").scrollTop = 0;
    });
    await page.evaluate(
      (id) => window.fixture.update("alice/lead", id, "unconfirmed"),
      one,
    );
    assert((await page.locator("#scroll").evaluate((el) => el.scrollTop)) < 10);
    await page.evaluate(() => {
      window.fixture.saved.queues["alice/lead"] = { known: false };
      window.fixture.emit("alice/lead", {
        type: "delivery_queue",
        queue: { known: false },
      });
    });
    assert.equal(await page.locator(".pi-queue-status").count(), 0);
    await page.evaluate(() =>
      window.events["mygo:locale"]({ locale: "zh-Hans" }),
    );
    await page.waitForTimeout(30);
    assert.match(await row(one).innerText(), /交付状态尚未确认/);
    assert.match(await row(retryID).innerText(), /交付已中断/);
    await page.evaluate(() => window.events["mygo:locale"]({ locale: "en" }));
    await page.waitForTimeout(30);
    await page.setViewportSize({ width: 480, height: 700 });
    assert(
      await page
        .locator(".msg-delivery")
        .evaluateAll((els) =>
          els.every((el) => el.getBoundingClientRect().right <= innerWidth),
        ),
    );
    assert.equal(
      await page.getByRole("button", { name: "Send now", exact: true }).count(),
      0,
    );
    assert(
      await row(one)
        .locator(".delivery-status[role=status][aria-live=polite]")
        .count(),
    );
    await page.screenshot({
      path: `${tmpdir()}/yesman-message-delivery-transparent-${name}.png`,
    });
    await page.locator('[data-agent="alice/worker"]').click();
    assert.equal(await input.inputValue(), "worker draft");
    assert.deepEqual(errors, []);
    console.log(
      name,
      "transparent receipts/native transcript/queue counts/focus/refresh/retry/images/drafts/keyboard/scroll/narrow/i18n PASS",
    );
  } finally {
    await browser.close();
  }
}
server.close();
