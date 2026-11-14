// yesman's window. The Go side owns all state; this page renders it and
// the live event stream of the agents' pi processes.
//
//   mygo.call("Yesman.<Method>", ...)  services in service.go
//   mygo.on("agents" | "agent-event" | "changed" | "clones" | "fetches" | "pi-packages" | "focus-agent" | "update" | "files", fn)

import { orderWorkers } from './sidebar.js';
import { renderMarkdown } from './markdown.js';
import { $, h, icon } from './dom.js';
import { createFiles } from './files.js';
import { createInfo } from './info.js';
import { createBoard } from './board.js';
import { createContextViewer } from './context.js';
import { createModelPicker } from './models.js';
import { createPiPage } from './pi.js';
import { createEnvPage } from './env.js';
import { formatSize } from './diff.js';
import { IMAGE_ACCEPT, imageFileError, readImageFile, imageURL, imagePayload } from './images.js';
import { toast, openDialog, initSelect } from './mds/js/magpie.js';
import { t, tn, tNodes, formatNumber, setLocale, locale, translatePage, LANGUAGES } from './i18n.js';

const mygo = window.mygo;
const call = (method, ...args) => mygo.call(`Yesman.${method}`, ...args);
const errorText = (err) => (err && (err.message || err.error)) || String(err);
const fail = (err) => toast(errorText(err), { duration: 6000 });

const STATUS = {
  stopped: 'Asleep',
  starting: 'Starting…',
  idle: 'Ready',
  working: 'Working',
  waiting: 'Needs you',
  failed: 'Stopped',
};
const statusText = (status) => (STATUS[status] ? t(STATUS[status]) : status);

// A worker's question goes to its lead, not to you: it shows as asking,
// in the working colour, and only a lead can need you.
const asking = (a) => a.role === 'worker' && a.status === 'waiting';
const statusLabel = (a) => (asking(a) ? t('Asking {name}', { name: a.yesman }) : statusText(a.status));
const statusTone = (a) => (asking(a) ? 'asking' : a.status);

const state = {
  snap: { yesmen: [], agents: [] },
  view: { kind: 'none' },
  convos: new Map(),
  drafts: new Map(),
  attachments: new Map(),
  modelOptions: new Map(),
  pane: null, // the pane beside the conversation: 'files', 'board' or null
  hideDone: new Set(JSON.parse(localStorage.getItem('yesman.hideDone') ?? '[]')), // yesmen whose done workers are hidden
  // The info panel between the conversation and the files, open unless
  // closed, as it was last.
  panelOpen: localStorage.getItem('yesman.info') !== 'hidden',
  repos: [],
  settingsPage: 'general',
  settings: null, // Yesman.Settings, for the General page
  storageDraft: null, // destination and inline feedback while changing folders
  storageUsage: { u: null, busy: false, error: '' }, // Yesman.StorageUsage, measured in the background
  about: null, // Yesman.About
  config: { busy: false, exported: null, imported: null }, // the last export and import, for Import and export
  repoQuery: '',
  fetches: { fetching: [], errors: {} }, // Yesman.Fetches: repositories being fetched, and their last errors
  clones: [], // Yesman.Clones: repositories on their way into the pool
  update: null, // Yesman.Update: where updating yesman stands
};

const agentById = (id) => state.snap.agents.find((a) => a.id === id);
const yesmanOf = (id) => state.snap.yesmen.find((y) => y.name === id?.split('/')[0]);
const selectedAgent = () => (state.view.kind === 'agent' ? agentById(state.view.id) : null);

// ---------- Conversations ----------

class Convo {
  constructor(a) {
    this.id = a.id;
    this.key = keyOf(a);
    this.messages = [];
    this.loaded = false;
    this.error = null;
    this.live = null; // { message, el, blocks: Map(index -> el) }
    this.tools = new Map(); // toolCallId -> { args, status, result }
  }
}

// An agent's name can be reused: a dismissed yesman's name can be hired
// again, and a lead can start a new worker with a removed one's name. Its
// hiring or start time tells those agents apart.
const keyOf = (a) => `${a.id}@${a.since ?? ''}`;
const contextViewer = createContextViewer({ call, keyOf });
const contextEvents = new Set(['message_end', 'tool_execution_end', 'agent_settled', 'compaction_end', 'auto_compaction_end']);

function convo(a) {
  let c = state.convos.get(keyOf(a));
  if (!c) state.convos.set(keyOf(a), (c = new Convo(a)));
  return c;
}

// Forget conversations and drafts of agents that are gone.
function pruneCaches() {
  const keys = new Set(state.snap.agents.map(keyOf));
  for (const map of [state.convos, state.drafts, state.attachments, state.modelOptions]) {
    for (const key of map.keys()) if (!keys.has(key)) map.delete(key);
  }
  contextViewer.prune(keys);
}

// Records that arrive while the history loads are queued, then replayed on
// top of it, skipping messages the history already has, so a turn in
// progress keeps streaming and none shows twice.
const messageKey = (m) => `${m?.role}:${m?.timestamp}`;

async function loadConvo(c) {
  c.queue = [];
  try {
    const data = await call('Messages', c.id);
    c.messages = data?.messages ?? [];
    c.tools.clear();
    for (const message of c.messages) indexMessage(c, message);
    c.live = null;
    c.loaded = true;
    c.error = null;
    // The reply being written when the history was read...
    for (const ev of data?.live ?? []) applyEvent(c, ev.data, true);
    // ...then what arrived since. An event numbered at or before the
    // history is in it, except a message that ended in between.
    const seen = new Set(c.messages.map(messageKey));
    for (const ev of c.queue) {
      const e = ev.data;
      if (ev.seq <= (data?.seq ?? 0) && !(e.type === 'message_end' && !seen.has(messageKey(e.message)))) continue;
      if (e.type === 'message_end' && seen.has(messageKey(e.message))) continue;
      applyEvent(c, e, true);
      if (e.type === 'message_end') seen.add(messageKey(e.message));
    }
  } catch (err) {
    c.error = errorText(err);
  }
  c.queue = null;
  const a = selectedAgent();
  if (a && keyOf(a) === c.key) renderThread();
}

const textOf = (content) =>
  typeof content === 'string' ? content : (content ?? []).filter((b) => b.type === 'text').map((b) => b.text).join('\n');

// Tool state is built from history and live events; rendering only reads it.
function indexTool(c, block) {
  const tool = c.tools.get(block.id) ?? { status: 'pending' };
  tool.args = block.arguments ?? tool.args ?? {};
  c.tools.set(block.id, tool);
}

function indexMessage(c, message) {
  if (message.role === 'assistant') {
    for (const block of message.content ?? []) {
      if (block.type === 'toolCall') indexTool(c, block);
    }
  } else if (message.role === 'toolResult') {
    const tool = c.tools.get(message.toolCallId) ?? {};
    tool.status = message.isError ? 'error' : 'done';
    tool.result = resultText(message);
    c.tools.set(message.toolCallId, tool);
  }
}

function toolSummary(args) {
  if (!args || typeof args !== 'object') return '';
  for (const key of ['command', 'path', 'file_path', 'pattern', 'url', 'query', 'repo', 'name', 'dir', 'message']) {
    if (typeof args[key] === 'string' && args[key]) return args[key].split('\n')[0];
  }
  const first = Object.values(args).find((v) => typeof v === 'string');
  return first ? first.split('\n')[0] : '';
}

function resultText(result) {
  if (!result) return '';
  if (typeof result === 'string') return result;
  return textOf(result.content) || (result.details ? JSON.stringify(result.details, null, 2) : '');
}

function toolEl(c, block) {
  const tool = c.tools.get(block.id);
  const el = h('details', { class: 'tool', 'data-tool': block.id, 'data-status': tool.status });
  el.append(
    h('summary', null,
      icon(tool.status === 'done' ? 'check' : tool.status === 'error' ? 'alert' : 'spinner', 'mds-icon tool-mark'),
      h('span', { class: 'tool-name' }, block.name.replace(/^yesman_/, 'yesman · ')),
      h('span', { class: 'tool-sum' }, toolSummary(tool.args)),
      h('span', { class: 'tool-state' }, tool.status === 'error' ? t('failed') : '')),
  );
  el.addEventListener('toggle', () => {
    if (!el.open) return;
    const cur = c.tools.get(block.id);
    el.querySelector('.tool-body')?.remove();
    el.append(
      h('div', { class: 'tool-body' },
        h('pre', null, JSON.stringify(cur.args ?? {}, null, 2)),
        cur.result ? h('pre', null, cur.result) : null),
    );
  });
  return el;
}

function updateTool(c, id) {
  const tool = c.tools.get(id);
  const el = document.querySelector(`#thread [data-tool="${CSS.escape(id)}"]`);
  if (!tool || !el) return;
  el.dataset.status = tool.status;
  const mark = el.querySelector('.tool-mark');
  mark?.replaceWith(icon(tool.status === 'done' ? 'check' : tool.status === 'error' ? 'alert' : 'spinner', 'mds-icon tool-mark'));
  el.querySelector('.tool-state').textContent = tool.status === 'error' ? t('failed') : '';
  el.querySelector('.tool-sum').textContent = toolSummary(tool.args);
  if (el.open) el.dispatchEvent(new Event('toggle'));
}

function blockEl(c, block) {
  if (block.type === 'text') return h('div', { class: 'md', html: renderMarkdown(block.text) });
  if (block.type === 'thinking') {
    if (!block.thinking?.trim()) return null;
    return h('details', { class: 'thinking' }, h('summary', null, t('Thinking')), h('div', null, block.thinking));
  }
  if (block.type === 'toolCall') return toolEl(c, block);
  return null;
}

function messageEl(c, m) {
  switch (m.role) {
    case 'user': {
      const text = textOf(m.content);
      const report = text.match(/^\[yesman\] Worker "([^"]+)" finished a run\. Its final message:\n\n([\s\S]*)$/);
      if (report) {
        return h('details', { class: 'note', open: true },
          h('summary', null, t('Report from worker {name}', { name: report[1] })),
          h('div', { class: 'md', html: renderMarkdown(report[2]) }));
      }
      const fromLead = text.match(/^\[from your lead\] ([\s\S]*)$/);
      if (fromLead) {
        return h('div', { class: 'note' }, h('strong', null, t('From the lead')), h('div', { class: 'md', html: renderMarkdown(fromLead[1]) }));
      }
      // What yesman tells the lead: a worker's question, how a run ended
      // when it did not finish, or the tasks still open.
      const notice = text.match(/^\[yesman\] ([\s\S]*)$/);
      if (notice) return h('div', { class: 'note note--quiet' }, h('div', { class: 'md', html: renderMarkdown(notice[1]) }));
      const images = Array.isArray(m.content) ? m.content.filter((b) => b.type === 'image' && imageURL(b)) : [];
      return h('div', { class: 'msg-user' }, text,
        images.length ? h('div', { class: 'message-images' }, images.map((image) =>
          h('img', { src: imageURL(image), alt: t('Attached image'), loading: 'lazy' }))) : null);
    }
    case 'assistant': {
      const el = h('div', { class: 'msg-assistant' });
      for (const block of m.content ?? []) {
        const b = blockEl(c, block);
        if (b) el.append(b);
      }
      if (m.stopReason === 'error' && m.errorMessage) {
        el.append(h('div', { class: 'note note--error' }, m.errorMessage));
      } else if (m.stopReason === 'aborted') {
        el.append(h('div', { class: 'note note--quiet' }, t('Stopped.')));
      }
      return el.childNodes.length ? el : null;
    }
    case 'toolResult':
      return null; // shown with its tool call
    case 'compactionSummary':
    case 'branchSummary':
      return h('div', { class: 'note note--quiet' }, t('Earlier conversation was summarized to save context.'));
    default: {
      if (m.display === false) return null;
      const text = textOf(m.content);
      return text ? h('div', { class: 'note note--quiet' }, text) : null;
    }
  }
}

function atBottom(scroller) {
  return scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
}

function scrollDown(force = false) {
  const scroller = $('#scroll');
  if (!scroller) return;
  if (force || scroller.dataset.stick !== 'false') scroller.scrollTop = scroller.scrollHeight;
}

function renderThread() {
  const thread = $('#thread');
  const a = selectedAgent();
  if (!thread || !a) return;
  const c = convo(a);
  thread.replaceChildren();
  if (c.error) {
    thread.append(h('div', { class: 'note note--error' }, c.error));
  } else if (!c.loaded) {
    thread.append(h('div', { class: 'working' }, icon('spinner', 'mds-icon tool-mark'), t('Waking up…')));
  } else if (!c.messages.length && !c.live) {
    const y = yesmanOf(a.id);
    thread.append(
      a.role === 'lead'
        ? h('div', { class: 'thread-empty' }, h('strong', null, t('{name} is ready', { name: a.yesman })),
            t('Tell {name} what you want done. It works in {path}, checks out repos from the pool, and starts workers when the job splits up.', { name: a.yesman, path: y?.path ?? t('its workspace') }))
        : h('div', { class: 'thread-empty' }, h('strong', null, a.name), t('This worker has not started yet.')),
    );
  } else {
    for (const m of c.messages) {
      const el = messageEl(c, m);
      if (el) thread.append(el);
    }
    if (c.live) {
      // Rebuild the streaming message: deltas that arrived while another
      // agent was shown only updated its data.
      c.live.el.replaceChildren();
      c.live.blocks.clear();
      c.live.message.content.forEach((block, i) => {
        const el = block && blockEl(c, block);
        if (el) {
          c.live.el.append(el);
          c.live.blocks.set(i, el);
        }
      });
      thread.append(c.live.el);
    }
  }
  renderWorking();
  scrollDown(true);
}

