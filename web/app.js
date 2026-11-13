// yesman's window. The Go side owns all state; this page renders it and
// the live event stream of the agents' pi processes.
//
//   mygo.call("Yesman.<Method>", ...)  services in service.go
//   mygo.on("agents" | "agent-event" | "changed" | "clones" | "fetches" | "focus-agent" | "update" | "files", fn)

import { renderMarkdown } from './markdown.js';
import { $, h, icon } from './dom.js';
import { createFiles } from './files.js';
import { createContextViewer } from './context.js';
import { createModelPicker } from './models.js';
import { toast, openDialog, initSelect } from './mds/js/magpie.js';

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

const state = {
  snap: { yesmen: [], agents: [] },
  view: { kind: 'none' },
  convos: new Map(),
  drafts: new Map(),
  modelOptions: new Map(),
  infoOpen: false,
  repos: [],
  settingsPage: 'general',
  settings: null, // Yesman.Settings, for the General page
  storageDraft: null, // destination and inline feedback while changing folders
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
  for (const map of [state.convos, state.drafts, state.modelOptions]) {
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
  const t = c.tools.get(block.id);
  const el = h('details', { class: 'tool', 'data-tool': block.id, 'data-status': t.status });
  el.append(
    h('summary', null,
      icon(t.status === 'done' ? 'check' : t.status === 'error' ? 'alert' : 'spinner', 'mds-icon tool-mark'),
      h('span', { class: 'tool-name' }, block.name.replace(/^yesman_/, 'yesman · ')),
      h('span', { class: 'tool-sum' }, toolSummary(t.args)),
      h('span', { class: 'tool-state' }, t.status === 'error' ? 'failed' : '')),
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
  const t = c.tools.get(id);
  const el = document.querySelector(`#thread [data-tool="${CSS.escape(id)}"]`);
  if (!t || !el) return;
  el.dataset.status = t.status;
  const mark = el.querySelector('.tool-mark');
  mark?.replaceWith(icon(t.status === 'done' ? 'check' : t.status === 'error' ? 'alert' : 'spinner', 'mds-icon tool-mark'));
  el.querySelector('.tool-state').textContent = t.status === 'error' ? 'failed' : '';
  el.querySelector('.tool-sum').textContent = toolSummary(t.args);
  if (el.open) el.dispatchEvent(new Event('toggle'));
}

function blockEl(c, block) {
  if (block.type === 'text') return h('div', { class: 'md', html: renderMarkdown(block.text) });
  if (block.type === 'thinking') {
    if (!block.thinking?.trim()) return null;
    return h('details', { class: 'thinking' }, h('summary', null, 'Thinking'), h('div', null, block.thinking));
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
          h('summary', null, `Report from worker ${report[1]}`),
          h('div', { class: 'md', html: renderMarkdown(report[2]) }));
      }
      const fromLead = text.match(/^\[from your lead\] ([\s\S]*)$/);
      if (fromLead) {
        return h('div', { class: 'note' }, h('strong', null, 'From the lead'), h('div', { class: 'md', html: renderMarkdown(fromLead[1]) }));
      }
      return h('div', { class: 'msg-user' }, text);
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
        el.append(h('div', { class: 'note note--quiet' }, 'Stopped.'));
      }
      return el.childNodes.length ? el : null;
    }
    case 'toolResult':
      return null; // shown with its tool call
    case 'compactionSummary':
    case 'branchSummary':
      return h('div', { class: 'note note--quiet' }, 'Earlier conversation was summarized to save context.');
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
    thread.append(h('div', { class: 'working' }, icon('spinner', 'mds-icon tool-mark'), 'Waking up…'));
  } else if (!c.messages.length && !c.live) {
    const y = yesmanOf(a.id);
    thread.append(
      a.role === 'lead'
        ? h('div', { class: 'thread-empty' }, h('strong', null, `${a.yesman} is ready`),
            `Tell ${a.yesman} what you want done. It works in ${y?.path ?? 'its workspace'}, checks out repos from the pool, and starts workers when the job splits up.`)
        : h('div', { class: 'thread-empty' }, h('strong', null, a.name), 'This worker has not started yet.'),
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
    thread.append(h('div', { class: 'working working-live', 'data-status': 'working' }, h('i', { class: 'mds-dot' }), 'Working…'));
  }
}

// Live records from an agent's pi process.
function onAgentEvent(ev) {
  const { agent: id, data: e } = ev;
  if (e.type === 'extension_ui_request' && e.method === 'notify') {
    toast(`${id}: ${e.message}`);
    return;
  }
  if (e.type === 'extension_error') {
    toast(`${id}: extension error: ${e.error}`, { duration: 6000 });
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
      const t = c.tools.get(e.toolCallId) ?? { args: e.args };
      t.args = e.args ?? t.args;
      if (e.type === 'tool_execution_end') {
        t.status = e.isError ? 'error' : 'done';
        t.result = resultText(e.result);
      } else {
        t.status = 'running';
        if (e.partialResult) t.result = resultText(e.partialResult);
      }
      c.tools.set(e.toolCallId, t);
      if (visible) updateTool(c, e.toolCallId);
      break;
    }
    case 'compaction_start':
      if (visible) $('#thread').append(h('div', { class: 'note note--quiet' }, 'Summarizing earlier conversation to save context…'));
      break;
    case 'auto_retry_start':
      if (visible) toast(`Retrying after: ${e.errorMessage}`);
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

function agentRow(a, cls, label) {
  const current = state.view.kind === 'agent' && state.view.id === a.id;
  const meta = a.status === 'idle' || a.status === 'stopped' ? '' : STATUS[a.status] ?? a.status;
  return h('button', {
    class: `agent-row ${cls}`, type: 'button', 'data-agent': a.id, 'data-status': a.status, 'aria-current': current ? 'page' : null,
    'aria-haspopup': 'menu',
    title: `${a.id}: ${STATUS[a.status] ?? a.status}${a.detail ? `\n${a.detail}` : ''}`,
    onclick: () => selectAgent(a.id),
    oncontextmenu: (e) => openAgentMenu(e, a),
    onkeydown: (e) => {
      if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) openAgentMenu(e, a);
    },
  }, statusDot(a.status), h('span', { class: 'name' }, label), h('span', { class: 'meta' }, meta));
}

