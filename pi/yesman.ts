// yesman's pi extension. yesman loads it into every agent it starts
// (`pi -e`), with the address of its local API in the environment.
//
// It gives the agent its yesman's context before every run (workspace,
// worktrees, the repo pool, shared knowledge, the board, the other agents)
// and tools to check repositories out of the pool, keep the board and, for
// a lead, to run workers. Everything is done by the yesman app; this file
// only forwards.
//
// A lead delegates, so that it is always free to talk: it has no shell and
// writes only knowledge notes. Its tools are narrowed when its session
// starts, and any call outside them is refused.
import { isAbsolute, relative, resolve } from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const API = process.env.YESMAN_API;
const TOKEN = process.env.YESMAN_TOKEN;
const AGENT = process.env.YESMAN_AGENT;
const ROLE = process.env.YESMAN_ROLE;
const WORKSPACE = process.env.YESMAN_WORKSPACE;

// Tools a lead does not get, and read-only ones it gets in their place.
const LEAD_DENIED = ["bash"];
const LEAD_WRITES = ["edit", "write"]; // allowed only for knowledge notes
const LEAD_ADDED = ["grep", "find", "ls"];

// knowledgePath reports whether a path a tool was given is a knowledge note.
function knowledgePath(path: unknown): boolean {
	if (typeof path !== "string" || !path || !WORKSPACE) return false;
	const dir = resolve(WORKSPACE, ".yesman", "knowledge");
	const rel = relative(dir, resolve(WORKSPACE, path.replace(/^@/, "")));
	return rel !== "" && !rel.startsWith("..") && !isAbsolute(rel);
}

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

function boardText(board: any): string {
	const tasks: any[] = board?.tasks ?? [];
	if (!tasks.length) return "The board is empty.";
	const line = (t: any) => {
		let s = `- ${t.id} [${t.status}] ${t.title}`;
		if (t.owner) s += ` (owner ${t.owner})`;
		if (t.note) s += `\n  note: ${String(t.note).replace(/\n/g, "\n  ")}`;
		if (t.detail) s += `\n  detail: ${String(t.detail).replace(/\n/g, "\n  ")}`;
		return s;
	};
	const open = tasks.filter((t) => t.status !== "done" && t.status !== "dropped");
	const closed = tasks.filter((t) => t.status === "done" || t.status === "dropped");
	const parts = [`Open (${open.length}):\n${open.map(line).join("\n") || "(none)"}`];
	if (closed.length) parts.push(`Done or dropped (${closed.length}):\n${closed.map(line).join("\n")}`);
	return parts.join("\n\n");
}