function renderWorking() {
  const thread = $('#thread');
  if (!thread) return;
  thread.querySelector('.working-live')?.remove();
  const a = selectedAgent();
  if (a?.status === 'working') {
    thread.append(h('div', { class: 'working working-live', 'data-status': 'working' }, h('i', { class: 'mds-dot' }), h('span', { class: 'working-label' }, workingLabel(a))));
  }
}

// Activity comes from the run's events in English. Translate its action,
// keeping the tool's subject (a path, command or name) as it was reported.
const ACTIVITY_SUBJECTS = {
  'Reading ': 'Reading {subject}',
  'Editing ': 'Editing {subject}',
  'Writing ': 'Writing {subject}',
  'Running ': 'Running {subject}',
  'Searching for ': 'Searching for {subject}',
  'Finding ': 'Finding {subject}',
  'Listing ': 'Listing {subject}',
  'Starting worker ': 'Starting worker {subject}',
  'Messaging ': 'Messaging {subject}',
  'Answering ': 'Answering {subject}',
  'Removing worker ': 'Removing worker {subject}',
  'Checking out ': 'Checking out {subject}',
  'Removing worktree ': 'Removing worktree {subject}',
  'Adding to the pool ': 'Adding to the pool {subject}',
  'Using ': 'Using {subject}',
};

function workingLabel(a) {
  if (!a.activity) return t('Working…');
  const [, message, parallel = ''] = a.activity.match(/^(.*?)( \(\+\d+\))?$/);
  let label = t(message);
  if (label === message) {
    for (const [prefix, key] of Object.entries(ACTIVITY_SUBJECTS)) {
      if (!message.startsWith(prefix)) continue;
      label = t(key, { subject: message.slice(prefix.length) });
      break;
    }
  }
  return `${label}${parallel}…`;
}

// Live records from an agent's pi process.
function onAgentEvent(ev) {
  const { agent: id, data: e } = ev;
  info.onAgentEvent(ev);
  if (e.type === 'extension_ui_request' && e.method === 'notify') {
    toast(`${id}: ${e.message}`);
    return;
  }
  if (e.type === 'extension_error') {
    toast(t('{agent}: extension error: {error}', { agent: id, error: e.error }), { duration: 6000 });
    return;
  }
  const agent = agentById(id);
  if (contextEvents.has(e.type)) contextViewer.refresh(agent);
  const c = agent && state.convos.get(keyOf(agent));
  if (!c) return;
  if (!c.loaded) {
    c.queue?.push(ev);
    return;
  }
  applyEvent(c, e, false);
}

// applyEvent updates a conversation with one record, and the thread when it
// is shown, unless the record is being replayed before the thread renders.
function applyEvent(c, e, replaying) {
  const visible = !replaying && state.view.kind === 'agent' && state.view.id === c.id && $('#thread');
  const scroller = $('#scroll');
  if (visible && scroller) scroller.dataset.stick = atBottom(scroller) ? 'true' : 'false';

  switch (e.type) {
    case 'message_start': {
      if (e.message?.role !== 'assistant') break;
      c.live = { message: { ...e.message, content: [] }, el: h('div', { class: 'msg-assistant' }), blocks: new Map() };
      if (visible) {
        const working = $('#thread .working-live');
        if (working) working.before(c.live.el);
        else $('#thread').append(c.live.el);
      }
      break;
    }
    case 'message_update': {
      if (!c.live) break;
      const u = e.assistantMessageEvent;
      const blocks = c.live.message.content;
      const i = u.contentIndex;
      switch (u.type) {
        case 'text_start': blocks[i] = { type: 'text', text: '' }; break;
        case 'text_delta': blocks[i] = { type: 'text', text: (blocks[i]?.text ?? '') + u.delta }; break;
        case 'text_end': blocks[i] = { type: 'text', text: u.content }; break;
        case 'thinking_start': blocks[i] = { type: 'thinking', thinking: '' }; break;
        case 'thinking_delta': blocks[i] = { type: 'thinking', thinking: (blocks[i]?.thinking ?? '') + u.delta }; break;
        case 'thinking_end': blocks[i] = { type: 'thinking', thinking: u.content }; break;
        case 'toolcall_start': blocks[i] = { type: 'toolCall', id: u.id, name: u.toolName, arguments: {} }; break;
        case 'toolcall_end': blocks[i] = u.toolCall; break;
        default: break;
      }
      if (blocks[i]?.type === 'toolCall') indexTool(c, blocks[i]);
      if (visible) scheduleLive(c, i);
      break;
    }
    case 'message_end': {
      const m = e.message;
      indexMessage(c, m);
      c.messages.push(m);
      if (m.role === 'assistant') {
        const old = c.live?.el;
        c.live = null;
        const el = messageEl(c, m);
        if (visible) {
          if (old?.isConnected) el ? old.replaceWith(el) : old.remove();
          else if (el) $('#thread').append(el);
        }
      } else if (visible) {
        if (m.role === 'toolResult') {
          updateTool(c, m.toolCallId);
        } else {
          const el = messageEl(c, m);
          if (el) {
            const working = $('#thread .working-live');
            working ? working.before(el) : $('#thread').append(el);
            $('#thread .thread-empty')?.remove();
          }
        }
      }
      break;
    }
    case 'tool_execution_start':
    case 'tool_execution_update':
    case 'tool_execution_end': {
      const tool = c.tools.get(e.toolCallId) ?? { args: e.args };
      tool.args = e.args ?? tool.args;
      if (e.type === 'tool_execution_end') {
        tool.status = e.isError ? 'error' : 'done';
        tool.result = resultText(e.result);
      } else {
        tool.status = 'running';
        if (e.partialResult) tool.result = resultText(e.partialResult);
      }
      c.tools.set(e.toolCallId, tool);
      if (visible) updateTool(c, e.toolCallId);
      break;
    }
    case 'compaction_start':
      if (visible) $('#thread').append(h('div', { class: 'note note--quiet' }, t('Summarizing earlier conversation to save context…')));
      break;
    case 'auto_retry_start':
      if (visible) toast(t('Retrying after: {error}', { error: e.errorMessage }));
      break;
    default:
      break;
  }
  if (visible) scrollDown();
}

// Streaming deltas re-render one block per animation frame.
let pending = null;
function scheduleLive(c, index) {
  pending ??= { c, indexes: new Set() };
  pending.indexes.add(index);
  if (pending.frame) return;
  pending.frame = requestAnimationFrame(() => {
    const { c: cv, indexes } = pending;
    pending = null;
    if (!cv.live) return;
    for (const i of indexes) {
      const block = cv.live.message.content[i];
      const old = cv.live.blocks.get(i);
      if (old && block?.type === 'toolCall' && old.dataset?.tool === block.id) {
        updateTool(cv, block.id);
        continue;
      }
      const fresh = block && blockEl(cv, block);
      if (fresh) {
        if (old) old.replaceWith(fresh);
        else cv.live.el.append(fresh);
        cv.live.blocks.set(i, fresh);
      }
    }
    scrollDown();
  });
}

// ---------- Sidebar ----------

function statusDot(status) {
  return h('span', { class: 'dot-wrap', 'data-status': status }, h('i', { class: 'mds-dot' }));
}

// How long a run has gone on: 8s, 4m, 1h 5m.
function elapsed(since) {
  const s = Math.max(0, Math.floor((Date.now() - new Date(since).getTime()) / 1000));
  if (s < 60) return t('{n}s', { n: formatNumber(s) });
  if (s < 3600) return t('{n}m', { n: formatNumber(Math.floor(s / 60)) });
  return t('{hours}h {minutes}m', { hours: formatNumber(Math.floor(s / 3600)), minutes: formatNumber(Math.floor((s % 3600) / 60)) });
}

function agentRow(a, cls, label) {
  const current = state.view.kind === 'agent' && state.view.id === a.id;
  const meta = a.status === 'working' && a.runStartedAt
    ? h('span', { class: 'meta', 'data-since': a.runStartedAt, title: t('How long this run has gone on') }, elapsed(a.runStartedAt))
    : h('span', { class: 'meta' }, a.status === 'idle' || a.status === 'stopped' || a.status === 'working' ? '' : asking(a) ? t('Asking') : statusText(a.status));
  const worker = cls.includes('agent-row--worker');
  const done = worker && a.finishedAt;
  let status = done ? t('Done') : statusLabel(a);
  if (worker && a.status === 'failed') status = t('{status}; {name} was told', { status, name: a.yesman });
  return h('button', {
    class: `agent-row ${cls}`, type: 'button', 'data-agent': a.id, 'data-status': statusTone(a), 'data-done': done ? 'true' : null,
    'data-sidebar-entry': worker ? 'worker' : cls.includes('agent-row--yesman') ? 'yesman' : 'needs-you',
    'aria-current': current ? 'page' : null, 'aria-haspopup': 'menu',
    title: `${a.id}: ${status}${a.title ? `\n${a.title}` : ''}${a.detail ? `\n${a.detail}` : ''}`,
    onclick: () => selectAgent(a.id),
    oncontextmenu: (e) => openAgentMenu(e, a),
    onkeydown: (e) => {
      if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) openAgentMenu(e, a);
    },
  }, done ? h('span', { class: 'dot-wrap' }, icon('check', 'mds-icon done-mark')) : statusDot(statusTone(a)),
  // A worker is one line: its number, then the subject of its work in words,
  // the same all along its task, never the commands it runs. Its name shows
  // on hover.
  worker
    ? [h('span', { class: 'num' }, workerNumber(a)), h('span', { class: 'subject' }, a.title || a.name)]
    : h('span', { class: 'name' }, label),
  meta);
}

// a1, a2, …: a worker's number within its yesman, in the order they started.
const workerNumber = (a) => (a.number ? `a${a.number}` : a.name);

// Run times count up while agents work.
setInterval(() => {
  for (const el of document.querySelectorAll('.agent-row .meta[data-since]')) el.textContent = elapsed(el.dataset.since);
}, 1000);

async function openAgentMenu(e, a) {
  e.preventDefault();
  e.stopPropagation();
  const row = e.currentTarget;
  const rect = row.getBoundingClientRect();
  const pointer = e.type === 'contextmenu' && (e.clientX !== 0 || e.clientY !== 0);
  const x = Math.round(pointer ? e.clientX : rect.left + 20);
  const y = Math.round(pointer ? e.clientY : rect.bottom);
  try {
    const action = await call('AgentMenu', a.id, x, y, state.hideDone.has(a.yesman));
    const current = agentById(a.id);
    if (!current || keyOf(current) !== keyOf(a)) return;
    const workspace = yesmanOf(a.id);
    switch (action) {
      case 'open': selectAgent(a.id); break;
      case 'workspace':
        state.pane = 'files';
        selectAgent(a.id);
        break;
      case 'reveal': await call('Reveal', workspace.path); break;
      case 'knowledge': await call('Reveal', `${workspace.path}/.yesman/knowledge/INDEX.md`); break;
      case 'logs': await call('Reveal', `${workspace.path}/.yesman/sessions/${a.name}`); break;
      case 'abort': await call('Abort', a.id); break;
      case 'sleep': await call('Stop', a.id); break;
      case 'archive': await dismiss(a.yesman); break;
      case 'remove': await removeWorker(a); break;
      case 'hide-done':
      case 'show-done': setHideDone(a.yesman, action === 'hide-done'); break;
      default:
        restoreSidebarFocus(row);
    }
  } catch (err) {
    fail(err);
  }
}

// A waiting lead has two entries. Preserve the entry as well as the agent;
// if it disappears, use the other entry, then the workspace's lead for a
// hidden/removed worker. Keep focus unchanged when the workspace is gone.
function restoreSidebarFocus(row) {
  if (!row?.dataset.agent) return;
  const list = $('#yesmen');
  const agent = `[data-agent="${CSS.escape(row.dataset.agent)}"]`;
  const entry = `[data-sidebar-entry="${CSS.escape(row.dataset.sidebarEntry)}"]`;
  const lead = `${row.dataset.agent.split('/')[0]}/lead`;
  const target = list.querySelector(agent + entry) ?? list.querySelector(agent)
    ?? list.querySelector(`[data-agent="${CSS.escape(lead)}"][data-sidebar-entry="yesman"]`);
  target?.focus({ preventScroll: true });
}

function renderSidebar() {
  const list = $('#yesmen');
  const focusedRow = list.contains(document.activeElement) ? document.activeElement : null;
  list.replaceChildren();
  const waiting = state.snap.agents.filter((a) => a.status === 'waiting' && a.role === 'lead');
  if (waiting.length) {
    list.append(h('p', { class: 'side-group' }, t('Needs you')));
    for (const a of waiting) list.append(agentRow(a, '', a.role === 'lead' ? a.yesman : a.id));
  }
  list.append(h('div', { class: 'side-group' }, h('span', null, t('Yesmen')),
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('Hire a yesman'), 'aria-label': t('Hire a yesman'), onclick: openHire }, icon('plus'))));
  for (const y of state.snap.yesmen) {
    const agents = state.snap.agents.filter((a) => a.yesman === y.name);
    const lead = agents.find((a) => a.role === 'lead');
    if (lead) list.append(agentRow(lead, 'agent-row--yesman', y.name));
    // Active workers come first; preserve start order within each class.
    // The yesman's menu can hide done workers, except the one being viewed.
    const hide = state.hideDone.has(y.name);
    for (const w of orderWorkers(agents.filter((a) => a.role === 'worker'))) {
      if (hide && w.finishedAt && !(state.view.kind === 'agent' && state.view.id === w.id)) continue;
      list.append(agentRow(w, 'agent-row--worker', w.name));
    }
  }
  restoreSidebarFocus(focusedRow);
  renderSideFoot();
}