async function openAgentMenu(e, a) {
  e.preventDefault();
  e.stopPropagation();
  const row = e.currentTarget;
  const rect = row.getBoundingClientRect();
  const pointer = e.type === 'contextmenu' && (e.clientX !== 0 || e.clientY !== 0);
  const x = Math.round(pointer ? e.clientX : rect.left + 20);
  const y = Math.round(pointer ? e.clientY : rect.bottom);
  try {
    const action = await call('AgentMenu', a.id, x, y);
    const current = agentById(a.id);
    if (!current || keyOf(current) !== keyOf(a)) return;
    const workspace = yesmanOf(a.id);
    switch (action) {
      case 'open': selectAgent(a.id); break;
      case 'workspace':
        state.infoOpen = true;
        selectAgent(a.id);
        break;
      case 'reveal': await call('Reveal', workspace.path); break;
      case 'knowledge': await call('Reveal', `${workspace.path}/.yesman/knowledge/INDEX.md`); break;
      case 'logs': await call('Reveal', `${workspace.path}/.yesman/sessions/${a.name}`); break;
      case 'abort': await call('Abort', a.id); break;
      case 'sleep': await call('Stop', a.id); break;
      case 'archive': await dismiss(a.yesman); break;
      case 'remove': await removeWorker(a); break;
      default:
        document.querySelector(`.agent-row[data-agent="${CSS.escape(a.id)}"]`)?.focus({ preventScroll: true });
    }
  } catch (err) {
    fail(err);
  }
}

function renderSidebar() {
  const list = $('#yesmen');
  list.replaceChildren();
  const waiting = state.snap.agents.filter((a) => a.status === 'waiting');
  if (waiting.length) {
    list.append(h('p', { class: 'side-group' }, 'Needs you'));
    for (const a of waiting) list.append(agentRow(a, '', a.role === 'lead' ? a.yesman : a.id));
  }
  list.append(h('div', { class: 'side-group' }, h('span', null, 'Yesmen'),
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Hire a yesman', 'aria-label': 'Hire a yesman', onclick: openHire }, icon('plus'))));
  for (const y of state.snap.yesmen) {
    const agents = state.snap.agents.filter((a) => a.yesman === y.name);
    const lead = agents.find((a) => a.role === 'lead');
    if (lead) list.append(agentRow(lead, 'agent-row--yesman', y.name));
    for (const w of agents.filter((a) => a.role === 'worker')) list.append(agentRow(w, 'agent-row--worker', w.name));
  }
  renderSideFoot();
}

// ---------- Main column ----------

// The titlebar: where you are on the left, what you can do on the right.
function renderBar() {
  const crumbs = $('#crumbs');
  const sep = () => h('span', { class: 'mds-crumb-sep', 'aria-hidden': 'true' }, '/');
  const a = selectedAgent();
  if (state.view.kind === 'settings') {
    const page = SETTINGS.find((s) => s.page === state.view.page);
    crumbs.replaceChildren(h('span', null, 'Settings'), sep(), h('span', { 'aria-current': 'page' }, page.label));
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
    a ? h('span', { class: 'mds-status', 'data-status': a.status, role: 'status' }, h('i', { class: 'mds-dot', 'aria-hidden': 'true' }), h('span', null, STATUS[a.status] ?? a.status)) : null,
    updatePill(),
    a && a.status !== 'stopped'
      ? button('power', 'Put this agent to sleep: end its pi process. It resumes its session when you next talk to it.', () => call('Stop', a.id).catch(fail))
      : null,
    a?.role === 'worker' ? button('x', 'Remove worker', () => removeWorker(a)) : null,
    a ? button('panel', state.infoOpen ? 'Hide the files' : 'Show the files', toggleInfo, { 'aria-pressed': String(state.infoOpen) }) : null,
  ].filter(Boolean));
}

// The pill shows only while there is something to do about an update.
function updatePill() {
  const u = state.update;
  if (!u?.enabled) return null;
  const pill = (iconName, label, title, extra) => h('button', { class: 'mds-pill mds-pill--responsive', type: 'button', title, ...extra },
    icon(iconName), h('span', { class: 'mds-pill-label' }, label));
  switch (u.state) {
    case 'available': return pill('up', `Update ${u.version}`, `Install yesman ${u.version}`, { onclick: installUpdate });
    case 'downloading': return pill('reload', `Updating ${Math.round((u.progress ?? 0) * 100)}%`, `Installing yesman ${u.version}`, { disabled: true, 'aria-busy': 'true' });
    case 'ready': return pill('up', 'Restart to update', `Restart into yesman ${u.version}`, { onclick: restartToUpdate });
    case 'failed': return pill('alert', 'Update failed', `Installing yesman ${u.version} failed: ${u.error}\nClick to try again.`, { 'data-tone': 'error', onclick: installUpdate });
    default: return null;
  }
}

function installUpdate() {
  call('InstallUpdate').catch(fail);
}

async function restartToUpdate() {
  const busy = state.snap.agents.filter((a) => a.status === 'working' || a.status === 'waiting').length;
  if (busy && !(await confirmAction('Restart yesman?',
    `${busy === 1 ? 'An agent is' : `${busy} agents are`} working. Restarting stops ${busy === 1 ? 'it' : 'them'}; ${busy === 1 ? 'it picks up its' : 'they pick up their'} session when you next talk to ${busy === 1 ? 'it' : 'them'}.`,
    'Restart'))) return;
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
    }, entry?.error ? 'Retry loading models' : 'Loading models…');
  }
  const disabled = entry.busy || working || a.status === 'starting';
  const picker = createModelPicker({
    models: entry.models, provider: a.provider, model: a.model, disabled,
    onSelect: (selected) => changeModelOption(a, 'SetModel', selected.provider, selected.id),
  });
  composerSelects.push(picker);
  const levels = a.effortLevels ?? [];
  const effort = h('select', {
    class: 'mds-select mds-select--text', 'aria-label': 'Effort', disabled: disabled || levels.length < 2,
    title: levels.length < 2 ? 'This model has no adjustable reasoning effort' : 'Reasoning effort',
    onchange: () => changeModelOption(a, 'SetEffort', effort.value),
  }, levels.map((level) => h('option', { value: level }, level === 'xhigh' ? 'Extra high' : level[0].toUpperCase() + level.slice(1))));
  if (!levels.length) effort.append(h('option', { value: '' }, 'Unavailable'));
  effort.value = a.effort ?? '';
  return h('div', { class: 'composer-options', 'aria-busy': String(entry.busy) },
    h('span', { class: 'composer-choice composer-choice--model' }, picker.trigger),
    h('label', { class: 'composer-choice' }, h('span', null, 'Effort'), effort));
}

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
      if (again.tagName === 'INPUT') again.setSelectionRange(active.selectionStart, active.selectionEnd);
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

