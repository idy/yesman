// Deterministic project-context trial. Pi executes the actual named tools.
import { appendFileSync } from "node:fs";
import { createAssistantMessageEventStream, getCurrentSystemPrompt } from "@earendil-works/pi-ai";

const text = (m) => typeof m?.content === "string" ? m.content : (m?.content ?? []).filter(b => b.type === "text").map(b => b.text).join("\n");
const ref = (s) => s.match(/pc1\.[\w-]+/)?.[0];

export default function (pi) {
	let session;
	pi.on("session_start", (_event, ctx) => { session = ctx.sessionManager.getSessionId(); });
	pi.registerProvider("project-test", {
		baseUrl: "http://127.0.0.1:1", apiKey: "fixture", api: "project-test-api",
		models: [{ id: "m", name: "Fixture", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 100000, maxTokens: 1000 }],
		streamSimple(model, context) {
			const stream = createAssistantMessageEventStream();
			const role = process.env.YESMAN_ROLE;
			const last = context.messages.at(-1);
			const value = text(last);
			const prompt = context.systemPrompt ?? getCurrentSystemPrompt(context.messages);
			let outputText = "fixture reply";
			let call;
			if (last?.role === "user") {
				if (role === "lead" && value.includes("PROJECT-KICKOFF")) {
					call = ["context_search", { query: "精确定位", source: "knowledge" }];
				} else if (role === "worker" && value.includes("PROJECT-WORKER")) {
					call = ["context_read", { ref: ref(prompt) }];
				} else if (role === "lead" && value.includes("WORKER-VERIFIED") && ref(value)) {
					call = ["context_read", { ref: ref(value) }];
				} else if (value.includes("PROJECT-RESUME")) {
					call = ["context_read", { ref: ref(value) }];
				}
			} else if (last?.role === "toolResult") {
				if (last.toolName === "context_search" && role === "lead" && ref(value)) {
					call = ["yesman_worker_start", { name: "lookup", title: "Verify source", task: "PROJECT-WORKER", dispatch: { boardTask: "t1", goal: "Verify the source", constraints: "Keep the original", deliverables: "Return a source ref", sources: [ref(value)] } }];
				} else if (last.toolName === "context_read" && value.includes("KNOWN FIELD 精确定位")) {
					const source = ref(context.messages.map(text).reverse().find(s => ref(s)) ?? "") ?? ref(prompt);
					const resumed = text(context.messages.findLast(m => m.role === "user")).includes("PROJECT-RESUME");
					outputText = resumed ? "RESUMED-VERIFIED" : role === "worker" ? `WORKER-VERIFIED ${source}` : "LEAD-VERIFIED";
				}
			}
			appendFileSync(process.env.PROJECT_AUDIT, JSON.stringify({ role, session, prompt, last: value, call, output: outputText }) + "\n");
			const output = {
				role: "assistant", content: call ? [{ type: "toolCall", id: `call-${Date.now()}-${role}`, name: call[0], arguments: call[1] }] : [{ type: "text", text: outputText }],
				api: model.api, provider: model.provider, model: model.id,
				usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
				stopReason: call ? "toolUse" : "stop", timestamp: Date.now(),
			};
			stream.push({ type: "start", partial: output });
			stream.push({ type: "done", reason: output.stopReason, message: output });
			stream.end();
			return stream;
		},
	});
}