// Whether a yesman's done workers are hidden, kept across launches.
function setHideDone(yesman, hide) {
  if (hide) state.hideDone.add(yesman);
  else state.hideDone.delete(yesman);
  localStorage.setItem('yesman.hideDone', JSON.stringify([...state.hideDone]));
  renderSidebar();
}

// ---------- Main column ----------

// The titlebar: where you are on the left, what you can do on the right.
function renderBar() {
  const crumbs = $('#crumbs');
  const sep = () => h('span', { class: 'mds-crumb-sep', 'aria-hidden': 'true' }, '/');
  const a = selectedAgent();
  if (state.view.kind === 'settings') {
    const page = SETTINGS.find((s) => s.page === state.view.page);
    crumbs.replaceChildren(h('span', null, t('Settings')), sep(), h('span', { 'aria-current': 'page' }, t(page.label)));
  } else if (a) {
    const y = yesmanOf(a.id);
    const here = h('span', { 'aria-current': 'page', title: y?.path ?? '' }, a.role === 'lead' ? a.yesman : a.name);
    crumbs.replaceChildren(...(a.role === 'lead' ? [here] : [h('span', null, a.yesman), sep(), here]));
  } else {
    crumbs.replaceChildren();
  }
  renderBarActions();
}

function renderBarActions() {
  const actions = $('#bar-actions');
  const a = state.view.kind === 'agent' ? selectedAgent() : null;
  const button = (iconName, label, onclick, extra = {}) =>
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick, ...extra }, icon(iconName));
  actions.replaceChildren(...[
    a ? h('span', { class: 'mds-status', 'data-status': statusTone(a), role: 'status' }, h('i', { class: 'mds-dot', 'aria-hidden': 'true' }), h('span', null, statusLabel(a))) : null,
    updatePill(),
    a && a.status !== 'stopped'
      ? button('power', t('Put this agent to sleep: end its pi process. It resumes its session when you next talk to it.'), () => call('Stop', a.id).catch(fail))
      : null,
    a?.role === 'worker' ? button('x', t('Remove worker'), () => removeWorker(a)) : null,
    a ? button('listDashes', state.panelOpen ? t('Hide the info') : t('Show the info'), togglePanel, { 'aria-pressed': String(state.panelOpen) }) : null,
    a ? button('listChecks', state.pane === 'board' ? t('Hide the board') : t('Show the board'), () => togglePane('board'), { 'aria-pressed': String(state.pane === 'board') }) : null,
    a ? button('panel', state.pane === 'files' ? t('Hide the files') : t('Show the files'), () => togglePane('files'), { 'aria-pressed': String(state.pane === 'files') }) : null,
  ].filter(Boolean));
}

// The pill shows only while there is something to do about an update.
function updatePill() {
  const u = state.update;
  if (!u?.enabled) return null;
  const pill = (iconName, label, title, extra) => h('button', { class: 'mds-pill mds-pill--responsive', type: 'button', title, ...extra },
    icon(iconName), h('span', { class: 'mds-pill-label' }, label));
  switch (u.state) {
    case 'available': return pill('up', t('Update {version}', { version: u.version }), t('Install yesman {version}', { version: u.version }), { onclick: installUpdate });
    case 'downloading': return pill('reload', t('Updating {percent}%', { percent: Math.round((u.progress ?? 0) * 100) }), t('Installing yesman {version}', { version: u.version }), { disabled: true, 'aria-busy': 'true' });
    case 'ready': return pill('up', t('Restart to update'), t('Restart into yesman {version}', { version: u.version }), { onclick: restartToUpdate });
    case 'failed': return pill('alert', t('Update failed'), `${t('Installing yesman {version} failed: {error}', { version: u.version, error: u.error })}\n${t('Click to try again.')}`, { 'data-tone': 'error', onclick: installUpdate });
    default: return null;
  }
}

function installUpdate() {
  call('InstallUpdate').catch(fail);
}

async function restartToUpdate() {
  const busy = state.snap.agents.filter((a) => a.status === 'working' || a.status === 'waiting').length;
  if (busy && !(await confirmAction(t('Restart yesman?'),
    tn(busy, 'An agent is working. Restarting stops it; it picks up its session when you next talk to it.',
      '{n} agents are working. Restarting stops them; they pick up their sessions when you next talk to them.'),
    t('Restart')))) return;
  call('RestartToUpdate').catch(fail);
}

function onUpdate(u) {
  state.update = u;
  renderBarActions();
  if (state.view.kind === 'settings' && state.view.page === 'about') renderBody();
}

// ---------- Model and effort ----------

let composerSelects = [];

function acceptModelInfo(a, info) {
  const current = agentById(a.id);
  if (!current || keyOf(current) !== keyOf(a)) return;
  onAgents(state.snap.agents.map((item) => item === current ? info : item));
}

async function loadModelOptions(a) {
  const key = keyOf(a);
  const cached = state.modelOptions.get(key);
  if (cached && (!cached.models || cached.busy || a.effortLevels)) return;
  const entry = { models: null, busy: false, error: null };
  state.modelOptions.set(key, entry);
  try {
    const data = await call('Models', a.id);
    entry.models = data.models;
    acceptModelInfo(a, data.agent);
  } catch (err) {
    entry.error = errorText(err);
  }
  if (selectedAgent() && keyOf(selectedAgent()) === key) renderDock();
}

async function changeModelOption(a, method, ...args) {
  const entry = state.modelOptions.get(keyOf(a));
  if (!entry || entry.busy) return;
  const focus = document.activeElement?.getAttribute('aria-label');
  entry.busy = true;
  renderDock();
  try {
    acceptModelInfo(a, await call(method, a.id, ...args));
  } catch (err) {
    fail(err);
  } finally {
    entry.busy = false;
    if (selectedAgent() && keyOf(selectedAgent()) === keyOf(a)) {
      const restoreFocus = document.activeElement === document.body;
      renderDock();
      if (restoreFocus) composerSelects.find((select) => select.trigger.getAttribute('aria-label') === focus)?.trigger.focus({ preventScroll: true });
    }
  }
}

function modelOptionsEl(a, working) {
  const entry = state.modelOptions.get(keyOf(a));
  if (!entry?.models) {
    return h('button', {
      class: 'mds-link-button', type: 'button', disabled: !entry?.error,
      title: entry?.error,
      onclick: () => { state.modelOptions.delete(keyOf(a)); loadModelOptions(a); renderDock(); },
    }, entry?.error ? t('Retry loading models') : t('Loading models…'));
  }
  const disabled = entry.busy || working || a.status === 'starting';
  const picker = createModelPicker({
    models: entry.models, provider: a.provider, model: a.model, disabled,
    onSelect: (selected) => changeModelOption(a, 'SetModel', selected.provider, selected.id),
  });
  composerSelects.push(picker);
  const levels = a.effortLevels ?? [];
  const effort = h('select', {
    class: 'mds-select mds-select--text', 'aria-label': t('Effort'), disabled: disabled || levels.length < 2,
    title: levels.length < 2 ? t('This model has no adjustable reasoning effort') : t('Reasoning effort'),
    onchange: () => changeModelOption(a, 'SetEffort', effort.value),
  }, levels.map((level) => h('option', { value: level }, effortLabel(level))));
  if (!levels.length) effort.append(h('option', { value: '' }, t('Unavailable')));
  effort.value = a.effort ?? '';
  return h('div', { class: 'composer-options', 'aria-busy': String(entry.busy) },
    h('span', { class: 'composer-choice composer-choice--model' }, picker.trigger),
    h('label', { class: 'composer-choice' }, h('span', null, t('Effort')), effort));
}

const EFFORT = { off: 'Off', minimal: 'Minimal', low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high' };
const effortLabel = (level) => (EFFORT[level] ? t(EFFORT[level]) : level[0].toUpperCase() + level.slice(1));

// ---------- Composer ----------

async function openHire() {
  const form = $('#hire-form');
  form.reset();
  if ((await openDialog($('#hire-dialog'))) === 'hire') hire(form.elements.name.value.trim());
}

function renderBody() {
  const body = $('#body');
  // A refresh while typing, say in the search box, keeps the caret there.
  const active = body.contains(document.activeElement) ? document.activeElement : null;
  contextViewer.close();
  for (const select of composerSelects) select.destroy();
  composerSelects = [];
  body.replaceChildren();
  if (state.view.kind === 'settings') {
    renderSettings(body);
    const again = active?.id && document.getElementById(active.id);
    if (again) {
      again.focus();
      if (['INPUT', 'TEXTAREA'].includes(again.tagName) && again.type !== 'password') again.setSelectionRange(active.selectionStart, active.selectionEnd);
    }
    return;
  }
  const a = selectedAgent();
  if (!a) return renderWelcome(body);

  const scroller = h('div', { class: 'scroll', id: 'scroll' }, h('div', { class: 'thread', id: 'thread' }));
  scroller.addEventListener('scroll', () => { scroller.dataset.stick = atBottom(scroller) ? 'true' : 'false'; });
  body.append(scroller, h('div', { class: 'dock' }, h('div', { class: 'dock-inner', id: 'dock' })));
  renderDock();
  renderThread();
}

function renderDock({ focusComposer = false } = {}) {
  const dock = $('#dock');
  const a = selectedAgent();
  const keep = dock?.querySelector('textarea');
  const focused = keep && document.activeElement === keep;
  const caret = focused ? [keep.selectionStart, keep.selectionEnd, keep.selectionDirection] : null;
  const control = dock?.contains(document.activeElement) && document.activeElement.getAttribute('role') === 'combobox'
    ? document.activeElement.getAttribute('aria-label') : null;
  for (const select of composerSelects) select.destroy();
  composerSelects = [];
  if (!dock || !a) return;
  dock.replaceChildren();

  if (a.status === 'failed' && a.detail) {
    dock.append(h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null, h('strong', null, t('This agent stopped')), h('p', null, a.detail))));
  } else if (a.detail) {
    dock.append(h('div', { class: 'mds-notice', role: 'status' }, h('div', null, h('strong', null, t('Needs a look')), h('p', null, a.detail))));
  }
  if (a.status === 'waiting' && a.dialog) dock.append(askEl(a));

  const working = a.status === 'working' || a.status === 'waiting';
  const key = keyOf(a);
  let attachments = state.attachments.get(key);
  if (!attachments) state.attachments.set(key, (attachments = { items: [], reading: false, sending: false }));
  const busy = attachments.reading || attachments.sending;
  const previews = h('div', { class: 'composer-images', hidden: !attachments.items.length }, attachments.items.map((image) =>
    h('div', { class: 'composer-image' },
      h('img', { src: imageURL(image), alt: image.name, title: image.name }),
      h('button', { class: 'round', type: 'button', disabled: busy, title: t('Remove image'), 'aria-label': t('Remove image'), onclick: () => {
        attachments.items = attachments.items.filter((item) => item !== image);
        renderDock();
      } }, icon('trash')))));
  const refresh = () => { if (keyOf(selectedAgent() ?? {}) === key) renderDock(); };
  const addImages = async (files) => {
    if (attachments.reading || attachments.sending || !files.length) return;
    const error = imageFileError(files, attachments.items);
    if (error) return fail(t(error));
    attachments.reading = true;
    refresh();
    try {
      const images = await Promise.all(files.map(readImageFile));
      attachments.items.push(...images);
    } catch (err) {
      fail(t(errorText(err)));
    } finally {
      attachments.reading = false;
      refresh();
    }
  };
  const fileInput = h('input', { type: 'file', accept: IMAGE_ACCEPT, multiple: true, hidden: true, onchange: () => {
    addImages([...fileInput.files]);
    fileInput.value = '';
  } });
  const attach = h('button', { class: 'round', type: 'button', disabled: busy, 'aria-label': t('Attach images'),
    title: t('Attach images (PNG, JPEG, GIF, WebP; 4 images, 5 MB each, 10 MB total)'), onclick: () => fileInput.click() }, icon('plus'));
  const textarea = h('textarea', {
    // Read-only keeps the caret/focus while RPC acceptance is pending. A
    // disabled textarea would drop focus to BODY before the next render.
    rows: 1, 'aria-label': t('Message'), readonly: attachments.sending,
    placeholder: a.role === 'lead' ? t('Message {name}…', { name: a.yesman }) : t('Message worker {name}…', { name: a.name }),
  });
  textarea.value = state.drafts.get(keyOf(a)) ?? '';
  const send = h('button', { class: 'round', type: 'submit', 'aria-label': t('Send'), title: working ? t('Send: delivered after its current step') : t('Send') }, icon('enter'));
  const stop = working ? h('button', { class: 'round round--stop', type: 'button', 'aria-label': t('Stop'), title: t('Stop the current run'), onclick: () => call('Abort', a.id).catch(fail) }, icon('stop')) : null;
  const sync = () => {
    send.disabled = (!textarea.value.trim() && !attachments.items.length) || busy || !!state.modelOptions.get(keyOf(a))?.busy;
    textarea.style.height = 'auto';
    textarea.style.height = `${textarea.scrollHeight}px`;
  };
  textarea.addEventListener('input', () => { state.drafts.set(keyOf(a), textarea.value); sync(); });
  textarea.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
      e.preventDefault();
      form.requestSubmit();
    }
  });
  textarea.addEventListener('paste', (e) => {
    const files = [...(e.clipboardData?.items ?? [])].filter((item) => item.kind === 'file' && item.type.startsWith('image/')).map((item) => item.getAsFile()).filter(Boolean);
    if (files.length) { e.preventDefault(); addImages(files); }
  });
  const form = h('form', { class: 'composer', onsubmit: (e) => { e.preventDefault(); sendMessage(a, textarea); } },
    fileInput, previews,
    h('div', { class: 'composer-input' }, textarea, h('div', { class: 'ask-actions' }, attach, stop, send)),
    h('div', { class: 'composer-controls' }, modelOptionsEl(a, working), contextViewer.mount(a)),
  );
  form.addEventListener('dragover', (e) => {
    if (e.dataTransfer?.types.includes('Files')) { e.preventDefault(); e.dataTransfer.dropEffect = busy ? 'none' : 'copy'; }
  });
  form.addEventListener('drop', (e) => {
    if (e.dataTransfer?.files.length) { e.preventDefault(); addImages([...e.dataTransfer.files]); }
  });
  dock.append(form);
  composerSelects.push(...[...form.querySelectorAll('select')].map((el) => {
    const select = initSelect(el);
    select.trigger.title = el.title;
    return select;
  }));
  sync();
  const nextControl = composerSelects.find((select) => select.trigger.getAttribute('aria-label') === control);
  if (nextControl && !nextControl.trigger.disabled) nextControl.trigger.focus({ preventScroll: true });
  else if (focusComposer || focused || !keep) {
    textarea.focus();
    if (caret) textarea.setSelectionRange(...caret);
  }
}