function renderDock() {
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
    dock.append(h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null, h('strong', null, 'This agent stopped'), h('p', null, a.detail))));
  } else if (a.detail) {
    dock.append(h('div', { class: 'mds-notice', role: 'status' }, h('div', null, h('strong', null, 'Needs a look'), h('p', null, a.detail))));
  }
  if (a.status === 'waiting' && a.dialog) dock.append(askEl(a));

  const working = a.status === 'working' || a.status === 'waiting';
  const textarea = h('textarea', {
    rows: 1, 'aria-label': 'Message',
    placeholder: a.role === 'lead' ? `Message ${a.yesman}…` : `Message worker ${a.name}…`,
  });
  textarea.value = state.drafts.get(keyOf(a)) ?? '';
  const send = h('button', { class: 'round', type: 'submit', 'aria-label': 'Send', title: working ? 'Send: delivered after its current step' : 'Send' }, icon('enter'));
  const stop = working ? h('button', { class: 'round round--stop', type: 'button', 'aria-label': 'Stop', title: 'Stop the current run', onclick: () => call('Abort', a.id).catch(fail) }, icon('stop')) : null;
  const sync = () => {
    send.disabled = !textarea.value.trim() || !!state.modelOptions.get(keyOf(a))?.busy;
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
  const form = h('form', { class: 'composer', onsubmit: (e) => { e.preventDefault(); sendMessage(a, textarea); } },
    h('div', { class: 'composer-input' }, textarea, h('div', { class: 'ask-actions' }, stop, send)),
    h('div', { class: 'composer-controls' }, modelOptionsEl(a, working), contextViewer.mount(a)),
  );
  dock.append(form);
  composerSelects.push(...[...form.querySelectorAll('select')].map((el) => {
    const select = initSelect(el);
    select.trigger.title = el.title;
    return select;
  }));
  sync();
  const nextControl = composerSelects.find((select) => select.trigger.getAttribute('aria-label') === control);
  if (nextControl && !nextControl.trigger.disabled) nextControl.trigger.focus({ preventScroll: true });
  else if (focused || !keep) {
    textarea.focus();
    if (caret) textarea.setSelectionRange(...caret);
  }
}

async function sendMessage(a, textarea) {
  const text = textarea.value.trim();
  if (!text || state.modelOptions.get(keyOf(a))?.busy) return;
  textarea.value = '';
  state.drafts.delete(keyOf(a));
  textarea.dispatchEvent(new Event('input'));
  try {
    await call('Send', a.id, text);
  } catch (err) {
    textarea.value = text;
    state.drafts.set(keyOf(a), text);
    textarea.dispatchEvent(new Event('input'));
    fail(err);
  }
}

// A question an extension asked through pi's RPC dialogs.
function askEl(a) {
  const d = a.dialog;
  const answer = (payload) => call('Answer', a.id, d.id, payload).catch(fail);
  const box = h('div', { class: 'ask', role: 'alert' }, h('strong', null, d.title || 'Question'), d.message ? h('p', null, d.message) : null);
  const actions = h('div', { class: 'ask-actions' });
  if (d.method === 'confirm') {
    actions.append(
      h('button', { class: 'mds-button mds-button--primary mds-button--small', type: 'button', onclick: () => answer({ confirmed: true }) }, 'Yes'),
      h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => answer({ confirmed: false }) }, 'No'),
    );
  } else if (d.method === 'select') {
    for (const option of d.options ?? []) {
      actions.append(h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => answer({ value: option }) }, option));
    }
    actions.append(h('button', { class: 'mds-button mds-button--subtle mds-button--small', type: 'button', onclick: () => answer({ cancelled: true }) }, 'Cancel'));
  } else {
    const input = d.method === 'editor'
      ? h('textarea', { class: 'mds-textarea', rows: 5 })
      : h('input', { class: 'mds-input', placeholder: d.placeholder ?? '' });
    input.value = d.prefill ?? '';
    box.append(input);
    actions.append(
      h('button', { class: 'mds-button mds-button--primary mds-button--small', type: 'button', onclick: () => answer({ value: input.value }) }, 'Submit'),
      h('button', { class: 'mds-button mds-button--subtle mds-button--small', type: 'button', onclick: () => answer({ cancelled: true }) }, 'Cancel'),
    );
  }
  box.append(actions);
  return box;
}

