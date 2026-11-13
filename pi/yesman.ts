// yesman's pi extension. yesman loads it into every agent it starts
// (`pi -e`), with the address of its local API in the environment.
//
// It gives the agent its yesman's context before every run (workspace,
// worktrees, the repo pool, shared knowledge, the other agents) and tools
// to check repositories out of the pool and, for a lead, to run workers.
// Its shell tool sets the user's secrets and variables in every command,
// asking the app for them each time, and what tools return hides the
// secrets' values from the model.
// Everything is done by the yesman app; this file only forwards.
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { createBashToolDefinition, createPowerShellToolDefinition } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const API = process.env.YESMAN_API;
const TOKEN = process.env.YESMAN_TOKEN;
const AGENT = process.env.YESMAN_AGENT;
const ROLE = process.env.YESMAN_ROLE;

async function call(method: "GET" | "POST", path: string, body?: unknown, signal?: AbortSignal): Promise<any> {
	const res = await fetch(API + path, {
		method,
		headers: {
			Authorization: `Bearer ${TOKEN}`,
			...(body === undefined ? {} : { "Content-Type": "application/json" }),
		},
		body: body === undefined ? undefined : JSON.stringify(body),
		signal,
	});
	const text = await res.text();
	if (!res.ok) {
		let message = text;
		try {
			message = JSON.parse(text).error ?? text;
		} catch {}
		throw new Error(message);
	}
	return (res.headers.get("content-type") ?? "").includes("application/json") ? JSON.parse(text) : text;
}

function result(text: string, details: unknown = {}) {
	return { content: [{ type: "text" as const, text }], details };
}

function json(value: unknown) {
	return JSON.stringify(value, null, 2);
}

// The secrets' values, and lines of them, as the app last gave them,
// longest first, which output shows as [secret NAME].
let hidden: { value: string; name: string }[] = [];

function hide(text: string): string {
	for (const h of hidden) {
		if (text.includes(h.value)) text = text.split(h.value).join(`[secret ${h.name}]`);
	}
	return text;
}

type Content = { type: string; text?: string }[];

function hideContent<T extends Content>(content: T): T {
	return content.map((c) => (c.type === "text" && typeof c.text === "string" ? { ...c, text: hide(c.text) } : c)) as T;
}

// hideJSON hides values in a JSON value, as JSON writes them.
function hideJSON(value: unknown): unknown {
	let text = JSON.stringify(value);
	for (const h of hidden) {
		const quoted = JSON.stringify(h.value).slice(1, -1);
		if (text.includes(quoted)) text = text.split(quoted).join(`[secret ${h.name}]`);
	}
	return JSON.parse(text);
}

// commandEnv asks the app for the variables to set in a command. Without
// an answer the command runs with none, and says so.
async function commandEnv(signal?: AbortSignal): Promise<{ env: Record<string, string>; note?: string }> {
	try {
		const r = await call("GET", "/v1/env", undefined, signal);
		// Locked, the app has no values to give: keep hiding those this
		// agent may have seen, as in a file a command wrote.
		hidden = r.locked && !r.hide?.length ? hidden : (r.hide ?? []);
		const note = r.errors?.length ? `yesman could not read some secrets or variables: ${r.errors.join("; ")}` : undefined;
		return { env: r.env ?? {}, note };
	} catch (err) {
		if (signal?.aborted) throw err;
		return { env: {}, note: `The yesman app did not answer (${(err as Error).message}), so this command ran without the secrets and variables.` };
	}
}