async function sendMessage(a, textarea) {
  const key = keyOf(a);
  const attachments = state.attachments.get(key);
  const text = textarea.value.trim();
  if ((!text && !attachments?.items.length) || attachments?.reading || attachments?.sending || state.modelOptions.get(key)?.busy) return;
  const images = imagePayload(attachments?.items ?? []);
  // A keyboard submit or a click on Send intends to continue composing.
  // Keep that focus through pending renders; subsequent renders follow the
  // actual focus, so moving to another control or agent cancels restoration.
  const focusComposer = document.activeElement === textarea ||
    document.activeElement === textarea.form?.querySelector('button[type="submit"]');
  attachments.sending = true;
  renderDock({ focusComposer });
  try {
    if (images.length) await call('SendImages', a.id, text, images);
    else await call('Send', a.id, text);
    state.drafts.delete(key);
    attachments.items = [];
  } catch (err) {
    // Keep both text and images for retry if pi rejects the prompt.
    fail(err);
  } finally {
    attachments.sending = false;
    if (keyOf(selectedAgent() ?? {}) === key) renderDock();
  }
}

// A question an extension asked through pi's RPC dialogs.
function askEl(a) {
  const d = a.dialog;
  const answer = (payload) => call('Answer', a.id, d.id, payload).catch(fail);
  const box = h('div', { class: 'ask', role: 'alert' }, h('strong', null, d.title || t('Question')), d.message ? h('p', null, d.message) : null,
    a.role === 'worker' ? h('p', { class: 'ask-hint' }, t('{name} has this question and answers it. You can answer here instead.', { name: a.yesman })) : null);
  const actions = h('div', { class: 'ask-actions' });
  if (d.method === 'confirm') {
    actions.append(
      h('button', { class: 'mds-button mds-button--primary mds-button--small', type: 'button', onclick: () => answer({ confirmed: true }) }, t('Yes')),
      h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => answer({ confirmed: false }) }, t('No')),
    );
  } else if (d.method === 'select') {
    for (const option of d.options ?? []) {
      actions.append(h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => answer({ value: option }) }, option));
    }
    actions.append(h('button', { class: 'mds-button mds-button--subtle mds-button--small', type: 'button', onclick: () => answer({ cancelled: true }) }, t('Cancel')));
  } else {
    const input = d.method === 'editor'
      ? h('textarea', { class: 'mds-textarea', rows: 5 })
      : h('input', { class: 'mds-input', placeholder: d.placeholder ?? '' });
    input.value = d.prefill ?? '';
    box.append(input);
    actions.append(
      h('button', { class: 'mds-button mds-button--primary mds-button--small', type: 'button', onclick: () => answer({ value: input.value }) }, t('Submit')),
      h('button', { class: 'mds-button mds-button--subtle mds-button--small', type: 'button', onclick: () => answer({ cancelled: true }) }, t('Cancel')),
    );
  }
  box.append(actions);
  return box;
}

function renderWelcome(body) {
  const input = h('input', { class: 'mds-input', placeholder: t('name, e.g. checkout-v2'), pattern: '[a-z0-9][a-z0-9\\-]{0,31}', required: true, spellcheck: 'false', 'aria-label': t('Name') });
  body.append(
    h('div', { class: 'page' },
      h('div', { class: 'welcome' },
        h('h1', { class: 'mds-title' }, t('Hire a yesman')),
        h('p', null, t('A yesman takes on a project. It gets a workspace of its own, checks out the repositories it needs from the shared pool, remembers what it learns, and starts workers when the job splits up.')),
        state.snap.piError ? h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null, h('strong', null, t('pi was not found')), h('p', null, state.snap.piError))) : null,
        h('form', { onsubmit: (e) => { e.preventDefault(); hire(input.value.trim()); } },
          input, h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, t('Hire'))),
      )),
  );
  input.focus();
}

// ---------- Settings ----------

// The pages, labelled in English: each is said in the page's language.
const SETTINGS = [
  { page: 'general', label: 'General', icon: 'sliders' },
  { page: 'repositories', label: 'Repositories', icon: 'branch' },
  { page: 'secrets', label: 'Secrets', icon: 'lock' },
  { page: 'variables', label: 'Variables', icon: 'code' },
  { page: 'storage', label: 'Storage', icon: 'folder' },
  { page: 'pi', label: 'pi', icon: 'package' },
  { page: 'config', label: 'Import and export', icon: 'transfer' },
  { page: 'about', label: 'About', icon: 'about' },
];

// The footer is built once so its submenu can slide open and closed, and
// again in a new language.
function buildSideFoot() {
  const nav = h('nav', { class: 'mds-subnav', id: 'settings-nav', 'aria-label': t('Settings') },
    SETTINGS.map((s) => h('button', { class: 'mds-nav-item', type: 'button', 'data-page': s.page, onclick: () => showSettings(s.page) },
      icon(s.icon), h('span', null, t(s.label)))));
  $('#side-foot').replaceChildren(h('div', { class: 'mds-nav-branch settings-branch' },
    h('div', { class: 'settings-drawer' }, h('div', { class: 'settings-clip' }, nav)),
    h('button', { class: 'mds-nav-item mds-nav-parent', id: 'settings-button', type: 'button', 'aria-controls': 'settings-nav', onclick: () => showSettings(state.settingsPage) },
      icon('gear'), h('span', null, t('Settings')))));
}

function renderSideFoot() {
  const open = state.view.kind === 'settings';
  $('.settings-branch').toggleAttribute('data-open', open);
  $('#settings-button').toggleAttribute('data-active', open);
  $('#settings-button').setAttribute('aria-expanded', String(open));
  $('#settings-nav').inert = !open;
  for (const item of document.querySelectorAll('#settings-nav .mds-nav-item')) {
    if (open && item.dataset.page === state.view.page) item.setAttribute('aria-current', 'page');
    else item.removeAttribute('aria-current');
  }
}

function showSettings(page) {
  state.settingsPage = page;
  state.view = { kind: 'settings', page };
  render();
  loadSettingsPage(page);
}

// Pages render what they last had at once and refresh it from the app.
async function loadSettingsPage(page) {
  if (page === 'storage') loadStorageUsage();
  try {
    if (page === 'general' || page === 'storage') state.settings = await call('Settings');
    else if (page === 'about') state.about = await call('About');
    else if (page === 'repositories') [state.repos, state.settings, state.fetches] = await Promise.all([call('Repos'), call('Settings'), call('Fetches')]);
    else if (page === 'pi') { await piPage.load(); return; }
    else if (page === 'secrets') { await secretsPage.load(); return; }
    else if (page === 'variables') { await variablesPage.load(); return; }
  } catch (err) {
    fail(err);
    return;
  }
  if (state.view.kind === 'settings' && state.view.page === page) renderBody();
}

function renderSettings(body) {
  const inner = h('div', { class: 'page-inner' });
  body.append(h('div', { class: 'page' }, inner));
  if (state.view.page === 'repositories') renderRepositories(inner);
  else if (state.view.page === 'storage') renderStorage(inner);
  else if (state.view.page === 'pi') piPage.render(inner);
  else if (state.view.page === 'secrets') secretsPage.render(inner);
  else if (state.view.page === 'variables') variablesPage.render(inner);
  else if (state.view.page === 'config') renderConfig(inner);
  else if (state.view.page === 'about') renderAbout(inner);
  else renderGeneral(inner);
}

function settingsSection(title, hint, ...rows) {
  return h('section', { class: 'settings-section' },
    h('div', { class: 'mds-section-heading' }, h('h2', null, title), hint ? h('p', null, hint) : null),
    h('div', { class: 'mds-group' }, rows));
}

function preferenceRow(label, hint, control) {
  return h('div', { class: 'mds-row mds-row--preference' },
    h('div', { class: 'mds-row-label' }, h('h3', null, label), hint),
    h('div', { class: 'mds-row-control row-end' }, control));
}

function revealButton(path, label) {
  return h('button', { class: 'mds-button mds-button--small', type: 'button', title: path, 'aria-label': label ? t('Reveal {name}', { name: label }) : null, onclick: () => call('Reveal', path).catch(fail) }, icon('folder'), t('Reveal'));
}

// choiceSelect is a select of choices, [value, label], that keeps the one
// chosen in v[key] and calls method with it, or goes back on failure. It
// keeps it first: a new language draws the page again before the call
// returns.
function choiceSelect(v, key, method, label, choices) {
  return h('select', { class: 'mds-select', 'aria-label': label, onchange: async (e) => {
    const select = e.currentTarget;
    const before = v[key];
    v[key] = select.value;
    try {
      await call(method, select.value);
    } catch (err) {
      fail(err);
      v[key] = before;
      if (select.isConnected) select.value = before;
      else if (state.view.kind === 'settings') renderBody();
    }
  } }, choices.map(([value, text]) => h('option', { value, selected: value === v[key] }, text)));
}

// Settings › pi, in pi.js.
const piPage = createPiPage({
  call,
  fail,
  confirm: (title, body, ok) => confirmAction(title, body, ok),
  agents: () => state.snap.agents,
  shown: () => state.view.kind === 'settings' && state.view.page === 'pi',
  rerender: () => renderBody(),
  ago: (iso, verb) => ago(iso, verb),
  preferenceRow,
  revealButton,
});

// Settings › Secrets and Settings › Variables, in env.js.
const envPage = (kind) => createEnvPage(kind, {
  call,
  fail,
  confirm: (title, body, ok) => confirmAction(title, body, ok),
  shown: () => state.view.kind === 'settings' && state.view.page === kind,
  rerender: () => renderBody(),
  preferenceRow,
  settingsSection,
  revealButton,
});
const secretsPage = envPage('secrets');
const variablesPage = envPage('variables');

function renderGeneral(inner) {
  const v = state.settings;
  if (!v) return;
  // A new language draws the page again, when the mygo:locale event says so.
  const language = choiceSelect(v, 'language', 'SetLanguage', t('Language'), [['system', t('Match the system')], ...LANGUAGES]);
  const appearance = choiceSelect(v, 'appearance', 'SetAppearance', t('Appearance'),
    [['system', t('Match the system')], ['light', t('Light')], ['dark', t('Dark')]]);
  inner.append(
    settingsSection(t('Appearance'), null,
      preferenceRow(t('Theme'), h('p', null, t('The window and its menus follow this.')), appearance),
      preferenceRow(t('Language'), h('p', null, t('The window, its menus and notifications use this. Match the system follows the languages of your system settings.')), language)),
  );
}

function renderStorage(inner) {
  const v = state.settings;
  if (!v) return;
  inner.append(
    settingsSection(t('Data folder'), t('Choose where yesman keeps its data.'), ...storageRows(v)),
    storageUsageSection(),
    settingsSection(t('Locations'), t('What yesman keeps, and where. Hover over a row for its full path.'),
      ...[
        ['Repositories', 'The shared pool of bare Git repositories.', v.pool],
        ['Workspaces', 'The parent folder for all yesman workspaces.', v.yesmen],
        ['Archive', 'Workspaces of dismissed yesmen, with their worktrees.', v.archive],
        ['Secrets', 'The secrets, encrypted, and the key that opens them.', v.secrets],
        ['Variables', 'The variables, in plain text.', v.variables],
        ['Runtime', 'Files yesman generates when it starts.', v.runtime],
        ['Settings file', 'Appearance and repository fetch preferences.', v.settingsFile],
        ['Storage configuration', 'Remembers the data folder across launches.', v.storageConfig],
      ].filter(([, , path]) => path).map(([label, hint, path]) => storagePathRow(t(label), t(hint), path))),
  );
}