function renderWelcome(body) {
  const input = h('input', { class: 'mds-input', placeholder: 'name, e.g. checkout-v2', pattern: '[a-z0-9][a-z0-9\\-]{0,31}', required: true, spellcheck: 'false', 'aria-label': 'Name' });
  body.append(
    h('div', { class: 'page' },
      h('div', { class: 'welcome' },
        h('h1', { class: 'mds-title' }, 'Hire a yesman'),
        h('p', null, 'A yesman takes on a project. It gets a workspace of its own, checks out the repositories it needs from the shared pool, remembers what it learns, and starts workers when the job splits up.'),
        state.snap.piError ? h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null, h('strong', null, 'pi was not found'), h('p', null, state.snap.piError))) : null,
        h('form', { onsubmit: (e) => { e.preventDefault(); hire(input.value.trim()); } },
          input, h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, 'Hire')),
      )),
  );
  input.focus();
}

// ---------- Settings ----------

const SETTINGS = [
  { page: 'general', label: 'General', icon: 'sliders' },
  { page: 'repositories', label: 'Repositories', icon: 'branch' },
  { page: 'config', label: 'Import and export', icon: 'transfer' },
  { page: 'about', label: 'About', icon: 'about' },
];

// The footer is built once so its submenu can slide open and closed.
function buildSideFoot() {
  const nav = h('nav', { class: 'mds-subnav', id: 'settings-nav', 'aria-label': 'Settings' },
    SETTINGS.map((s) => h('button', { class: 'mds-nav-item', type: 'button', 'data-page': s.page, onclick: () => showSettings(s.page) },
      icon(s.icon), h('span', null, s.label))));
  $('#side-foot').replaceChildren(h('div', { class: 'mds-nav-branch settings-branch' },
    h('div', { class: 'settings-drawer' }, h('div', { class: 'settings-clip' }, nav)),
    h('button', { class: 'mds-nav-item mds-nav-parent', id: 'settings-button', type: 'button', 'aria-controls': 'settings-nav', onclick: () => showSettings(state.settingsPage) },
      icon('gear'), h('span', null, 'Settings'))));
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
  try {
    if (page === 'general') state.settings = await call('Settings');
    else if (page === 'about') state.about = await call('About');
    else if (page === 'repositories') [state.repos, state.settings, state.fetches] = await Promise.all([call('Repos'), call('Settings'), call('Fetches')]);
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
  return h('button', { class: 'mds-button mds-button--small', type: 'button', title: path, 'aria-label': label ? `Reveal ${label}` : null, onclick: () => call('Reveal', path).catch(fail) }, icon('folder'), 'Reveal');
}

function renderGeneral(inner) {
  const v = state.settings;
  if (!v) return;
  const appearance = h('select', { class: 'mds-select', 'aria-label': 'Appearance', onchange: async (e) => {
    const select = e.currentTarget;
    try {
      await call('SetAppearance', select.value);
      v.appearance = select.value;
    } catch (err) {
      fail(err);
      select.value = v.appearance;
    }
  } }, [['system', 'Match the system'], ['light', 'Light'], ['dark', 'Dark']].map(([value, label]) =>
    h('option', { value, selected: value === v.appearance }, label)));
  inner.append(
    settingsSection('Appearance', null,
      preferenceRow('Theme', h('p', null, 'The window and its menus follow this.'), appearance)),
    settingsSection('Storage', 'Choose where yesman keeps its data.',
      ...storageRows(v),
      ...[
        ['Repositories', 'The shared pool of bare Git repositories.', v.pool],
        ['Workspaces', 'The parent folder for all yesman workspaces.', v.yesmen],
        ['Archive', 'Workspaces of dismissed yesmen, with their worktrees.', v.archive],
        ['Runtime', 'Files yesman generates when it starts.', v.runtime],
        ['Settings file', 'Appearance and repository fetch preferences.', v.settingsFile],
        ['Storage configuration', 'Remembers the data folder across launches.', v.storageConfig],
      ].filter(([, , path]) => path).map(([label, hint, path]) => storagePathRow(label, hint, path))),
    settingsSection('Agents', null,
      preferenceRow('pi',
        v.piError ? h('p', { class: 'danger' }, v.piError) : h('p', null, 'Every lead and worker runs this pi. Set YESMAN_PI to use another. ', h('span', { class: 'path' }, v.pi)),
        h('span', { class: 'mds-status', 'data-status': v.piError ? 'failed' : 'idle' }, h('i', { class: 'mds-dot' }), h('span', null, v.piError ? 'Not found' : 'Found')))),
  );
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
  } }, 'Change…');
  const current = preferenceRow('yesman folder',
    h('div', null, h('p', { class: 'path', title: v.home }, v.home),
      v.homeOverride ? h('p', null, 'Set by YESMAN_HOME. Unset it and restart to change the folder here.') : null),
    h('div', { class: 'storage-actions' }, revealButton(v.home), change));
  current.classList.add('storage-home-row');
  const rows = [current];
  if (d) {
    const input = h('input', { class: 'mds-input', id: 'storage-path', required: true, value: d.path,
      placeholder: 'Full path to a folder', autocomplete: 'off', spellcheck: 'false',
      disabled: d.busy, 'aria-describedby': 'storage-hint storage-error', 'aria-invalid': d.error ? 'true' : null,
      oninput: (e) => { d.path = e.currentTarget.value; } });
    const browse = h('button', { class: 'mds-button', type: 'button', disabled: d.busy, onclick: async () => {
      browse.disabled = true;
      try {
        const path = await call('ChooseStorageFolder');
        if (path) { d.path = path; d.error = ''; }
      } catch (err) { d.error = errorText(err); }
      finally {
        if (state.view.kind === 'settings' && state.view.page === 'general' && state.storageDraft === d) {
          renderBody();
          $('#storage-path')?.focus();
        }
      }
    } }, icon('folder'), 'Browse…');
    rows.push(h('form', { class: 'storage-edit', 'aria-busy': String(d.busy), onsubmit: (e) => { e.preventDefault(); changeStorageFolder(d); } },
      h('label', { class: 'field', for: 'storage-path' }, 'New yesman folder'),
      h('div', { class: 'storage-input' }, input, browse),
      h('p', { class: 'mds-hint', id: 'storage-hint' }, 'Files are not moved. To keep your data, copy the current folder yourself before switching.'),
      h('p', { class: 'danger', id: 'storage-error', role: 'alert', hidden: !d.error }, d.error),
      h('div', { class: 'storage-actions' },
        h('button', { class: 'mds-button', type: 'button', disabled: d.busy, onclick: () => {
          state.storageDraft = null; renderBody();
          $('.storage-actions button:last-child')?.focus();
        } }, 'Cancel'),
        h('button', { class: 'mds-button mds-button--primary', type: 'submit', disabled: d.busy }, d.busy ? 'Please wait…' : 'Use folder and restart'))));
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
    const agents = active ? ` ${active === 1 ? 'The active agent stops' : `${active} active agents stop`}; their sessions resume when you next talk to them.` : '';
    if (!(await confirmAction('Use this yesman folder?', `Restart yesman using ${destination}.${agents} Files are not copied. Save your edits and copy any data you want to use there first.`, 'Use folder and restart'))) return;
    await call('SetStorageFolder', destination);
  } catch (err) {
    d.error = errorText(err);
  } finally {
    d.busy = false;
    if (state.view.kind === 'settings' && state.view.page === 'general') {
      renderBody();
      if (d.error) $('#storage-path')?.focus();
    }
  }
}

