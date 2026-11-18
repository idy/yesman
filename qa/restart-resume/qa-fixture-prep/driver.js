/* Append to the original S/Q index.html through YESMAN_WEB_DIR, as the existing
 * production upgrade runner does. No Go binary, embedded assets or origin
 * metadata is replaced. Endpoint is inserted by fixture.driver_text(). */
(() => {
  const endpoint = __ENDPOINT__;
  const page = crypto.randomUUID();
  const allowed = new Set([
    "HireNamed", "Send", "Submit", "Stop", "CheckForUpdates", "RestartToUpdate",
  ]);
  const post = (kind, value) => fetch(`${endpoint}/${kind}`, {
    method: "POST", body: JSON.stringify({ page, ...value }),
    headers: { "Content-Type": "application/json" },
  });
  async function call(method, args = []) {
    return window.mygo.call(`Yesman.${method}`, ...args);
  }
  async function observe() {
    // Do not use Messages to inspect paused agents: it starts pi.
    return { about: await call("About"), state: await call("State"),
      update: await call("Update"), restart: await call("RestartStatus") };
  }
  window.addEventListener("DOMContentLoaded", async () => {
    try {
      // The existing page must load its real bridge before our first observation.
      while (!window.mygo?.call) await new Promise(r => setTimeout(r, 20));
      await post("report", { observation: await observe() });
      while (true) {
        const c = await fetch(`${endpoint}/command?page=${page}`).then(r => r.json());
        if (c) {
          if (c.page !== page || !allowed.has(c.method) || !Array.isArray(c.args)) {
            throw Error("command is not bound to this page and allowed production method");
          }
          // Controller serves a command once. Never retry an unacknowledged
          // mutation on this or a new page, even after a rejected bridge call.
          await post("started", { id: c.id, method: c.method });
          const result = await call(c.method, c.args);
          await post("done", { id: c.id, result: result ?? null });
        }
        await post("report", { observation: await observe() });
        await new Promise(r => setTimeout(r, 200));
      }
    } catch (e) {
      await post("error", { error: String(e) }).catch(() => {});
      // A terminal error is evidence. No timeout reset, resend or reload.
    }
  });
})();