// The locations Usage measures, in the order Locations lists them.
const STORAGE_PARTS = { repos: 'Repositories', yesmen: 'Workspaces', archive: 'Archive', runtime: 'Runtime', other: 'Other files' };

// loadStorageUsage measures the yesman folder, which can take a while, and
// draws the card again in place: a folder being typed keeps its focus.
async function loadStorageUsage(refresh = false) {
  const s = state.storageUsage;
  if (s.busy) return;
  s.busy = true;
  redrawStorageUsage();
  try {
    s.u = await call('StorageUsage', refresh);
    s.error = '';
  } catch (err) {
    s.error = errorText(err);
  } finally {
    s.busy = false;
    redrawStorageUsage();
  }
}

function redrawStorageUsage() {
  if (state.view.kind === 'settings' && state.view.page === 'storage') $('#storage-usage')?.replaceWith(storageUsageSection());
}

// The yesman folder's size, a bar of its locations, then each, and what
// is left on the disk.
function storageUsageSection() {
  const { u, busy, error } = state.storageUsage;
  const refresh = h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('Measure again'), 'aria-label': t('Measure again'), disabled: busy, 'aria-busy': busy ? 'true' : null, onclick: () => loadStorageUsage(true) },
    busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload'));
  let body;
  if (!u) {
    body = [h('div', { class: 'storage-usage-head' },
      error ? h('p', { class: 'danger', role: 'alert' }, error) : h('p', { class: 'storage-usage-meta' }, t('Measuring…')),
      h('div', { class: 'storage-usage-when' }, refresh))];
  } else {
    const whole = u.bytes || 1;
    const parts = u.parts.map((p) => ({ ...p, label: t(STORAGE_PARTS[p.kind] ?? p.kind) }));
    const share = (p) => {
      const pct = (p.bytes / whole) * 100;
      return p.bytes > 0 && pct < 1 ? '<1%' : `${Math.round(pct)}%`;
    };
    const files = tn(u.files, '{n} file', '{n} files');
    const meta = [u.partial ? t('At least, in {files}', { files }) : files];
    if (u.disk) meta.push(t('{free} free of {total} on this disk', { free: formatSize(u.disk.free), total: formatSize(u.disk.total) }));
    body = [
      h('div', { class: 'storage-usage-head' },
        h('div', null,
          h('p', { class: 'storage-usage-total' }, formatSize(u.bytes)),
          h('p', { class: 'storage-usage-meta' }, meta.join(' · '))),
        h('div', { class: 'storage-usage-when' }, h('span', null, ago(u.measuredAt, 'measured')), refresh)),
      error ? h('p', { class: 'danger', role: 'alert' }, error) : null,
      h('div', { class: 'storage-meter', role: 'img', 'aria-label': t('{size} in all', { size: formatSize(u.bytes) }) },
        parts.filter((p) => p.bytes > 0).map((p) => h('span', { 'data-tone': p.kind, style: `flex-grow: ${p.bytes / whole}`, title: `${p.label}: ${formatSize(p.bytes)}` }))),
      h('ul', { class: 'storage-legend', role: 'list' }, parts.map((p) => h('li', { title: `${p.label}\n${tn(p.files, '{n} file', '{n} files')}` },
        h('i', { class: 'storage-swatch', 'data-tone': p.kind, 'aria-hidden': 'true' }),
        h('span', { class: 'storage-legend-label' }, p.label),
        h('span', { class: 'storage-legend-size' }, formatSize(p.bytes)),
        h('span', { class: 'storage-legend-share' }, share(p))))),
    ];
  }
  const section = settingsSection(t('Usage'), t('The disk yesman’s data takes, by location.'),
    h('div', { class: 'storage-usage', 'aria-busy': String(busy) }, body));
  section.id = 'storage-usage';
  return section;
}

function storagePathRow(label, hint, path) {
  const row = preferenceRow(label, h('p', null, hint), revealButton(path, label));
  row.classList.add('storage-path-row');
  row.title = path;
  return row;
}

function storageRows(v) {
  const d = state.storageDraft;
  const change = h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: v.homeOverride || Boolean(d), onclick: () => {
    state.storageDraft = { path: '', error: '', busy: false };
    renderBody();
    $('#storage-path')?.focus();
  } }, t('Change…'));
  const current = preferenceRow(t('yesman folder'),
    h('div', null, h('p', { class: 'path', title: v.home }, v.home),
      v.homeOverride ? h('p', null, t('Set by YESMAN_HOME. Unset it and restart to change the folder here.')) : null),
    h('div', { class: 'storage-actions' }, revealButton(v.home), change));
  current.classList.add('storage-home-row');
  const rows = [current];
  if (d) {
    const input = h('input', { class: 'mds-input', id: 'storage-path', required: true, value: d.path,
      placeholder: t('Full path to a folder'), autocomplete: 'off', spellcheck: 'false',
      disabled: d.busy, 'aria-describedby': 'storage-hint storage-error', 'aria-invalid': d.error ? 'true' : null,
      oninput: (e) => { d.path = e.currentTarget.value; } });
    const browse = h('button', { class: 'mds-button', type: 'button', disabled: d.busy, onclick: async () => {
      browse.disabled = true;
      try {
        const path = await call('ChooseStorageFolder');
        if (path) { d.path = path; d.error = ''; }
      } catch (err) { d.error = errorText(err); }
      finally {
        if (state.view.kind === 'settings' && state.view.page === 'storage' && state.storageDraft === d) {
          renderBody();
          $('#storage-path')?.focus();
        }
      }
    } }, icon('folder'), t('Browse…'));
    rows.push(h('form', { class: 'storage-edit', 'aria-busy': String(d.busy), onsubmit: (e) => { e.preventDefault(); changeStorageFolder(d); } },
      h('label', { class: 'field', for: 'storage-path' }, t('New yesman folder')),
      h('div', { class: 'storage-input' }, input, browse),
      h('p', { class: 'mds-hint', id: 'storage-hint' }, t('Files are not moved. To keep your data, copy the current folder yourself before switching.')),
      h('p', { class: 'danger', id: 'storage-error', role: 'alert', hidden: !d.error }, d.error),
      h('div', { class: 'storage-actions' },
        h('button', { class: 'mds-button', type: 'button', disabled: d.busy, onclick: () => {
          state.storageDraft = null; renderBody();
          $('.storage-actions button:last-child')?.focus();
        } }, t('Cancel')),
        h('button', { class: 'mds-button mds-button--primary', type: 'submit', disabled: d.busy }, d.busy ? t('Please wait…') : t('Use folder and restart')))));
  }
  return rows;
}

async function changeStorageFolder(d) {
  if (d.busy) return;
  d.busy = true;
  d.error = '';
  renderBody();
  try {
    const destination = await call('CheckStorageFolder', d.path.trim());
    const active = state.snap.agents.filter((a) => ['working', 'starting', 'waiting'].includes(a.status)).length;
    const body = active
      ? tn(active, 'Restart yesman using {path}. The active agent stops; its session resumes when you next talk to it. Files are not copied. Save your edits and copy any data you want to use there first.',
        'Restart yesman using {path}. {n} active agents stop; their sessions resume when you next talk to them. Files are not copied. Save your edits and copy any data you want to use there first.', { path: destination })
      : t('Restart yesman using {path}. Files are not copied. Save your edits and copy any data you want to use there first.', { path: destination });
    if (!(await confirmAction(t('Use this yesman folder?'), body, t('Use folder and restart')))) return;
    await call('SetStorageFolder', destination);
  } catch (err) {
    d.error = errorText(err);
  } finally {
    d.busy = false;
    if (state.view.kind === 'settings' && state.view.page === 'storage') {
      renderBody();
      if (d.error) $('#storage-path')?.focus();
    }
  }
}

// What About says of updates, with the one action that fits.
function updateStatus() {
  const u = state.update;
  const faint = (text) => h('span', { class: 'about-faint' }, text);
  if (!u) return faint(t('Checking…'));
  if (!u.enabled) return faint(t('Release builds update themselves from GitHub. This build does not.'));
  const action = (label, onclick) => h('button', { class: 'mds-button mds-button--small', type: 'button', onclick }, label);
  const check = () => call('CheckForUpdates').catch(fail);
  switch (u.state) {
    case 'available': return h('span', { class: 'about-update' }, h('span', null, t('yesman {version} is available', { version: u.version })), action(t('Install'), installUpdate));
    case 'downloading': return faint(t('Installing {version}, {percent}%', { version: u.version, percent: Math.round((u.progress ?? 0) * 100) }));
    case 'ready': return h('span', { class: 'about-update' }, h('span', null, t('{version} is installed', { version: u.version })), action(t('Restart'), restartToUpdate));
    case 'failed': return h('span', { class: 'about-update' }, h('span', { class: 'danger' }, t('Installing {version} failed: {error}', { version: u.version, error: u.error })), action(t('Try again'), installUpdate));
    default:
      return h('span', { class: 'about-update' },
        u.checking ? faint(t('Checking…'))
          : u.checkError ? h('span', { class: 'danger' }, t('The last check failed: {error}', { error: u.checkError }))
          : h('span', null, u.checkedAt ? t('Up to date, {when}', { when: ago(u.checkedAt, 'checked') }) : t('Not checked yet')),
        u.checking ? null : action(t('Check now'), check));
  }
}

function renderAbout(inner) {
  const v = state.about;
  const row = (label, ...value) => h('div', { class: 'mds-row about-row' },
    h('div', { class: 'mds-row-label' }, h('label', null, label)),
    h('div', { class: 'mds-row-control about-value' }, value));
  const faint = (text) => h('span', { class: 'about-faint' }, text);
  const checking = () => faint(t('Checking…'));
  let build = checking();
  if (v) {
    const date = v.time ? new Date(v.time).toLocaleDateString(locale(), { year: 'numeric', month: 'short', day: 'numeric' }) : '';
    const detail = [v.revision ? v.revision.slice(0, 12) : '', date, v.modified ? t('with local changes') : ''].filter(Boolean).join(' · ');
    build = [v.version || t('Development build'), detail ? faint(` ${detail}`) : null];
  }
  inner.append(
    settingsSection('yesman', t('Hires yesmen: pi agents that each take on one project in a workspace of their own.'),
      row(t('Build'), build),
      row(t('Updates'), updateStatus()),
      row('pi', v ? (v.pi || h('span', { class: 'danger' }, v.piError)) : checking()),
      row('MyGo', v ? v.mygo || faint(t('unknown')) : checking()),
      row('Magpie', v ? v.magpie || faint(t('unknown')) : checking()),
      row('Go', v ? v.go : checking())),
    h('div', null, h('button', { class: 'mds-link-button', type: 'button', onclick: () => call('OpenURL', 'https://github.com/idy/yesman').catch(fail) }, t('Source on GitHub'))),
  );
}

// ---------- Import and export ----------

// A configuration file carries the settings and the repo pool. Importing it
// shows what it would change first, then merges it or replaces with it.

const THEMES = { system: 'Match the system', light: 'Light', dark: 'Dark' };
const themeLabel = (theme) => (THEMES[theme] ? t(THEMES[theme]) : theme);
const autoFetchLabel = (minutes) => {
  const label = AUTO_FETCH.find(([m]) => m === minutes)?.[1];
  return label ? t(label) : tn(minutes, 'Every minute', 'Every {n} minutes');
};
const repositories = (n) => tn(n, '{n} repository', '{n} repositories');
const fileName = (path) => path.split(/[\\/]/).pop();

function renderConfig(inner) {
  const c = state.config;
  const action = (label, onclick) => h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: c.busy, onclick }, label);
  const e = c.exported;
  const exportHint = e
    ? h('div', null,
        h('p', null, tNodes('Exported {repos} and the settings to {path}.', { repos: repositories(e.repos), path: h('span', { class: 'path', title: e.path }, e.path) }),
          e.skipped.length ? [' ', t('Left out, as they are not from GitHub: {names}.', { names: e.skipped.join(', ') })] : null),
        h('p', null, exportedEnv(e)))
    : h('p', null, t('The theme, the fetch schedule, every GitHub repository in the pool, the variables and the secrets with the password that opens them, as a TOML file. Keep it private.'));
  const i = c.imported;
  const importHint = i ? h('div', { class: 'config-imported' }, i.lines.map((line) => h('p', { class: line.danger ? 'danger' : null }, line.text)))
    : h('p', null, t('A file exported from yesman. You see what it changes, then merge it or replace with it.'));
  inner.append(
    settingsSection(t('Configuration'), t('Back up yesman’s settings, repo pool, variables and secrets, or carry them to another computer. Workspaces, their knowledge and conversations are not included.'),
      preferenceRow(t('Export'), exportHint, h('div', { class: 'storage-actions' }, e ? revealButton(e.path, t('the exported file')) : null, action(t('Export…'), exportConfig))),
      preferenceRow(t('Import'), importHint, action(t('Import…'), importConfig))));
}