// What About says of updates, with the one action that fits.
function updateStatus() {
  const u = state.update;
  const faint = (text) => h('span', { class: 'about-faint' }, text);
  if (!u) return faint('Checking…');
  if (!u.enabled) return faint('Release builds update themselves from GitHub. This build does not.');
  const action = (label, onclick) => h('button', { class: 'mds-button mds-button--small', type: 'button', onclick }, label);
  const check = () => call('CheckForUpdates').catch(fail);
  switch (u.state) {
    case 'available': return h('span', { class: 'about-update' }, h('span', null, `yesman ${u.version} is available`), action('Install', installUpdate));
    case 'downloading': return faint(`Installing ${u.version}, ${Math.round((u.progress ?? 0) * 100)}%`);
    case 'ready': return h('span', { class: 'about-update' }, h('span', null, `${u.version} is installed`), action('Restart', restartToUpdate));
    case 'failed': return h('span', { class: 'about-update' }, h('span', { class: 'danger' }, `Installing ${u.version} failed: ${u.error}`), action('Try again', installUpdate));
    default:
      return h('span', { class: 'about-update' },
        u.checking ? faint('Checking…')
          : u.checkError ? h('span', { class: 'danger' }, `The last check failed: ${u.checkError}`)
          : h('span', null, u.checkedAt ? `Up to date, ${ago(u.checkedAt, 'checked')}` : 'Not checked yet'),
        u.checking ? null : action('Check now', check));
  }
}

function renderAbout(inner) {
  const v = state.about;
  const row = (label, ...value) => h('div', { class: 'mds-row about-row' },
    h('div', { class: 'mds-row-label' }, h('label', null, label)),
    h('div', { class: 'mds-row-control about-value' }, value));
  const faint = (text) => h('span', { class: 'about-faint' }, text);
  let build = faint('Checking…');
  if (v) {
    const date = v.time ? new Date(v.time).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) : '';
    const detail = [v.revision ? v.revision.slice(0, 12) : '', date, v.modified ? 'with local changes' : ''].filter(Boolean).join(' · ');
    build = [v.version || 'Development build', detail ? faint(` ${detail}`) : null];
  }
  inner.append(
    settingsSection('yesman', 'Hires yesmen: pi agents that each take on one project in a workspace of their own.',
      row('Build', build),
      row('Updates', updateStatus()),
      row('pi', v ? (v.pi || h('span', { class: 'danger' }, v.piError)) : faint('Checking…')),
      row('MyGo', v ? v.mygo || faint('unknown') : faint('Checking…')),
      row('Magpie', v ? v.magpie || faint('unknown') : faint('Checking…')),
      row('Go', v ? v.go : faint('Checking…'))),
    h('div', null, h('button', { class: 'mds-link-button', type: 'button', onclick: () => call('OpenURL', 'https://github.com/idy/yesman').catch(fail) }, 'Source on GitHub')),
  );
}

// ---------- Import and export ----------

// A configuration file carries the settings and the repo pool. Importing it
// shows what it would change first, then merges it or replaces with it.