export default function yesman(pi: ExtensionAPI) {
	if (!API || !TOKEN || !AGENT) return; // not started by yesman

	if (ROLE === "lead") {
		// Narrow the lead's tools before every run, in case something
		// enabled a denied one since.
		pi.on("before_agent_start", async () => {
			const all = new Set(pi.getAllTools().map((t) => t.name));
			const active = pi.getActiveTools();
			const wanted = active.filter((name) => !LEAD_DENIED.includes(name));
			for (const name of LEAD_ADDED) if (all.has(name) && !wanted.includes(name)) wanted.push(name);
			if (wanted.length !== active.length || wanted.some((name) => !active.includes(name))) pi.setActiveTools(wanted);
		});
		pi.on("tool_call", async (event) => {
			if (LEAD_DENIED.includes(event.toolName)) {
				return { block: true, reason: "The lead has no shell, to stay free for the user. Hand this to a worker with yesman_worker_start." };
			}
			if (LEAD_WRITES.includes(event.toolName) && !knowledgePath((event.input as { path?: unknown } | undefined)?.path)) {
				return { block: true, reason: "The lead writes only knowledge notes under .yesman/knowledge/. Hand other changes to a worker with yesman_worker_start." };
			}
		});
	}

	pi.on("before_agent_start", async (event) => {
		let text: string;
		try {
			text = await call("GET", "/v1/context");
		} catch (err) {
			text = `The yesman app did not answer (${(err as Error).message}), so yesman's tools may fail.`;
		}
		const options = event.systemPromptOptions as { sections?: Record<string, string | null> } | undefined;
		if (options) {
			options.sections ??= {};
			options.sections.yesman = text;
		}
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

	const taskFields = {
		title: Type.Optional(Type.String({ description: "One line, up to 200 characters" })),
		status: Type.Optional(Type.String({ description: "todo, doing, blocked, done or dropped" })),
		owner: Type.Optional(Type.String({ description: "The agent the task is handed to, usually a worker's name; empty to clear" })),
		detail: Type.Optional(Type.String({ description: "What the task is, in Markdown" })),
		note: Type.Optional(Type.String({ description: "Where it stands: why it is blocked, or its result with links (branch, PR)" })),
	};
	pi.registerTool({
		name: "yesman_board",
		label: "yesman: board",
		description:
			"Read or change this yesman's board: its task list, which the user watches beside the conversation. Call it with no arguments to read it. One call can add, update, remove and order tasks, and is applied whole or not at all. Tasks have IDs such as t3, given by the board. Statuses: todo, doing, blocked, done, dropped.",
		parameters: Type.Object({
			add: Type.Optional(Type.Array(Type.Object({ ...taskFields, title: Type.String({ description: "One line, up to 200 characters" }) }), { description: "New tasks, added at the end; status defaults to todo" })),
			update: Type.Optional(Type.Array(Type.Object({ id: Type.String({ description: "The task's ID, such as t3" }), ...taskFields }), { description: "Changes to tasks: only the fields given change" })),
			remove: Type.Optional(Type.Array(Type.String(), { description: "IDs of tasks to delete. Mark finished work done or dropped instead; remove only mistakes." })),
			order: Type.Optional(Type.Array(Type.String(), { description: "Task IDs in the order to show them; tasks left out follow in their current order" })),
		}),
		async execute(_id, params, signal) {
			const change = Object.fromEntries(Object.entries(params).filter(([, v]) => Array.isArray(v) && v.length));
			const board = Object.keys(change).length
				? await call("POST", "/v1/board", change, signal)
				: await call("GET", "/v1/board", undefined, signal);
			return result(boardText(board), board);
		},
	});

	if (ROLE !== "lead") return;

	pi.registerTool({
		name: "yesman_worker_start",
		label: "yesman: start worker",
		description:
			"Start a worker: a new pi agent of this yesman, in this workspace, that works on one task and reports its final message back to you when it finishes a run; then yesman closes it and shows it as done until you message it again. Give it a title the user sees beside its name, and a complete task: the goal, which worktree to use (or to check one out), what done means and how to deliver (commit, push, PR), and the ID of its board task, whose owner you set to the worker's name. Workers run in parallel; give each its own worktree.",
		parameters: Type.Object({
			name: Type.String({ description: "Short name: 1-32 lowercase letters, digits and dashes, such as fix-login" }),
			title: Type.String({ description: "The subject of its work in a few words, up to 80 characters, as the user should see it: usually its board task's title, such as \"Fix the login timeout\". Not an instruction like \"Do task t5\"." }),
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
		description: "Send a message to one of your workers. A working worker reads it after its current tool calls; an idle one starts a new run, and a done one is started again with its session and has work again.",
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
		name: "yesman_worker_answer",
		label: "yesman: answer worker",
		description:
			"Answer the question one of your workers is waiting on, which yesman sent you as a [yesman] message: value for a choice (one of its options) or for text, confirmed for a yes or no, or cancelled to dismiss it. Workers' questions come to you, not the user: decide yourself whenever you can.",
		parameters: Type.Object({
			name: Type.String({ description: "The worker's name" }),
			requestId: Type.String({ description: "The original question's requestId, from its notification; never substitute a newer question" }),
			value: Type.Optional(Type.String({ description: "The option chosen, as offered, or the text asked for" })),
			confirmed: Type.Optional(Type.Boolean({ description: "The answer to a yes or no question" })),
			cancelled: Type.Optional(Type.Boolean({ description: "Dismiss the question without an answer" })),
		}),
		async execute(_id, params, signal) {
			await call("POST", "/v1/workers/answer", params, signal);
			return result(`Answered ${params.name}.`);
		},
	});

	pi.registerTool({
		name: "yesman_worker_remove",
		label: "yesman: remove worker",
		description: "Remove a worker from this yesman for good. Not needed when its task is done: a worker that reported is closed and kept as done, for follow-ups. Its session log stays in .yesman/sessions/.",
		parameters: Type.Object({
			name: Type.String({ description: "The worker's name" }),
		}),
		async execute(_id, params, signal) {
			await call("POST", "/v1/workers/remove", params, signal);
			return result(`Removed worker ${params.name}.`);
		},
	});
}