async function exportConfig() {
  const c = state.config;
  c.busy = true;
  renderBody();
  try {
    const result = await call('ExportConfig');
    if (result.path) c.exported = result;
  } catch (err) {
    fail(err);
  } finally {
    c.busy = false;
    if (state.view.kind === 'settings' && state.view.page === 'config') renderBody();
  }
}

const variablesCount = (n) => tn(n, '{n} variable', '{n} variables');
const secretsCount = (n) => tn(n, '{n} secret', '{n} secrets');

// What an export says of the variables and secrets it carries.
function exportedEnv(e) {
  if (e.secrets == null) return t('It has {variables}, and no secrets, as none are set up.', { variables: variablesCount(e.variables) });
  return e.password
    ? t('It has {variables} and {secrets}, with the password that opens them: keep it private.', { variables: variablesCount(e.variables), secrets: secretsCount(e.secrets) })
    : t('It has {variables} and {secrets}, but not their password, which the keychain does not keep: you need it to open them after importing.', { variables: variablesCount(e.variables), secrets: secretsCount(e.secrets) });
}

async function importConfig() {
  const c = state.config;
  c.busy = true;
  renderBody();
  try {
    const path = await call('ChooseConfigFile');
    if (!path) return;
    const plan = await call('PreviewImport', path);
    const mode = await askImport(plan);
    if (mode !== 'merge' && mode !== 'replace') return;
    const result = await call('ImportConfig', path, mode);
    c.imported = { lines: importLines(plan, result) };
    state.settings = null; // the next General or Repositories page reads them again
    refreshState();
  } catch (err) {
    fail(err);
  } finally {
    c.busy = false;
    if (state.view.kind === 'settings' && state.view.page === 'config') renderBody();
  }
}

// What a plan changes in the settings, a line for each: merging and
// replacing differ only for a setting the file leaves out, which replacing
// resets.
function settingChanges(plan) {
  const fields = [['appearance', t('Theme'), themeLabel], ['autoFetch', t('Fetch automatically'), autoFetchLabel]];
  const lines = [];
  for (const [key, label, show] of fields) {
    const now = plan.current[key];
    if (plan.merge[key] !== now) lines.push({ text: t('{setting}: {from} → {to}', { setting: label, from: show(now), to: show(plan.merge[key]) }) });
    else if (plan.replace[key] !== now) lines.push({ text: t('{setting}: {from} → {to}', { setting: label, from: show(now), to: show(plan.replace[key]) }), replaceOnly: true });
  }
  return lines;
}

// askImport shows what importing would change and resolves to merge,
// replace or cancel.
function askImport(plan) {
  const dialog = $('#import-dialog');
  const part = (title, ...children) => h('section', { class: 'import-part' }, h('h3', null, title), children);
  const list = (items) => h('ul', { class: 'import-list' }, items);
  const settings = settingChanges(plan);
  const extra = plan.extra;
  const removable = extra.filter((r) => !r.inUse).length;
  const env = plan.env;
  const replaceDiffers = removable > 0 || settings.some((s) => s.replaceOnly)
    || Boolean(env.variables?.extra.length) || Boolean(env.secrets?.plan?.extra.length);
  dialog.replaceChildren(h('form', { method: 'dialog' },
    h('h2', { class: 'mds-heading', id: 'import-title' }, t('Import {file}', { file: fileName(plan.path) })),
    h('p', { class: 'dialog-hint path', title: plan.path }, plan.path),
    h('div', { class: 'import-body' },
      part(t('Settings'), settings.length
        ? list(settings.map((s) => h('li', null, s.text, s.replaceOnly ? h('span', { class: 'import-faint' }, ` · ${t('Replace only')}`) : null)))
        : h('p', null, t('Unchanged.'))),
      part(t('Repositories'),
        plan.add.length ? h('p', null, tNodes('Clones {repos}: {names}', { repos: repositories(plan.add.length), names: h('span', { class: 'import-names' }, plan.add.join(', ')) })) : null,
        h('p', null, plan.present.length
          ? tn(plan.present.length, '1 repository of the file is in the pool already.', '{n} repositories of the file are in the pool already.')
          : plan.add.length ? '' : t('The file lists none.'))),
      extra.length ? part(t('Only in this pool ({n})', { n: extra.length }),
        h('p', null, t('Merge keeps them. Replace removes them, except those that work depends on.')),
        list(extra.map((r) => h('li', null, h('span', { class: 'import-names', title: r.name }, r.label),
          h('span', { class: 'import-faint' }, ` · ${r.inUse ? t('kept: {reason}', { reason: r.inUse })
            : r.clone ? (r.clone === 'failed' ? t('failed to clone, stopped with Replace') : t('cloning, stopped with Replace'))
            : t('removed with Replace')}`))))) : null,
      ...envParts(plan.env, part),
      plan.ignored.length ? h('p', { class: 'import-faint' }, t('Ignored, as this yesman does not know them: {keys}', { keys: plan.ignored.join(', ') })) : null),
    h('div', { class: 'mds-dialog-actions' },
      h('button', { class: 'mds-button', value: 'cancel' }, t('Cancel')),
      replaceDiffers && !env.replaceClash.length ? h('button', { class: 'mds-button', value: 'replace', title: t('Also reset the settings the file leaves out and remove the repositories, variables and secrets it does not list') }, t('Replace')) : null,
      env.clash.length ? null : h('button', { class: 'mds-button mds-button--primary', value: 'merge' }, t('Merge')))));
  const answer = openDialog(dialog);
  (dialog.querySelector('.mds-button--primary') ?? dialog.querySelector('.mds-button'))?.focus();
  return answer;
}

// envParts say what importing does to the variables and secrets, a part
// for each the file has.
function envParts(env, part) {
  const names = (list) => h('span', { class: 'import-names mds-mono' }, list.join(', '));
  const changes = (p) => [
    p.add.length ? h('p', null, tNodes('Adds {names}', { names: names(p.add) })) : null,
    p.change.length ? h('p', null, tNodes('Changes {names}', { names: names(p.change) })) : null,
    p.extra.length ? h('p', null, tNodes('Only here: {names}. Merge keeps them; Replace removes them.', { names: names(p.extra) })) : null,
    !p.add.length && !p.change.length && !p.extra.length ? h('p', null, t('Unchanged.')) : null,
  ];
  const parts = [];
  if (env.variables) parts.push(part(t('Variables'), changes(env.variables)));
  const s = env.secrets;
  if (s) {
    parts.push(part(t('Secrets'),
      s.skipped ? h('p', { class: 'danger' }, t('Left out: {reason}.', { reason: s.skipped }))
        : s.restore ? h('p', null, s.password
          ? t('Sets up {secrets} from the file, and keeps their password in the keychain.', { secrets: secretsCount(s.names.length) })
          : t('Sets up {secrets} from the file, locked: the file has no password, so enter it in Settings › Secrets.', { secrets: secretsCount(s.names.length) }))
          : changes(s.plan)));
  }
  const clash = env.clash.length ? env.clash : env.replaceClash;
  if (clash.length) {
    parts.push(h('p', { class: 'danger' }, env.clash.length
      ? t('Variables and secrets would both set {names}. Rename one, then import again.', { names: clash.join(', ') })
      : t('Replace would have variables and secrets both set {names}, so only Merge is offered.', { names: clash.join(', ') })));
  }
  return parts;
}

// What an import did, in lines for the Import row.
function importLines(plan, result) {
  const parts = [];
  if (settingChanges(plan).some((s) => result.mode === 'replace' || !s.replaceOnly)) parts.push(t('settings changed'));
  if (result.cloning.length) parts.push(tn(result.cloning.length, '1 repository cloning', '{n} repositories cloning'));
  if (result.removed.length) parts.push(t('{n} removed', { n: result.removed.length }));
  if (result.stopped.length) parts.push(tn(result.stopped.length, '1 clone stopped', '{n} clones stopped'));
  if (result.env.variables != null) parts.push(t('{variables} now', { variables: variablesCount(result.env.variables) }));
  if (result.env.secrets != null) parts.push(result.env.restored
    ? (result.env.locked ? t('{secrets} set up, locked', { secrets: secretsCount(result.env.secrets) }) : t('{secrets} set up', { secrets: secretsCount(result.env.secrets) }))
    : t('{secrets} now', { secrets: secretsCount(result.env.secrets) }));
  const changes = { file: fileName(plan.path), changes: parts.length ? parts.join(' · ') : t('nothing to change') };
  const lines = [{ text: result.mode === 'replace' ? t('Replaced with {file}: {changes}.', changes) : t('Merged {file}: {changes}.', changes) }];
  for (const r of result.kept) lines.push({ text: t('Kept {name}: {reason}.', { name: r.label, reason: r.inUse }), danger: Boolean(r.failed) });
  if (result.env.skipped) lines.push({ text: t('Secrets left out: {reason}.', { reason: result.env.skipped }), danger: true });
  return lines;
}

// ---------- Repositories ----------

// What ago says of a time, by what happened then.
const AGO = {
  fetched: ['never fetched', 'fetched just now', 'fetched {n} min ago', 'fetched {n} h ago', 'fetched {n} d ago'],
  checked: ['never checked', 'checked just now', 'checked {n} min ago', 'checked {n} h ago', 'checked {n} d ago'],
  published: ['never published', 'published just now', 'published {n} min ago', 'published {n} h ago', 'published {n} d ago'],
  measured: ['never measured', 'measured just now', 'measured {n} min ago', 'measured {n} h ago', 'measured {n} d ago'],
};

function ago(iso, what = 'fetched') {
  const [never, now, minutes, hours, days] = AGO[what];
  if (!iso) return t(never);
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return t(now);
  if (s < 3600) return t(minutes, { n: Math.round(s / 60) });
  if (s < 86400) return t(hours, { n: Math.round(s / 3600) });
  return t(days, { n: Math.round(s / 86400) });
}

// owner/repo for a GitHub URL, which is all a pool repository can be;
// repositories added before yesman took only GitHub keep their URL.
const githubName = (url) => /github\.com[:/]([^/]+\/[^/]+?)(?:\.git)?\/?$/.exec(url ?? '')?.[1] ?? '';

// The key of a GitHub repository, owner/repo, which GitHub matches in any case.
const repoKey = (fullName) => fullName.toLowerCase();

function renderRepositories(inner) {
  const search = state.repos.length + state.clones.length > 1
    ? h('label', { class: 'search' }, icon('search'),
        h('input', { class: 'mds-input', id: 'repo-search', type: 'search', placeholder: t('Find a repository'), 'aria-label': t('Find a repository'), spellcheck: 'false', value: state.repoQuery,
          oninput: (e) => { state.repoQuery = e.currentTarget.value; renderRepoList(); } }))
    : null;
  inner.append(
    h('div', { class: 'mds-toolbar' },
      h('p', { class: 'toolbar-lead' }, t('GitHub repositories the yesmen check worktrees out of. Only yesman writes here. It fetches a repository whenever one is checked out, and all of them when you ask or on a schedule.')),
      h('div', { class: 'mds-toolbar-actions' }, search,
        h('button', { class: 'mds-button mds-button--small fetch-all', id: 'fetch-all', type: 'button', onclick: () => call('FetchAll').catch(fail) }),
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('Reveal the repo pool'), 'aria-label': t('Reveal the repo pool'), onclick: () => call('Reveal', state.snap.pool).catch(fail) }, icon('folder')))),
    h('div', { class: 'card-list', id: 'repo-list', role: 'list', 'aria-label': t('Repositories') }),
    h('div', null, h('button', { class: 'mds-link-button add-link', type: 'button', onclick: openAddRepos }, icon('plus'), t('Add repositories'))),
    autoFetchSection());
  renderRepoList();
}

const AUTO_FETCH = [[0, 'Never'], [1, 'Every minute'], [5, 'Every 5 minutes'], [15, 'Every 15 minutes'], [60, 'Every hour']];

function autoFetchSection() {
  const v = state.settings;
  if (!v) return null;
  const select = h('select', { class: 'mds-select', 'aria-label': t('Fetch automatically'), onchange: async (e) => {
    const el = e.currentTarget;
    try {
      await call('SetAutoFetch', Number(el.value));
      v.autoFetch = Number(el.value);
    } catch (err) {
      fail(err);
      el.value = String(v.autoFetch);
    }
  } }, AUTO_FETCH.map(([value, label]) => h('option', { value, selected: value === v.autoFetch }, t(label))));
  return settingsSection(t('Fetching'), null,
    preferenceRow(t('Fetch automatically'), h('p', null, t('Fetches every repository in the pool while yesman runs, in the tray too.')), select));
}

