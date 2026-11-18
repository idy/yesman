// Local, deterministic provider for check-queue-append.mjs. No network or key.
// Each provider call N waits for `${APPEND_GATE}.N` (or `${APPEND_GATE}.all`),
// so a fixture can hold exactly one turn while it races queue operations.
import { appendFileSync, existsSync } from "node:fs";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

export default function (pi) {
  let calls = 0;
  pi.registerProvider("append-test", {
    baseUrl: "http://127.0.0.1:1",
    apiKey: "fixture",
    api: "append-test-api",
    models: [
      {
        id: "m",
        name: "Append fixture",
        reasoning: false,
        input: ["text", "image"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 100000,
        maxTokens: 1000,
      },
    ],
    streamSimple(model, context, options) {
      const call = ++calls;
      const stream = createAssistantMessageEventStream();
      appendFileSync(
        process.env.APPEND_AUDIT,
        JSON.stringify({ call, users: context.messages.filter((m) => m.role === "user") }) + "\n",
      );
      (async () => {
        const output = {
          role: "assistant",
          content: [{ type: "text", text: `fixture ${call}` }],
          api: model.api,
          provider: model.provider,
          model: model.id,
          usage: {
            input: 1,
            output: 1,
            cacheRead: 0,
            cacheWrite: 0,
            totalTokens: 2,
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
          },
          stopReason: "pending",
          timestamp: Date.now(),
        };
        stream.push({ type: "start", partial: output });
        const gate = process.env.APPEND_GATE;
        while (!existsSync(`${gate}.${call}`) && !existsSync(`${gate}.all`) && !options?.signal?.aborted) {
          await new Promise((resolve) => setTimeout(resolve, 5));
        }
        if (options?.signal?.aborted) {
          output.stopReason = "aborted";
          stream.push({ type: "error", reason: "aborted", error: output });
        } else {
          output.stopReason = "stop";
          stream.push({ type: "done", reason: "stop", message: output });
        }
        stream.end();
      })();
      return stream;
    },
  });
}