const THEMES = { system: 'Match the system', light: 'Light', dark: 'Dark' };
const autoFetchLabel = (minutes) => AUTO_FETCH.find(([m]) => m === minutes)?.[1] ?? `Every ${minutes} minutes`;
const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
const fileName = (path) => path.split(/[\\/]/).pop();

function renderConfig(inner) {
  const c = state.config;
  const action = (label, onclick) => h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: c.busy, onclick }, label);
  const e = c.exported;
  const exportHint = e
    ? h('p', null, `Exported ${plural(e.repos, 'repository', 'repositories')} and the settings to `, h('span', { class: 'path', title: e.path }, e.path),
        e.skipped.length ? `. Left out, as they are not from GitHub: ${e.skipped.join(', ')}.` : '')
    : h('p', null, 'The theme, the fetch schedule and every GitHub repository in the pool, as a TOML file.');
  const i = c.imported;
  const importHint = i ? h('div', { class: 'config-imported' }, i.lines.map((line) => h('p', { class: line.danger ? 'danger' : null }, line.text)))
    : h('p', null, 'A file exported from yesman. You see what it changes, then merge it or replace with it.');
  inner.append(
    settingsSection('Configuration', 'Back up yesman’s settings and repo pool, or carry them to another computer. Workspaces, their knowledge and conversations are not included.',
      preferenceRow('Export', exportHint, h('div', { class: 'storage-actions' }, e ? revealButton(e.path, 'the exported file') : null, action('Export…', exportConfig))),
      preferenceRow('Import', importHint, action('Import…', importConfig))));
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
  const fields = [['appearance', 'Theme', (v) => THEMES[v] ?? v], ['autoFetch', 'Fetch automatically', autoFetchLabel]];
  const lines = [];
  for (const [key, label, show] of fields) {
    const now = plan.current[key];
    if (plan.merge[key] !== now) lines.push({ text: `${label}: ${show(now)} → ${show(plan.merge[key])}` });
    else if (plan.replace[key] !== now) lines.push({ text: `${label}: ${show(now)} → ${show(plan.replace[key])}`, replaceOnly: true });
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
  const replaceDiffers = removable > 0 || settings.some((s) => s.replaceOnly);
  dialog.replaceChildren(h('form', { method: 'dialog' },
    h('h2', { class: 'mds-heading', id: 'import-title' }, `Import ${fileName(plan.path)}`),
    h('p', { class: 'dialog-hint path', title: plan.path }, plan.path),
    h('div', { class: 'import-body' },
      part('Settings', settings.length
        ? list(settings.map((s) => h('li', null, s.text, s.replaceOnly ? h('span', { class: 'import-faint' }, ' · Replace only') : null)))
        : h('p', null, 'Unchanged.')),
      part('Repositories',
        plan.add.length ? h('p', null, `Clones ${plural(plan.add.length, 'repository', 'repositories')}: `, h('span', { class: 'import-names' }, plan.add.join(', '))) : null,
        h('p', null, plan.present.length ? `${plural(plan.present.length, 'repository', 'repositories')} of the file ${plan.present.length === 1 ? 'is' : 'are'} in the pool already.` : plan.add.length ? '' : 'The file lists none.')),
      extra.length ? part(`Only in this pool (${extra.length})`,
        h('p', null, 'Merge keeps them. Replace removes them, except those that work depends on.'),
        list(extra.map((r) => h('li', null, h('span', { class: 'import-names', title: r.name }, r.label),
          h('span', { class: 'import-faint' }, r.inUse ? ` · kept: ${r.inUse}` : r.clone ? ` · ${r.clone === 'failed' ? 'failed to clone' : 'cloning'}, stopped with Replace` : ' · removed with Replace'))))) : null,
      plan.ignored.length ? h('p', { class: 'import-faint' }, `Ignored, as this yesman does not know them: ${plan.ignored.join(', ')}`) : null),
    h('div', { class: 'mds-dialog-actions' },
      h('button', { class: 'mds-button', value: 'cancel' }, 'Cancel'),
      replaceDiffers ? h('button', { class: 'mds-button', value: 'replace', title: 'Also reset the settings the file leaves out and remove the repositories it does not list' }, 'Replace') : null,
      h('button', { class: 'mds-button mds-button--primary', value: 'merge' }, 'Merge'))));
  const answer = openDialog(dialog);
  dialog.querySelector('.mds-button--primary').focus();
  return answer;
}

// What an import did, in lines for the Import row.
function importLines(plan, result) {
  const verb = result.mode === 'replace' ? 'Replaced with' : 'Merged';
  const parts = [];
  if (settingChanges(plan).some((s) => result.mode === 'replace' || !s.replaceOnly)) parts.push('settings changed');
  if (result.cloning.length) parts.push(`${plural(result.cloning.length, 'repository', 'repositories')} cloning`);
  if (result.removed.length) parts.push(`${result.removed.length} removed`);
  if (result.stopped.length) parts.push(`${plural(result.stopped.length, 'clone')} stopped`);
  const lines = [{ text: `${verb} ${fileName(plan.path)}: ${parts.length ? parts.join(' · ') : 'nothing to change'}.` }];
  for (const r of result.kept) lines.push({ text: `Kept ${r.label}: ${r.inUse}.`, danger: r.inUse.startsWith('removing it failed') });
  return lines;
}

// ---------- Repositories ----------