// Fetch all spins while any repository fetches, and waits while all do.
function renderFetchAll() {
  const button = $('#fetch-all');
  if (!button) return;
  const busy = state.fetches.fetching.length > 0;
  button.disabled = !state.repos.length || state.repos.every((r) => state.fetches.fetching.includes(r.name));
  button.replaceChildren(busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload'), t('Fetch all'));
}

const cloneStatus = (c) => (c.state === 'queued' ? t('waiting to clone') : t('cloning…'));

// A pool repository; clone is set while it is being added again, as an
// agent may.
function repoRow(r, clone) {
  const worktrees = state.snap.yesmen.flatMap((y) => y.worktrees.filter((w) => w.repo === r.name).map((w) => ({ yesman: y.name, dir: w.dir || w.path })));
  const users = [...new Set(worktrees.map((w) => w.yesman))];
  const fetching = state.fetches.fetching.includes(r.name);
  const busy = fetching || !!clone;
  const name = githubName(r.url);
  // The last fetch's error stays, in place of when it was fetched, until
  // a fetch succeeds.
  const error = !busy && state.fetches.errors[r.name];
  const status = clone ? cloneStatus(clone) : fetching ? t('fetching…') : error ? null : ago(r.fetchedAt);
  const meta = [name ? null : r.url, r.upstream ? t('fork of {name}', { name: githubName(r.upstream) || r.upstream }) : null, r.defaultBranch ? t('default {branch}', { branch: r.defaultBranch }) : null, status].filter(Boolean).join(' · ');
  return h('div', { class: 'card-row', role: 'listitem' },
    h('span', { class: 'card-row-icon' }, icon('branch')),
    h('div', { class: 'card-row-text' }, h('strong', { title: r.name }, name || r.name),
      h('span', { title: error || null }, meta, error ? [meta ? ' · ' : null, h('span', { class: 'danger' }, t('last fetch failed: {error}', { error }))] : null)),
    users.length
      ? h('span', { class: 'card-row-aside', title: worktrees.map((w) => `${w.yesman}: ${w.dir}`).join('\n') },
          `${tn(worktrees.length, '1 worktree', '{n} worktrees')} · ${users.join(', ')}`)
      : null,
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('Fetch {name}', { name: r.name }), 'aria-label': t('Fetch {name}', { name: r.name }), disabled: busy, onclick: () => fetchRepo(r.name) },
      busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload')));
}

// A repository on its way into the pool: queued, cloning, or stopped by
// an error until it is tried again or dismissed.
function cloneRow(c) {
  const failed = c.state === 'failed';
  const button = (label, glyph, onclick) =>
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick }, icon(glyph));
  return h('div', { class: 'card-row', role: 'listitem', 'data-clone': c.state },
    h('span', { class: 'card-row-icon' }, icon('branch')),
    h('div', { class: 'card-row-text' },
      h('strong', { title: `github.com/${c.repo}` }, c.repo),
      failed ? h('span', { class: 'danger', title: c.error }, t('Could not clone: {error}', { error: c.error })) : h('span', null, cloneStatus(c))),
    failed
      ? h('span', { class: 'card-row-actions' },
          button(t('Try {name} again', { name: c.repo }), 'reload', () => call('RetryClone', c.repo).catch(fail)),
          button(t('Dismiss {name}', { name: c.repo }), 'x', () => call('DismissClone', c.repo).catch(fail)))
      : h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', disabled: true, title: cloningLabel(c), 'aria-label': cloningLabel(c) },
          h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })));
}

const cloningLabel = (c) => (c.state === 'queued' ? t('{name} is waiting to clone', { name: c.repo }) : t('{name} is cloning', { name: c.repo }));

function renderRepoList() {
  const list = $('#repo-list');
  if (!list) return;
  renderFetchAll();
  const adding = new Map(state.clones.filter((c) => c.state !== 'failed').map((c) => [repoKey(c.repo), c]));
  const inPool = new Set();
  const rows = state.repos.map((r) => {
    const name = githubName(r.url);
    if (name) inPool.add(repoKey(name));
    return { key: repoKey(name || r.name), text: `${r.name} ${r.url} ${r.upstream ?? ''}`, row: () => repoRow(r, name && adding.get(repoKey(name))) };
  });
  for (const c of state.clones) {
    if (c.state !== 'failed' && inPool.has(repoKey(c.repo))) continue;
    rows.push({ key: repoKey(c.repo), text: c.repo, row: () => cloneRow(c) });
  }
  rows.sort((a, b) => a.key.localeCompare(b.key));
  const q = state.repoQuery.trim().toLowerCase();
  const shown = q ? rows.filter((r) => r.text.toLowerCase().includes(q)) : rows;
  if (!rows.length) {
    list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, t('No repositories yet')), h('span', null, t('Add some from GitHub, or let a yesman add the ones it needs.'))));
  } else if (!shown.length) {
    list.replaceChildren(h('div', { class: 'card-empty' }, h('span', null, t('No repository matches “{query}”.', { query: state.repoQuery.trim() }))));
  } else {
    list.replaceChildren(...shown.map((r) => r.row()));
  }
}

// The fetches event shows the repository fetching, and its error in its
// row if the fetch fails.
function fetchRepo(name) {
  call('FetchRepo', name).catch(() => {});
}

// A repository whose fetch ended is read again for its fetch time, while
// the repositories page shows. The newest state wins.
let fetchesSeq = 0;
async function onFetches(next) {
  const seq = ++fetchesSeq;
  const ended = state.fetches.fetching.some((name) => !next.fetching.includes(name));
  if (ended && $('#repo-list')) {
    const repos = await call('Repos').catch(() => null);
    if (repos) state.repos = repos;
  }
  if (seq !== fetchesSeq) return;
  state.fetches = next;
  renderRepoList();
}

// A clone that finished leaves the list once the pool lists it, so its
// row turns into the repository's without a gap. The newest list wins.
let clonesSeq = 0;
async function onClones(list) {
  const seq = ++clonesSeq;
  const now = new Set(list.map((c) => repoKey(c.repo)));
  if (state.clones.some((c) => !now.has(repoKey(c.repo)))) {
    const repos = await call('Repos').catch(() => null);
    if (repos) state.repos = repos;
  }
  if (seq !== clonesSeq) return;
  state.clones = list;
  renderRepoList();
}

// ---------- Adding repositories ----------

// The add dialog takes an owner, a user or an organization, and lists its
// repositories to tick; what follows a slash filters them. owner/repo or a
// GitHub URL adds that repository even when nothing is ticked. Adding
// returns at once: the repositories clone in the background, in the list.
const OWNER_RE = /^[A-Za-z0-9][A-Za-z0-9-]{0,38}$/;
const NAME_RE = /^[A-Za-z0-9._-]{1,100}$/;

const picker = {
  lists: new Map(), // owner key -> { owner, state: 'loading' | 'ready' | 'failed', repos, error }
  selected: new Map(), // repo key -> owner/repo
  timer: 0,
  error: '',
};

function readPickerInput() {
  const raw = $('#picker-input').value.trim();
  const path = raw.replace(/^git@github\.com:/i, '').replace(/^(?:[a-z+]+:\/\/)?(?:[^@/]+@)?(?:www\.)?github\.com\//i, '');
  const [owner = '', rest = ''] = path.split('/');
  const name = rest.replace(/\.git$/i, '');
  const valid = OWNER_RE.test(owner);
  const full = valid && NAME_RE.test(name) && name !== '.' && name !== '..' ? `${owner}/${name}` : '';
  // A URL goes as typed, since it may name the protocol to clone over.
  return { raw, owner: valid ? owner : '', name, full, input: full && /[:@]/.test(raw) ? raw : full };
}

// Where each GitHub repository the page knows of stands.
function pickerTaken() {
  const taken = new Map();
  for (const r of state.repos) {
    const name = githubName(r.url);
    if (name) taken.set(repoKey(name), t('In the pool'));
  }
  for (const c of state.clones) {
    if (c.state !== 'failed' && !taken.has(repoKey(c.repo))) taken.set(repoKey(c.repo), t('Cloning'));
  }
  return taken;
}

function buildRepoDialog() {
  const input = h('input', { class: 'mds-input', id: 'picker-input', placeholder: t('owner, or owner/repo'), 'aria-label': t('GitHub user, organization or repository'),
    spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', 'aria-describedby': 'picker-error',
    oninput: onPickerInput,
    onkeydown: (e) => {
      // Enter lists an owner not listed yet, or lists it again after an
      // error, before it adds anything.
      if (e.key !== 'Enter') return;
      const { owner } = readPickerInput();
      const entry = owner && picker.lists.get(owner.toLowerCase());
      if (owner && (!entry || entry.state === 'failed')) {
        e.preventDefault();
        listOwner(owner, true);
      }
    } });
  $('#repo-dialog').replaceChildren(h('form', { id: 'repo-form', onsubmit: submitPicker },
    h('h2', { class: 'mds-heading', id: 'repo-dialog-title' }, t('Add repositories')),
    h('p', { class: 'dialog-hint' }, t('Name a GitHub user or organization to pick from its repositories, or owner/repo to add one. They clone in the background; for a fork, yesman also fetches the repository it was forked from.')),
    h('label', { class: 'search picker-search' }, icon('search'), input, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })),
    h('div', { class: 'picker-results', id: 'picker-results' }),
    h('div', { class: 'mds-dialog-actions picker-actions' },
      h('p', { class: 'picker-status', id: 'picker-error', 'aria-live': 'polite' }),
      h('button', { class: 'mds-button', type: 'button', onclick: () => $('#repo-dialog').close('cancel') }, t('Cancel')),
      h('button', { class: 'mds-button mds-button--primary', type: 'submit', id: 'picker-add' }, t('Add')))));
}

function openAddRepos() {
  clearTimeout(picker.timer);
  picker.lists.clear();
  picker.selected.clear();
  picker.error = '';
  $('#picker-input').value = '';
  renderPicker();
  openDialog($('#repo-dialog'));
  $('#picker-input').focus();
}

function onPickerInput() {
  picker.error = '';
  clearTimeout(picker.timer);
  const { owner } = readPickerInput();
  if (owner && !picker.lists.has(owner.toLowerCase())) picker.timer = setTimeout(() => listOwner(owner), 400);
  renderPicker();
}

async function listOwner(owner, again = false) {
  const key = owner.toLowerCase();
  clearTimeout(picker.timer);
  const had = picker.lists.get(key);
  if (had && !(again && had.state === 'failed')) return;
  const entry = { owner, state: 'loading', repos: [], error: '' };
  picker.lists.set(key, entry);
  renderPicker();
  try {
    entry.repos = await call('OwnerRepos', owner);
    entry.state = 'ready';
  } catch (err) {
    entry.state = 'failed';
    entry.error = errorText(err);
  }
  if (picker.lists.get(key) === entry && $('#repo-dialog').open) renderPicker();
}

// The owner's listed repository a typed one names, in GitHub's spelling.
function pickerListed(typed) {
  const entry = typed.full && picker.lists.get(typed.owner.toLowerCase());
  if (entry?.state !== 'ready') return undefined;
  return entry.repos.find((r) => repoKey(r.fullName) === repoKey(typed.full)) ?? null;
}

// What Add would add: the ticked repositories, or else the one typed,
// unless it is in the pool. One the owner's list leaves out is still
// tried: the list may not show every repository the user can clone (the
// public API lists no private ones, and a list stops at 1,000).
function pickerChoice(typed = readPickerInput()) {
  if (picker.selected.size) return [...picker.selected.values()];
  if (!typed.full || pickerTaken().has(repoKey(typed.full))) return [];
  const listed = pickerListed(typed);
  return [listed && typed.input === typed.full ? listed.fullName : typed.input];
}

function pickerNote(...children) {
  return h('div', { class: 'picker-note' }, children);
}

// Loading keeps the dialog still: the first list loads into placeholder
// rows of the same size, and a list already shown stays, dimmed, until
// the next owner's replaces it.
function renderPicker() {
  const results = $('#picker-results');
  const typed = readPickerInput();
  const entry = typed.owner && picker.lists.get(typed.owner.toLowerCase());
  const loading = !!typed.owner && (!entry || entry.state === 'loading');
  $('.picker-search').toggleAttribute('data-busy', loading);
  results.setAttribute('aria-busy', String(loading));
  const note = (...children) => {
    results.dataset.shows = '';
    results.replaceChildren(pickerNote(...children));
  };
  if (loading) {
    if (!results.querySelector('.picker-list, .picker-skeleton')) {
      results.dataset.shows = '';
      results.replaceChildren(pickerSkeleton());
    }
  } else if (!typed.owner) {
    note(typed.raw
      ? t('“{input}” is not a GitHub user, organization or repository.', { input: typed.raw })
      : t('Type a GitHub user or organization to list its repositories.'));
  } else if (entry.state === 'failed') {
    note(h('span', { class: 'danger' }, entry.error), h('span', null, t('Press Enter to try again.')));
  } else {
    const q = typed.name.toLowerCase();
    const shown = q ? entry.repos.filter((r) => r.fullName.slice(r.fullName.indexOf('/') + 1).toLowerCase().includes(q)) : entry.repos;
    if (!entry.repos.length) note(t('{owner} has no repositories you can see.', { owner: entry.owner }));
    else if (!shown.length) note(t('No repository of {owner} matches “{query}”.', { owner: entry.owner, query: typed.name }));
    else {
      // A new owner's list fades in; filtering it redraws it in place.
      const shows = `${entry.owner.toLowerCase()}/${q}`;
      const fresh = !results.dataset.shows?.startsWith(`${entry.owner.toLowerCase()}/`);
      if (shows !== results.dataset.shows) results.scrollTop = 0;
      results.dataset.shows = shows;
      results.replaceChildren(h('div', { class: 'picker-list', 'data-fresh': fresh ? 'true' : null }, pickerRows(entry, shown)));
    }
  }
  renderPickerFoot(typed);
}

