// Local, deterministic provider for check-delivery-pi.mjs. No network or key.
import { appendFileSync, existsSync } from "node:fs";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

export default function (pi) {
  pi.on("input", (event) => {
    if (event.text === "fixture-ping") return { action: "handled" };
  });
  pi.registerProvider("delivery-test", {
    baseUrl: "http://127.0.0.1:1",
    apiKey: "fixture",
    api: "delivery-test-api",
    models: [
      {
        id: "m",
        name: "Fixture",
        reasoning: false,
        input: ["text", "image"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 100000,
        maxTokens: 1000,
      },
    ],
    streamSimple(model, context, options) {
      const stream = createAssistantMessageEventStream();
      appendFileSync(
        process.env.DELIVERY_AUDIT,
        JSON.stringify(context.messages.filter((m) => m.role === "user")) +
          "\n",
      );
      (async () => {
        const output = {
          role: "assistant",
          content: [{ type: "text", text: "fixture" }],
          api: model.api,
          provider: model.provider,
          model: model.id,
          usage: {
            input: 1,
            output: 1,
            cacheRead: 0,
            cacheWrite: 0,
            totalTokens: 2,
            cost: {
              input: 0,
              output: 0,
              cacheRead: 0,
              cacheWrite: 0,
              total: 0,
            },
          },
          stopReason: "pending",
          timestamp: Date.now(),
        };
        stream.push({ type: "start", partial: output });
        while (
          !existsSync(process.env.DELIVERY_GATE) &&
          !options?.signal?.aborted
        ) {
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
