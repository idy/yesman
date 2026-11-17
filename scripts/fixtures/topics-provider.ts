// Local, deterministic provider for topics_pi_test.go. No network or key.
// It records every model request (system prompt and messages) to
// TOPICS_AUDIT, holds runs while TOPICS_GATE is missing, and, when the last
// user message contains "FIXTURE-TOOL <json>", calls yesman_topics with
// that JSON once, so the lead's real tool and the app's API are exercised.
import { appendFileSync, existsSync } from "node:fs";
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";

const text = (m) => (typeof m.content === "string" ? m.content : (m.content ?? []).filter((b) => b.type === "text").map((b) => b.text).join("\n"));

export default function (pi) {
	pi.registerProvider("topics-test", {
		baseUrl: "http://127.0.0.1:1",
		apiKey: "fixture",
		api: "topics-test-api",
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
			appendFileSync(process.env.TOPICS_AUDIT, JSON.stringify({ systemPrompt: context.systemPrompt, messages: context.messages }) + "\n");
			const last = context.messages[context.messages.length - 1];
			const ask = last?.role === "user" ? /FIXTURE-TOOL (\{.*\})/s.exec(text(last)) : null;
			(async () => {
				const output = {
					role: "assistant",
					content: [],
					api: model.api,
					provider: model.provider,
					model: model.id,
					usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
					stopReason: "pending",
					timestamp: Date.now(),
				};
				stream.push({ type: "start", partial: output });
				while (!existsSync(process.env.TOPICS_GATE) && !options?.signal?.aborted) {
					await new Promise((resolve) => setTimeout(resolve, 5));
				}
				if (options?.signal?.aborted) {
					output.stopReason = "aborted";
					stream.push({ type: "error", reason: "aborted", error: output });
				} else if (ask) {
					output.content = [{ type: "toolCall", id: `call-${Date.now()}`, name: "yesman_topics", arguments: JSON.parse(ask[1]) }];
					output.stopReason = "toolUse";
					stream.push({ type: "done", reason: "toolUse", message: output });
				} else {
					output.content = [{ type: "text", text: last?.role === "toolResult" ? `tool said: ${text(last).split("\n")[0]}` : "fixture reply" }];
					output.stopReason = "stop";
					stream.push({ type: "done", reason: "stop", message: output });
				}
				stream.end();
			})();
			return stream;
		},
	});
}