function pickerSkeleton() {
  // Bones sit in the text a row has, so a placeholder row is as tall as a
  // real one with a description.
  const bone = () => h('i', { class: 'picker-bone' });
  const row = (head) => h('div', { class: head ? 'picker-row picker-all' : 'picker-row' }, h('i', { class: 'picker-bone-box' }),
    head ? h('span', null, bone()) : h('span', { class: 'picker-row-text' }, h('strong', null, bone()), h('span', null, bone())));
  return h('div', { class: 'picker-skeleton', 'aria-hidden': 'true' }, row(true), Array.from({ length: 7 }, () => row(false)));
}

function pickerRows(entry, shown) {
  const taken = pickerTaken();
  const free = shown.filter((r) => !taken.has(repoKey(r.fullName)));
  const all = h('input', { type: 'checkbox', id: 'picker-all', disabled: !free.length, onchange: (e) => {
    for (const r of free) {
      if (e.currentTarget.checked) picker.selected.set(repoKey(r.fullName), r.fullName);
      else picker.selected.delete(repoKey(r.fullName));
    }
    syncPicker();
  } });
  all.free = free;
  // Ticks kept from another filter or owner show here too.
  const on = free.filter((r) => picker.selected.has(repoKey(r.fullName))).length;
  all.checked = on > 0 && on === free.length;
  all.indeterminate = on > 0 && on < free.length;
  const count = shown.length === entry.repos.length ? repositories(shown.length) : t('{shown} of {total}', { shown: shown.length, total: entry.repos.length });
  const head = h('label', { class: 'picker-row picker-all' }, all, h('span', null, t('Select all · {count}', { count })));
  const rows = shown.map((r) => {
    const key = repoKey(r.fullName);
    const status = taken.get(key);
    const box = h('input', { type: 'checkbox', 'data-key': key, disabled: !!status, onchange: (e) => {
      if (e.currentTarget.checked) picker.selected.set(key, r.fullName);
      else picker.selected.delete(key);
      syncPicker();
    } });
    box.checked = !!status || picker.selected.has(key);
    const tags = status ?? [r.private && t('Private'), r.fork && t('Fork'), r.archived && t('Archived')].filter(Boolean).join(' · ');
    return h('label', { class: 'picker-row', 'data-taken': status ? 'true' : null }, box,
      h('span', { class: 'picker-row-text' },
        h('strong', { title: r.fullName }, r.fullName.slice(r.fullName.indexOf('/') + 1)),
        r.description ? h('span', { title: r.description }, r.description) : null),
      tags ? h('span', { class: 'picker-row-aside' }, tags) : null);
  });
  return [head, ...rows];
}

// Ticking updates the boxes in place, so focus and scroll stay put.
function syncPicker() {
  for (const box of document.querySelectorAll('#picker-results input[data-key]')) {
    if (!box.disabled) box.checked = picker.selected.has(box.dataset.key);
  }
  const all = $('#picker-all');
  if (all) {
    const on = all.free.filter((r) => picker.selected.has(repoKey(r.fullName))).length;
    all.checked = on > 0 && on === all.free.length;
    all.indeterminate = on > 0 && on < all.free.length;
  }
  renderPickerFoot();
}

function renderPickerFoot(typed = readPickerInput()) {
  const choice = pickerChoice(typed);
  const n = picker.selected.size;
  const add = $('#picker-add');
  add.disabled = !choice.length;
  add.textContent = n ? tn(n, 'Add 1 repository', 'Add {n} repositories') : choice.length ? t('Add {name}', { name: typed.full }) : t('Add');
  const status = $('#picker-error');
  status.dataset.tone = picker.error ? 'error' : '';
  status.title = picker.error;
  const entry = typed.owner && picker.lists.get(typed.owner.toLowerCase());
  const loading = typed.owner && (!entry || entry.state === 'loading');
  status.textContent = picker.error
    || (n ? t('{n} selected', { n })
      : typed.full && pickerTaken().has(repoKey(typed.full)) ? t('{name} is already in the pool.', { name: typed.full })
      : pickerListed(typed) === null ? t('Not in {owner}’s list; Add tries it anyway.', { owner: typed.owner })
      : loading ? t('Listing {owner}’s repositories…', { owner: typed.owner }) : '');
}

async function submitPicker(e) {
  e.preventDefault();
  const repos = pickerChoice();
  if (!repos.length) return;
  const add = $('#picker-add');
  add.disabled = true;
  try {
    state.clones = await call('AddRepos', repos);
    $('#repo-dialog').close('add');
    renderRepoList();
  } catch (err) {
    picker.error = errorText(err);
    renderPickerFoot();
  }
}

// ---------- Files pane ----------

// The pane shows the workspace of the yesman whose agent is shown.
const files = createFiles({
  call,
  fail,
  yesman: () => {
    const a = selectedAgent();
    return a ? yesmanOf(a.id) : null;
  },
  isOpen: () => state.pane === 'files',
  setOpen: (on) => setPane(on ? 'files' : state.pane === 'files' ? null : state.pane),
  checkout: (y) => openCheckout(y),
  dismiss: (name) => dismiss(name),
  confirm: (title, body, ok) => confirmAction(title, body, ok),
  shown: () => info.render(),
  worktrees: (y) => info.worktrees(y),
});

// ---------- Info panel ----------

// The panel shows the yesman whose agent is shown, and opens its parts in
// the files pane.
const info = createInfo({
  call,
  fail,
  yesman: () => {
    const a = selectedAgent();
    return a ? yesmanOf(a.id) : null;
  },
  isOpen: () => state.panelOpen,
  files: () => files.shown(),
  openKnowledge: () => files.openKnowledge(),
  openWorktree: (dir, show) => files.openWorktree(dir, show),
  checkout: (y) => openCheckout(y),
  openURL: (url) => call('OpenURL', url).catch(fail),
});

function togglePanel() {
  state.panelOpen = !state.panelOpen;
  localStorage.setItem('yesman.info', state.panelOpen ? 'shown' : 'hidden');
  info.render();
  renderBarActions();
}

// ---------- Board pane ----------

// The board shares the place beside the conversation with the files.
const board = createBoard({
  call,
  yesman: () => {
    const a = selectedAgent();
    return a ? yesmanOf(a.id) : null;
  },
  isOpen: () => state.pane === 'board',
  setOpen: (on) => setPane(on ? 'board' : state.pane === 'board' ? null : state.pane),
  agent: (id) => agentById(id),
  openAgent: (id) => selectAgent(id),
});

function setPane(pane) {
  state.pane = pane;
  renderInfo();
  renderBarActions();
}

function togglePane(pane) {
  setPane(state.pane === pane ? null : pane);
}

function renderInfo() {
  files.render();
  board.render();
}

async function openCheckout(y) {
  try {
    state.repos = await call('Repos');
  } catch (err) {
    fail(err);
    return;
  }
  if (!state.repos.length) {
    toast(t('Add a repository to the pool first.'));
    showSettings('repositories');
    return;
  }
  const form = $('#checkout-form');
  form.reset();
  form.elements.repo.replaceChildren(...state.repos.map((r) => h('option', { value: r.name }, r.name)));
  form.elements.branch.placeholder = `yesman/${y.name}/…`;
  if ((await openDialog($('#checkout-dialog'))) !== 'checkout') return;
  try {
    const wt = await call('Checkout', y.name, { repo: form.elements.repo.value, branch: form.elements.branch.value.trim() });
    toast(t('Checked out {path}', { path: wt.dir || wt.path }));
  } catch (err) {
    fail(err);
  }
}

// ---------- Actions ----------

function selectAgent(id) {
  state.view = { kind: 'agent', id };
  const a = agentById(id);
  if (!a) {
    render();
    return;
  }
  loadModelOptions(a);
  const c = convo(a);
  if (!c.loaded && !c.loading) {
    c.loading = loadConvo(c).finally(() => { c.loading = null; });
  }
  render();
}

async function hire(name) {
  if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(name)) {
    toast(t('Use 1-32 lowercase letters, digits and dashes.'));
    return;
  }
  try {
    const y = await call('Hire', name);
    await refreshState();
    selectAgent(`${y.name}/lead`);
  } catch (err) {
    fail(err);
  }
}

async function confirmAction(title, body, ok) {
  const dialog = $('#confirm-dialog');
  $('#confirm-title').textContent = title;
  $('#confirm-body').textContent = body;
  $('#confirm-ok').textContent = ok;
  return (await openDialog(dialog)) === 'ok';
}

async function dismiss(name) {
  if (!(await confirmAction(t('Dismiss {name}?', { name }), t('Its agents stop and its workspace moves to the archive. Branches stay in the repo pool.'), t('Dismiss')))) return;
  try {
    const dest = await call('Dismiss', name);
    toast(t('Archived to {path}', { path: dest }));
    // A view of its agents falls back to the first yesman once they are gone.
    await refreshState();
  } catch (err) {
    fail(err);
  }
}

async function removeWorker(a) {
  if (!(await confirmAction(t('Remove {name}?', { name: a.name }), t('It stops and leaves this yesman. Its worktrees and session log stay in the workspace.'), t('Remove')))) return;
  try {
    await call('RemoveWorker', a.id);
    // selectAgent loads the lead's conversation if this window has not yet.
    if (state.view.kind === 'agent' && state.view.id === a.id) selectAgent(`${a.yesman}/lead`);
    else render();
  } catch (err) {
    fail(err);
  }
}

// ---------- State ----------

let refreshTimer = null;
function refreshSoon() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(refreshState, 150);
}

async function refreshState() {
  try {
    [state.snap, state.repos, state.clones] = await Promise.all([call('State'), call('Repos'), call('Clones')]);
  } catch (err) {
    fail(err);
    return;
  }
  pruneCaches();
  if (state.view.kind === 'agent' && !agentById(state.view.id)) state.view = { kind: 'none' };
  if (state.view.kind === 'none' && state.snap.yesmen.length) {
    state.view = { kind: 'agent', id: `${state.snap.yesmen[0].name}/lead` };
    selectAgent(state.view.id);
    return;
  }
  render();
}

function onAgents(agents) {
  const before = selectedAgent();
  const ids = state.snap.agents.map((a) => a.id).join();
  state.snap.agents = agents;
  pruneCaches();
  renderSidebar();
  // The board links a task's owner while it is one of the agents.
  if (state.pane === 'board' && agents.map((a) => a.id).join() !== ids) board.render();
  piPage.onAgents();
  const after = selectedAgent();
  if (after?.status === 'working' && after.activity !== before?.activity) {
    const label = $('#thread .working-live .working-label');
    if (label) label.textContent = workingLabel(after);
  }
  if (after?.status === 'idle' && before?.status === 'starting') loadModelOptions(after);
  if (state.view.kind === 'agent' && !after) {
    refreshSoon();
    return;
  }
  if (after && (before?.status !== after.status || before?.model !== after.model || before?.provider !== after.provider || before?.effort !== after.effort || JSON.stringify(before?.effortLevels) !== JSON.stringify(after.effortLevels) || before?.detail !== after.detail || JSON.stringify(before?.dialog) !== JSON.stringify(after.dialog))) {
    renderBar();
    renderDock();
    renderWorking();
    if (before?.status === 'stopped' && after.status !== 'stopped' && !convo(after).loaded) selectAgent(after.id);
  }
}

function render() {
  $('#app').dataset.view = state.view.kind;
  renderSidebar();
  renderBar();
  renderBody();
  renderInfo();
}

// onLocale draws the page again in the language the app shows now.
function onLocale(tag) {
  setLocale(tag);
  translatePage();
  buildSideFoot();
  if (!$('#repo-dialog').open) buildRepoDialog();
  render();
}

async function start() {
  setLocale(mygo?.locale ?? navigator.language);
  translatePage();
  if (!mygo) {
    document.body.replaceChildren(h('div', { class: 'welcome' }, h('h1', { class: 'mds-title' }, 'yesman'), h('p', null, t('Open this page in the yesman app.'))));
    return;
  }
  buildSideFoot();
  buildRepoDialog();
  mygo.on('mygo:locale', (e) => onLocale(e.locale));
  document.addEventListener('click', (e) => {
    const link = e.target.closest('a[href]');
    if (!link) return;
    e.preventDefault();
    if (link.dataset.path) {
      // A file of the workspace shown opens in the files pane.
      if (!files.openPath(link.dataset.path)) call('Reveal', link.dataset.path).catch(fail);
      return;
    }
    if (/^(https?:|mailto:)/.test(link.getAttribute('href'))) call('OpenURL', link.getAttribute('href')).catch(fail);
  });
  mygo.on('agents', onAgents);
  mygo.on('agent-event', onAgentEvent);
  mygo.on('changed', () => { refreshSoon(); info.onChanged(); });
  mygo.on('clones', onClones);
  mygo.on('fetches', onFetches);
  mygo.on('pi-packages', (jobs) => piPage.onJobs(jobs));
  mygo.on('update', onUpdate);
  mygo.on('files', (change) => { files.onChange(change); info.onFiles(change); });
  mygo.on('board', (yesman) => board.onChange(yesman));
  document.addEventListener('keydown', (e) => files.onKey(e));
  call('Update').then(onUpdate, () => {});
  mygo.on('focus-agent', (id) => {
    call('TakeFocus').catch(() => {});
    selectAgent(id);
  });
  await refreshState();
  // A window opened by a notification click shows the agent it was about.
  const focus = await call('TakeFocus').catch(() => '');
  if (focus && agentById(focus)) selectAgent(focus);
}

start();