export default function yesman(pi: ExtensionAPI) {
	if (!API || !TOKEN || !AGENT) return; // not started by yesman

	pi.on("before_agent_start", async (event) => {
		let text: string;
		try {
			[text] = await Promise.all([call("GET", "/v1/context"), commandEnv()]);
		} catch (err) {
			text = `The yesman app did not answer (${(err as Error).message}), so yesman's tools may fail.`;
		}
		const options = event.systemPromptOptions as { sections?: Record<string, string | null> } | undefined;
		if (options) {
			options.sections ??= {};
			options.sections.yesman = text;
		}
	});

	// The shell tools, pi's own, with the environment set in each command.
	// They keep pi's choice of which is active (defaultTools): registering
	// them does not activate them.
	const shell = (make: typeof createBashToolDefinition) => {
		const base = make(process.cwd());
		pi.registerTool({
			...base,
			defaultActive: false,
			async execute(id, params, signal, onUpdate, ctx) {
				const { env, note } = await commandEnv(signal);
				const tool = make(ctx.cwd, { spawnHook: (c) => ({ ...c, env: { ...c.env, ...env } }) });
				const update: typeof onUpdate = onUpdate && ((partial) => onUpdate({ ...partial, content: hideContent(partial.content ?? []) }));
				let out;
				try {
					out = await tool.execute(id, params, signal, update, ctx);
				} catch (err) {
					const message = hide((err as Error).message ?? String(err));
					throw new Error(note ? `${message}\n\n${note}` : message);
				}
				return note ? { ...out, content: [...out.content, { type: "text" as const, text: note }] } : out;
			},
		});
	};
	shell(createBashToolDefinition);
	if (process.platform === "win32") shell(createPowerShellToolDefinition);

	// Whatever a tool returns, a secret's value does not reach the model.
	pi.on("tool_result", (event) => {
		if (!hidden.length) return;
		const content = hideContent(event.content);
		const changed = content.some((c, i) => (c as any).text !== (event.content[i] as any).text);
		const structured = event.structuredContent === undefined ? undefined : hideJSON(event.structuredContent);
		const structuredChanged = structured !== undefined && JSON.stringify(structured) !== JSON.stringify(event.structuredContent);
		if (!changed && !structuredChanged) return;
		return structured === undefined ? { content } : { content, structuredContent: structured as any };
	});

	pi.registerTool({
		name: "yesman_env",
		label: "yesman: environment",
		description:
			"Say what the environment variables yesman sets in every command are: the user's secrets (keys and tokens) and variables, with what each is for, and each variable's value. A secret's value is never shown.",
		parameters: Type.Object({
			name: Type.Optional(Type.String({ description: "One variable's name; all of them when left out" })),
		}),
		async execute(_id, params, signal) {
			const r = await call("GET", "/v1/env/describe", undefined, signal);
			let vars: any[] = r.vars ?? [];
			if (params.name) vars = vars.filter((v) => v.name === params.name);
			const lines = vars.map((v) => {
				const what = v.kind === "secret" ? (v.locked ? "secret, locked" : "secret") : "variable";
				const value = v.kind === "variable" ? ` = ${JSON.stringify(v.value ?? "")}` : "";
				const description = v.description ? `: ${v.description.trim().replace(/\n/g, "\n    ")}` : "";
				return `- ${v.name} (${what})${value}${description}`;
			});
			const notes: string[] = [];
			if (r.locked) notes.push("The secrets are locked, so commands get only the variables now. Ask the user to unlock them in yesman's Settings › Secrets.");
			if (r.errors?.length) notes.push(`yesman could not read some of them: ${r.errors.join("; ")}`);
			const text = vars.length
				? lines.join("\n")
				: params.name
					? `yesman sets no ${params.name}. The user adds secrets and variables in yesman's Settings.`
					: "yesman sets no secrets or variables. The user adds them in yesman's Settings › Secrets and Settings › Variables.";
			return result([text, ...notes].join("\n\n"), r);
		},
	});

	pi.registerTool({
		name: "yesman_repos",
		label: "yesman: repos",
		description: "List the repositories in yesman's shared repo pool and the worktrees checked out in this workspace.",
		parameters: Type.Object({}),
		async execute(_id, _params, signal) {
			const r = await call("GET", "/v1/repos", undefined, signal);
			const repos = (r.repos ?? []).map((x: any) => `- ${x.name} (${x.url}${x.upstream ? `, upstream ${x.upstream}` : ""}${x.defaultBranch ? `, default ${x.defaultBranch}` : ""})`);
			const wts = (r.worktrees ?? []).map((w: any) => `- ${w.path}: ${w.repo}, ${w.detached ? `detached at ${w.head}` : `branch ${w.branch}`}`);
			return result(`Pool:\n${repos.join("\n") || "(empty)"}\n\nWorktrees in this workspace:\n${wts.join("\n") || "(none)"}`, r);
		},
	});

	pi.registerTool({
		name: "yesman_repo_add",
		label: "yesman: add repo",
		description:
			"Add a GitHub repository to yesman's shared repo pool, so that any yesman can check it out. The pool takes only GitHub repositories. For a fork, yesman also fetches the repository it was forked from, as the upstream remote. Adding a repository already in the pool returns it.",
		parameters: Type.Object({
			repo: Type.String({ description: "The GitHub repository: owner/repo, or its URL such as https://github.com/owner/repo or git@github.com:owner/repo.git" }),
		}),
		async execute(_id, params, signal) {
			const repo = await call("POST", "/v1/repos", params, signal);
			return result(`In the pool as ${repo.name}${repo.defaultBranch ? `, default branch ${repo.defaultBranch}` : ""}.`, repo);
		},
	});

	pi.registerTool({
		name: "yesman_checkout",
		label: "yesman: checkout",
		description:
			"Check a worktree of a pool repository out into this workspace, after fetching it. With branch, continue that branch, or create it from base (default: the remote's branch of that name, else its default branch). Without branch or ref, a new branch yesman/<yesman>/<dir> is created. With ref (a tag, commit or remote branch such as upstream/main), check out a detached copy for reading.",
		parameters: Type.Object({
			repo: Type.String({ description: "Pool name such as github.com/owner/repo, or a unique part such as repo" }),
			branch: Type.Optional(Type.String({ description: "Branch to work on" })),
			base: Type.Optional(Type.String({ description: "Start point of a new branch, such as origin/main or upstream/main" })),
			ref: Type.Optional(Type.String({ description: "Check out this ref detached, for reading" })),
			dir: Type.Optional(Type.String({ description: "Directory name in the workspace; default <repo>--<branch> or <repo>@<ref>" })),
		}),
		async execute(_id, params, signal) {
			const wt = await call("POST", "/v1/checkout", params, signal);
			const where = wt.detached ? `detached at ${wt.head}` : `on branch ${wt.branch}`;
			return result(`Checked out ${wt.repo} at ${wt.path}, ${where}.${wt.warning ? `\nWarning: ${wt.warning}` : ""}`, wt);
		},
	});

	pi.registerTool({
		name: "yesman_worktree_remove",
		label: "yesman: remove worktree",
		description:
			"Remove a worktree from this workspace once its work is merged or abandoned. Its branch stays in the pool. Refuses when it has uncommitted changes, unless force.",
		parameters: Type.Object({
			dir: Type.String({ description: "The worktree's directory name in the workspace" }),
			force: Type.Optional(Type.Boolean({ description: "Discard uncommitted changes" })),
		}),
		async execute(_id, params, signal) {
			await call("POST", "/v1/worktrees/remove", params, signal);
			return result(`Removed ${params.dir}.`);
		},
	});

	pi.registerTool({
		name: "yesman_agents",
		label: "yesman: agents",
		description: "List this yesman's agents, the lead and its workers, with their status and tasks.",
		parameters: Type.Object({}),
		async execute(_id, _params, signal) {
			const agents = await call("GET", "/v1/agents", undefined, signal);
			return result(json(agents), agents);
		},
	});

	if (ROLE !== "lead") return;

	pi.registerTool({
		name: "yesman_worker_start",
		label: "yesman: start worker",
		description:
			"Start a worker: a new pi agent of this yesman, in this workspace, that works on one task and reports its final message back to you when it finishes a run. Give it a complete task: the goal, which worktree to use (or to check one out), what done means and how to deliver (commit, push, PR). Workers run in parallel; give each its own worktree.",
		parameters: Type.Object({
			name: Type.String({ description: "Short name: 1-32 lowercase letters, digits and dashes, such as fix-login" }),
			task: Type.String({ description: "The task, complete enough to work on without asking" }),
		}),
		async execute(_id, params, signal) {
			const info = await call("POST", "/v1/workers", params, signal);
			return result(`Started worker ${info.name}. It will report back when it finishes a run.`, info);
		},
	});

	pi.registerTool({
		name: "yesman_worker_send",
		label: "yesman: message worker",
		description: "Send a message to one of your workers. A working worker reads it after its current tool calls; an idle one starts a new run.",
		parameters: Type.Object({
			name: Type.String({ description: "The worker's name" }),
			message: Type.String(),
		}),
		async execute(_id, params, signal) {
			await call("POST", "/v1/workers/send", params, signal);
			return result(`Sent to ${params.name}.`);
		},
	});

	pi.registerTool({
		name: "yesman_worker_remove",
		label: "yesman: remove worker",
		description: "Stop a worker and remove it from this yesman once its task is done. Its session log stays in .yesman/sessions/.",
		parameters: Type.Object({
			name: Type.String({ description: "The worker's name" }),
		}),
		async execute(_id, params, signal) {
			await call("POST", "/v1/workers/remove", params, signal);
			return result(`Removed worker ${params.name}.`);
		},
	});
}