function ago(iso, verb = 'fetched') {
  if (!iso) return `never ${verb}`;
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${verb} just now`;
  if (s < 3600) return `${verb} ${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${verb} ${Math.round(s / 3600)} h ago`;
  return `${verb} ${Math.round(s / 86400)} d ago`;
}

// owner/repo for a GitHub URL, which is all a pool repository can be;
// repositories added before yesman took only GitHub keep their URL.
const githubName = (url) => /github\.com[:/]([^/]+\/[^/]+?)(?:\.git)?\/?$/.exec(url ?? '')?.[1] ?? '';

// The key of a GitHub repository, owner/repo, which GitHub matches in any case.
const repoKey = (fullName) => fullName.toLowerCase();

function renderRepositories(inner) {
  const search = state.repos.length + state.clones.length > 1
    ? h('label', { class: 'search' }, icon('search'),
        h('input', { class: 'mds-input', id: 'repo-search', type: 'search', placeholder: 'Find a repository', 'aria-label': 'Find a repository', spellcheck: 'false', value: state.repoQuery,
          oninput: (e) => { state.repoQuery = e.currentTarget.value; renderRepoList(); } }))
    : null;
  inner.append(
    h('div', { class: 'mds-toolbar' },
      h('p', { class: 'toolbar-lead' }, 'GitHub repositories the yesmen check worktrees out of. Only yesman writes here. It fetches a repository whenever one is checked out, and all of them when you ask or on a schedule.'),
      h('div', { class: 'mds-toolbar-actions' }, search,
        h('button', { class: 'mds-button mds-button--small fetch-all', id: 'fetch-all', type: 'button', onclick: () => call('FetchAll').catch(fail) }),
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Reveal the repo pool', 'aria-label': 'Reveal the repo pool', onclick: () => call('Reveal', state.snap.pool).catch(fail) }, icon('folder')))),
    h('div', { class: 'card-list', id: 'repo-list', role: 'list', 'aria-label': 'Repositories' }),
    h('div', null, h('button', { class: 'mds-link-button add-link', type: 'button', onclick: openAddRepos }, icon('plus'), 'Add repositories')),
    autoFetchSection());
  renderRepoList();
}

const AUTO_FETCH = [[0, 'Never'], [1, 'Every minute'], [5, 'Every 5 minutes'], [15, 'Every 15 minutes'], [60, 'Every hour']];

function autoFetchSection() {
  const v = state.settings;
  if (!v) return null;
  const select = h('select', { class: 'mds-select', 'aria-label': 'Fetch automatically', onchange: async (e) => {
    const el = e.currentTarget;
    try {
      await call('SetAutoFetch', Number(el.value));
      v.autoFetch = Number(el.value);
    } catch (err) {
      fail(err);
      el.value = String(v.autoFetch);
    }
  } }, AUTO_FETCH.map(([value, label]) => h('option', { value, selected: value === v.autoFetch }, label)));
  return settingsSection('Fetching', null,
    preferenceRow('Fetch automatically', h('p', null, 'Fetches every repository in the pool while yesman runs, in the tray too.'), select));
}

// Fetch all spins while any repository fetches, and waits while all do.
function renderFetchAll() {
  const button = $('#fetch-all');
  if (!button) return;
  const busy = state.fetches.fetching.length > 0;
  button.disabled = !state.repos.length || state.repos.every((r) => state.fetches.fetching.includes(r.name));
  button.replaceChildren(busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload'), 'Fetch all');
}

const cloneStatus = (c) => (c.state === 'queued' ? 'waiting to clone' : 'cloning…');

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
  const status = clone ? cloneStatus(clone) : fetching ? 'fetching…' : error ? null : ago(r.fetchedAt);
  const meta = [name ? null : r.url, r.upstream ? `fork of ${githubName(r.upstream) || r.upstream}` : null, r.defaultBranch ? `default ${r.defaultBranch}` : null, status].filter(Boolean).join(' · ');
  return h('div', { class: 'card-row', role: 'listitem' },
    h('span', { class: 'card-row-icon' }, icon('branch')),
    h('div', { class: 'card-row-text' }, h('strong', { title: r.name }, name || r.name),
      h('span', { title: error || null }, meta, error ? [meta ? ' · ' : null, h('span', { class: 'danger' }, `last fetch failed: ${error}`)] : null)),
    users.length
      ? h('span', { class: 'card-row-aside', title: worktrees.map((w) => `${w.yesman}: ${w.dir}`).join('\n') },
          `${worktrees.length} worktree${worktrees.length === 1 ? '' : 's'} · ${users.join(', ')}`)
      : null,
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: `Fetch ${r.name}`, 'aria-label': `Fetch ${r.name}`, disabled: busy, onclick: () => fetchRepo(r.name) },
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
      failed ? h('span', { class: 'danger', title: c.error }, `Could not clone: ${c.error}`) : h('span', null, cloneStatus(c))),
    failed
      ? h('span', { class: 'card-row-actions' },
          button(`Try ${c.repo} again`, 'reload', () => call('RetryClone', c.repo).catch(fail)),
          button(`Dismiss ${c.repo}`, 'x', () => call('DismissClone', c.repo).catch(fail)))
      : h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', disabled: true, title: `${c.repo} is ${cloneStatus(c).replace('…', '')}`, 'aria-label': `${c.repo} is ${cloneStatus(c).replace('…', '')}` },
          h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })));
}

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
    list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, 'No repositories yet'), h('span', null, 'Add some from GitHub, or let a yesman add the ones it needs.')));
  } else if (!shown.length) {
    list.replaceChildren(h('div', { class: 'card-empty' }, h('span', null, `No repository matches “${state.repoQuery.trim()}”.`)));
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
    if (name) taken.set(repoKey(name), 'In the pool');
  }
  for (const c of state.clones) {
    if (c.state !== 'failed' && !taken.has(repoKey(c.repo))) taken.set(repoKey(c.repo), 'Cloning');
  }
  return taken;
}

function buildRepoDialog() {
  const input = h('input', { class: 'mds-input', id: 'picker-input', placeholder: 'owner, or owner/repo', 'aria-label': 'GitHub user, organization or repository',
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
    h('h2', { class: 'mds-heading', id: 'repo-dialog-title' }, 'Add repositories'),
    h('p', { class: 'dialog-hint' }, 'Name a GitHub user or organization to pick from its repositories, or owner/repo to add one. They clone in the background; for a fork, yesman also fetches the repository it was forked from.'),
    h('label', { class: 'search picker-search' }, icon('search'), input, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })),
    h('div', { class: 'picker-results', id: 'picker-results' }),
    h('div', { class: 'mds-dialog-actions picker-actions' },
      h('p', { class: 'picker-status', id: 'picker-error', 'aria-live': 'polite' }),
      h('button', { class: 'mds-button', type: 'button', onclick: () => $('#repo-dialog').close('cancel') }, 'Cancel'),
      h('button', { class: 'mds-button mds-button--primary', type: 'submit', id: 'picker-add' }, 'Add'))));
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
      ? `“${typed.raw}” is not a GitHub user, organization or repository.`
      : 'Type a GitHub user or organization to list its repositories.');
  } else if (entry.state === 'failed') {
    note(h('span', { class: 'danger' }, entry.error), h('span', null, 'Press Enter to try again.'));
  } else {
    const q = typed.name.toLowerCase();
    const shown = q ? entry.repos.filter((r) => r.fullName.slice(r.fullName.indexOf('/') + 1).toLowerCase().includes(q)) : entry.repos;
    if (!entry.repos.length) note(`${entry.owner} has no repositories you can see.`);
    else if (!shown.length) note(`No repository of ${entry.owner} matches “${typed.name}”.`);
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
  const count = shown.length === entry.repos.length ? `${shown.length} ${shown.length === 1 ? 'repository' : 'repositories'}` : `${shown.length} of ${entry.repos.length}`;
  const head = h('label', { class: 'picker-row picker-all' }, all, h('span', null, `Select all · ${count}`));
  const rows = shown.map((r) => {
    const key = repoKey(r.fullName);
    const status = taken.get(key);
    const box = h('input', { type: 'checkbox', 'data-key': key, disabled: !!status, onchange: (e) => {
      if (e.currentTarget.checked) picker.selected.set(key, r.fullName);
      else picker.selected.delete(key);
      syncPicker();
    } });
    box.checked = !!status || picker.selected.has(key);
    const tags = status ?? [r.private && 'Private', r.fork && 'Fork', r.archived && 'Archived'].filter(Boolean).join(' · ');
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
  add.textContent = n > 1 ? `Add ${n} repositories` : n === 1 ? 'Add 1 repository' : choice.length ? `Add ${typed.full}` : 'Add';
  const status = $('#picker-error');
  status.dataset.tone = picker.error ? 'error' : '';
  status.title = picker.error;
  const entry = typed.owner && picker.lists.get(typed.owner.toLowerCase());
  const loading = typed.owner && (!entry || entry.state === 'loading');
  status.textContent = picker.error
    || (n ? `${n} selected`
      : typed.full && pickerTaken().has(repoKey(typed.full)) ? `${typed.full} is already in the pool.`
      : pickerListed(typed) === null ? `Not in ${typed.owner}’s list; Add tries it anyway.`
      : loading ? `Listing ${typed.owner}’s repositories…` : '');
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
  isOpen: () => state.infoOpen,
  setOpen: (on) => {
    state.infoOpen = on;
    files.render();
    renderBarActions();
  },
  checkout: (y) => openCheckout(y),
  dismiss: (name) => dismiss(name),
  confirm: (title, body, ok) => confirmAction(title, body, ok),
});

function toggleInfo() {
  state.infoOpen = !state.infoOpen;
  files.render();
  renderBarActions();
}

function renderInfo() {
  files.render();
}

async function openCheckout(y) {
  try {
    state.repos = await call('Repos');
  } catch (err) {
    fail(err);
    return;
  }
  if (!state.repos.length) {
    toast('Add a repository to the pool first.');
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
    toast(`Checked out ${wt.dir || wt.path}`);
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
    toast('Use 1-32 lowercase letters, digits and dashes.');
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
  if (!(await confirmAction(`Dismiss ${name}?`, `Its agents stop and its workspace moves to the archive. Branches stay in the repo pool.`, 'Dismiss'))) return;
  try {
    const dest = await call('Dismiss', name);
    toast(`Archived to ${dest}`);
    // A view of its agents falls back to the first yesman once they are gone.
    await refreshState();
  } catch (err) {
    fail(err);
  }
}

async function removeWorker(a) {
  if (!(await confirmAction(`Remove ${a.name}?`, 'It stops and leaves this yesman. Its worktrees and session log stay in the workspace.', 'Remove'))) return;
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
  state.snap.agents = agents;
  pruneCaches();
  renderSidebar();
  const after = selectedAgent();
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

async function start() {
  if (!mygo) {
    document.body.replaceChildren(h('div', { class: 'welcome' }, h('h1', { class: 'mds-title' }, 'yesman'), h('p', null, 'Open this page in the yesman app.')));
    return;
  }
  buildSideFoot();
  buildRepoDialog();
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
  mygo.on('changed', refreshSoon);
  mygo.on('clones', onClones);
  mygo.on('fetches', onFetches);
  mygo.on('update', onUpdate);
  mygo.on('files', (change) => files.onChange(change));
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
