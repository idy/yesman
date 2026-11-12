// yesman's window. The Go side owns all state; this page renders it and
// the live event stream of the agents' pi processes.
//
//   mygo.call("Yesman.<Method>", ...)  services in service.go
//   mygo.on("agents" | "agent-event" | "changed" | "focus-agent", fn)

import { renderMarkdown } from './markdown.js';
import { toast, openDialog } from './mds/js/magpie.js';

const $ = (selector, root = document) => root.querySelector(selector);

function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(props ?? {})) {
    if (value == null || value === false) continue;
    if (key === 'class') el.className = value;
    else if (key === 'html') el.innerHTML = value; // only rendered Markdown, which escapes its input
    else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
    else if (value === true) el.setAttribute(key, '');
    else el.setAttribute(key, value);
  }
  for (const child of children.flat(Infinity)) {
    if (child == null || child === false) continue;
    el.append(child instanceof Node ? child : String(child));
  }
  return el;
}

const SVG = 'http://www.w3.org/2000/svg';
function icon(name, cls = 'mds-icon') {
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('class', cls);
  svg.setAttribute('aria-hidden', 'true');
  const paths = {
    send: 'M205.66 117.66a8 8 0 0 1-11.32 0L136 59.31V216a8 8 0 0 1-16 0V59.31l-58.34 58.35a8 8 0 0 1-11.32-11.32l72-72a8 8 0 0 1 11.32 0l72 72a8 8 0 0 1 0 11.32Z',
    stop: 'M200 56v144a16 16 0 0 1-16 16H72a16 16 0 0 1-16-16V56a16 16 0 0 1 16-16h112a16 16 0 0 1 16 16Z',
    folder: 'M216 72h-84.69L104 44.69A15.86 15.86 0 0 0 92.69 40H40a16 16 0 0 0-16 16v144.62A15.4 15.4 0 0 0 39.38 216h177.51A15.13 15.13 0 0 0 232 200.89V88a16 16 0 0 0-16-16ZM40 56h52.69l16 16H40Zm176 144H40V88h176Z',
    panel: 'M216 40H40a16 16 0 0 0-16 16v144a16 16 0 0 0 16 16h176a16 16 0 0 0 16-16V56a16 16 0 0 0-16-16ZM40 56h112v144H40Zm176 144h-48V56h48v144Z',
    power: 'M120 128V48a8 8 0 0 1 16 0v80a8 8 0 0 1-16 0Zm67.41-74.07a8 8 0 0 0-8.82 13.36C201.4 82.33 216 106.23 216 128a88 88 0 0 1-176 0c0-21.77 14.6-45.67 37.41-60.71a8 8 0 1 0-8.82-13.36C40.62 72.36 24 99.42 24 128a104 104 0 0 0 208 0c0-28.58-16.62-55.64-44.59-74.07Z',
    gear: 'M128,80a48,48,0,1,0,48,48A48.05,48.05,0,0,0,128,80Zm0,80a32,32,0,1,1,32-32A32,32,0,0,1,128,160Zm109.94-52.79a8,8,0,0,0-3.89-5.4l-29.83-17-.12-33.62a8,8,0,0,0-2.83-6.08,111.91,111.91,0,0,0-36.72-20.67,8,8,0,0,0-6.46.59L128,41.85,97.88,25a8,8,0,0,0-6.47-.6A112.1,112.1,0,0,0,54.73,45.15a8,8,0,0,0-2.83,6.07l-.15,33.65-29.83,17a8,8,0,0,0-3.89,5.4,106.47,106.47,0,0,0,0,41.56,8,8,0,0,0,3.89,5.4l29.83,17,.12,33.62a8,8,0,0,0,2.83,6.08,111.91,111.91,0,0,0,36.72,20.67,8,8,0,0,0,6.46-.59L128,214.15,158.12,231a7.91,7.91,0,0,0,3.9,1,8.09,8.09,0,0,0,2.57-.42,112.1,112.1,0,0,0,36.68-20.73,8,8,0,0,0,2.83-6.07l.15-33.65,29.83-17a8,8,0,0,0,3.89-5.4A106.47,106.47,0,0,0,237.94,107.21Zm-15,34.91-28.57,16.25a8,8,0,0,0-3,3c-.58,1-1.19,2.06-1.81,3.06a7.94,7.94,0,0,0-1.22,4.21l-.15,32.25a95.89,95.89,0,0,1-25.37,14.3L134,199.13a8,8,0,0,0-3.91-1h-.19c-1.21,0-2.43,0-3.64,0a8.08,8.08,0,0,0-4.1,1l-28.84,16.1A96,96,0,0,1,67.88,201l-.11-32.2a8,8,0,0,0-1.22-4.22c-.62-1-1.23-2-1.8-3.06a8.09,8.09,0,0,0-3-3.06l-28.6-16.29a90.49,90.49,0,0,1,0-28.26L61.67,97.63a8,8,0,0,0,3-3c.58-1,1.19-2.06,1.81-3.06a7.94,7.94,0,0,0,1.22-4.21l.15-32.25a95.89,95.89,0,0,1,25.37-14.3L122,56.87a8,8,0,0,0,4.1,1c1.21,0,2.43,0,3.64,0a8.08,8.08,0,0,0,4.1-1l28.84-16.1A96,96,0,0,1,188.12,55l.11,32.2a8,8,0,0,0,1.22,4.22c.62,1,1.23,2,1.8,3.06a8.09,8.09,0,0,0,3,3.06l28.6,16.29A90.49,90.49,0,0,1,222.9,142.12Z',
    sliders: 'M40,88H73a32,32,0,0,0,62,0h81a8,8,0,0,0,0-16H135a32,32,0,0,0-62,0H40a8,8,0,0,0,0,16Zm64-24A16,16,0,1,1,88,80,16,16,0,0,1,104,64ZM216,168H199a32,32,0,0,0-62,0H40a8,8,0,0,0,0,16h97a32,32,0,0,0,62,0h17a8,8,0,0,0,0-16Zm-48,24a16,16,0,1,1,16-16A16,16,0,0,1,168,192Z',
    about: 'M128,24A104,104,0,1,0,232,128,104.11,104.11,0,0,0,128,24Zm0,192a88,88,0,1,1,88-88A88.1,88.1,0,0,1,128,216Zm16-40a8,8,0,0,1-8,8,16,16,0,0,1-16-16V128a8,8,0,0,1,0-16,16,16,0,0,1,16,16v40A8,8,0,0,1,144,176ZM112,84a12,12,0,1,1,12,12A12,12,0,0,1,112,84Z',
    branch: 'M232,64a32,32,0,1,0-40,31v17a8,8,0,0,1-8,8H96a23.84,23.84,0,0,0-8,1.38V95a32,32,0,1,0-16,0v66a32,32,0,1,0,16,0V144a8,8,0,0,1,8-8h88a24,24,0,0,0,24-24V95A32.06,32.06,0,0,0,232,64ZM64,64A16,16,0,1,1,80,80,16,16,0,0,1,64,64ZM96,192a16,16,0,1,1-16-16A16,16,0,0,1,96,192ZM200,80a16,16,0,1,1,16-16A16,16,0,0,1,200,80Z',
    search: 'M229.66,218.34l-50.07-50.06a88.11,88.11,0,1,0-11.31,11.31l50.06,50.07a8,8,0,0,0,11.32-11.32ZM40,112a72,72,0,1,1,72,72A72.08,72.08,0,0,1,40,112Z',
    plus: 'M224,128a8,8,0,0,1-8,8H136v80a8,8,0,0,1-16,0V136H40a8,8,0,0,1,0-16h80V40a8,8,0,0,1,16,0v80h80A8,8,0,0,1,224,128Z',
  };
  if (paths[name]) {
    svg.setAttribute('viewBox', '0 0 256 256');
    const path = document.createElementNS(SVG, 'path');
    path.setAttribute('fill', 'currentColor');
    path.setAttribute('d', paths[name]);
    svg.append(path);
  } else {
    const use = document.createElementNS(SVG, 'use');
    use.setAttribute('href', `/mds/icons/icons.svg#${name}`);
    svg.append(use);
  }
  return svg;
}

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
  infoOpen: false,
  repos: [],
  settingsPage: 'general',
  settings: null, // Yesman.Settings, for the General page
  about: null, // Yesman.About
  repoQuery: '',
  fetching: new Set(), // repositories being fetched
  addRepo: null, // the add form's draft while it is open
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

function convo(a) {
  let c = state.convos.get(keyOf(a));
  if (!c) state.convos.set(keyOf(a), (c = new Convo(a)));
  return c;
}

// Forget conversations and drafts of agents that are gone.
function pruneCaches() {
  const keys = new Set(state.snap.agents.map(keyOf));
  for (const map of [state.convos, state.drafts]) {
    for (const key of map.keys()) if (!keys.has(key)) map.delete(key);
  }
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
  if (state.snap.yesmen.length) list.append(h('p', { class: 'side-group' }, 'Yesmen'));
  for (const y of state.snap.yesmen) {
    const agents = state.snap.agents.filter((a) => a.yesman === y.name);
    const lead = agents.find((a) => a.role === 'lead');
    if (lead) list.append(agentRow(lead, 'agent-row--yesman', y.name));
    for (const w of agents.filter((a) => a.role === 'worker')) list.append(agentRow(w, 'agent-row--worker', w.name));
  }
  renderSideFoot();
}

// ---------- Main column ----------

function renderBar() {
  const bar = $('#bar');
  bar.replaceChildren();
  const a = selectedAgent();
  if (state.view.kind === 'settings') {
    const page = SETTINGS.find((s) => s.page === state.view.page);
    bar.append(h('nav', { class: 'mds-crumbs bar-crumbs', 'aria-label': 'Breadcrumb' },
      h('span', null, 'Settings'), h('span', { class: 'mds-crumb-sep', 'aria-hidden': 'true' }, '/'), h('strong', { 'aria-current': 'page' }, page.label)));
    return;
  }
  if (!a) {
    bar.append(h('div', { class: 'bar-title' }));
    return;
  }
  const y = yesmanOf(a.id);
  const title = a.role === 'lead' ? a.yesman : `${a.yesman} / ${a.name}`;
  const sub = [a.role === 'lead' ? 'lead' : 'worker', a.model, y?.path].filter(Boolean).join(' · ');
  bar.append(
    h('div', { class: 'bar-title' }, h('strong', null, title), h('span', null, sub)),
    h('span', { class: 'mds-status', 'data-status': a.status }, h('i', { class: 'mds-dot' }), h('span', null, STATUS[a.status] ?? a.status)),
    h('div', { class: 'bar-actions' },
      a.status !== 'stopped'
        ? h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Put this agent to sleep: end its pi process. It resumes its session when you next talk to it.', 'aria-label': 'Put agent to sleep', onclick: () => call('Stop', a.id).catch(fail) }, icon('power'))
        : null,
      a.role === 'worker'
        ? h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Remove worker', 'aria-label': 'Remove worker', onclick: () => removeWorker(a) }, icon('x'))
        : null,
      h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Workspace', 'aria-label': 'Toggle workspace panel', 'aria-pressed': String(state.infoOpen), onclick: toggleInfo }, icon('panel')),
    ),
  );
}

function renderBody() {
  const body = $('#body');
  // A refresh while typing, say in the search box, keeps the caret there.
  const active = body.contains(document.activeElement) ? document.activeElement : null;
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
  if (!dock || !a) return;
  const keep = dock.querySelector('textarea');
  const focused = keep && document.activeElement === keep;
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
  const send = h('button', { class: 'round', type: 'submit', 'aria-label': 'Send', title: working ? 'Send: delivered after its current step' : 'Send' }, icon('send'));
  const stop = working ? h('button', { class: 'round round--stop', type: 'button', 'aria-label': 'Stop', title: 'Stop the current run', onclick: () => call('Abort', a.id).catch(fail) }, icon('stop')) : null;
  const sync = () => {
    send.disabled = !textarea.value.trim();
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
    textarea,
    h('div', { class: 'composer-row' },
      h('span', { class: 'composer-hint' }, working ? 'Working. A new message steers it after its current step.' : 'Enter to send, Shift+Enter for a new line'),
      h('div', { class: 'ask-actions' }, stop, send)),
  );
  dock.append(form);
  sync();
  if (focused || !keep) textarea.focus();
}

async function sendMessage(a, textarea) {
  const text = textarea.value.trim();
  if (!text) return;
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
    else if (page === 'repositories') state.repos = await call('Repos');
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

function revealButton(path) {
  return h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => call('Reveal', path).catch(fail) }, icon('folder'), 'Reveal');
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
    settingsSection('Storage', 'Everything yesman keeps is in one folder. Start yesman with YESMAN_HOME set to keep it somewhere else.',
      preferenceRow('yesman folder', h('p', { class: 'path' }, v.home), revealButton(v.home)),
      preferenceRow('Archive', h('p', null, 'Workspaces of dismissed yesmen, with their worktrees. ', h('span', { class: 'path' }, v.archive)), revealButton(v.archive))),
    settingsSection('Agents', null,
      preferenceRow('pi',
        v.piError ? h('p', { class: 'danger' }, v.piError) : h('p', null, 'Every lead and worker runs this pi. Set YESMAN_PI to use another. ', h('span', { class: 'path' }, v.pi)),
        h('span', { class: 'mds-status', 'data-status': v.piError ? 'failed' : 'idle' }, h('i', { class: 'mds-dot' }), h('span', null, v.piError ? 'Not found' : 'Found')))),
  );
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
      row('pi', v ? (v.pi || h('span', { class: 'danger' }, v.piError)) : faint('Checking…')),
      row('MyGo', v ? v.mygo || faint('unknown') : faint('Checking…')),
      row('Magpie', v ? v.magpie || faint('unknown') : faint('Checking…')),
      row('Go', v ? v.go : faint('Checking…'))),
    h('div', null, h('button', { class: 'mds-link-button', type: 'button', onclick: () => call('OpenURL', 'https://github.com/idy/yesman').catch(fail) }, 'Source on GitHub')),
  );
}

// ---------- Repositories ----------

function ago(iso) {
  if (!iso) return 'never fetched';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return 'fetched just now';
  if (s < 3600) return `fetched ${Math.round(s / 60)} min ago`;
  if (s < 86400) return `fetched ${Math.round(s / 3600)} h ago`;
  return `fetched ${Math.round(s / 86400)} d ago`;
}

function renderRepositories(inner) {
  const search = state.repos.length > 1
    ? h('label', { class: 'search' }, icon('search'),
        h('input', { class: 'mds-input', id: 'repo-search', type: 'search', placeholder: 'Find a repository', 'aria-label': 'Find a repository', spellcheck: 'false', value: state.repoQuery,
          oninput: (e) => { state.repoQuery = e.currentTarget.value; renderRepoList(); } }))
    : null;
  inner.append(
    h('div', { class: 'mds-toolbar' },
      h('p', { class: 'toolbar-lead' }, 'Bare repositories the yesmen check worktrees out of. Only yesman writes here, and it fetches a repository whenever one is checked out.'),
      h('div', { class: 'mds-toolbar-actions' }, search,
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Reveal the repo pool', 'aria-label': 'Reveal the repo pool', onclick: () => call('Reveal', state.snap.pool).catch(fail) }, icon('folder')))),
    h('div', { class: 'card-list', id: 'repo-list', role: 'list', 'aria-label': 'Repositories' }),
    h('div', { id: 'repo-add' }));
  renderRepoList();
  renderRepoAdd();
}

function repoRow(r) {
  const worktrees = state.snap.yesmen.flatMap((y) => y.worktrees.filter((w) => w.repo === r.name).map((w) => ({ yesman: y.name, dir: w.dir || w.path })));
  const users = [...new Set(worktrees.map((w) => w.yesman))];
  const busy = state.fetching.has(r.name);
  const meta = [r.url, r.upstream ? `forked from ${r.upstream}` : null, r.defaultBranch ? `default ${r.defaultBranch}` : null, busy ? 'fetching…' : ago(r.fetchedAt)].filter(Boolean).join(' · ');
  return h('div', { class: 'card-row', role: 'listitem' },
    h('span', { class: 'card-row-icon' }, icon('branch')),
    h('div', { class: 'card-row-text' }, h('strong', null, r.name), h('span', null, meta)),
    users.length
      ? h('span', { class: 'card-row-aside', title: worktrees.map((w) => `${w.yesman}: ${w.dir}`).join('\n') },
          `${worktrees.length} worktree${worktrees.length === 1 ? '' : 's'} · ${users.join(', ')}`)
      : null,
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: `Fetch ${r.name}`, 'aria-label': `Fetch ${r.name}`, disabled: busy, onclick: () => fetchRepo(r.name) },
      busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('reload')));
}

function renderRepoList() {
  const list = $('#repo-list');
  if (!list) return;
  const q = state.repoQuery.trim().toLowerCase();
  const repos = q ? state.repos.filter((r) => `${r.name} ${r.url} ${r.upstream ?? ''}`.toLowerCase().includes(q)) : state.repos;
  if (!state.repos.length) {
    list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, 'No repositories yet'), h('span', null, 'Add one by URL, or let a yesman add the ones it needs.')));
  } else if (!repos.length) {
    list.replaceChildren(h('div', { class: 'card-empty' }, h('span', null, `No repository matches “${state.repoQuery.trim()}”.`)));
  } else {
    list.replaceChildren(...repos.map(repoRow));
  }
}

async function fetchRepo(name) {
  state.fetching.add(name);
  renderRepoList();
  try {
    await call('FetchRepo', name);
    state.repos = await call('Repos');
  } catch (err) {
    fail(err);
  } finally {
    state.fetching.delete(name);
    renderRepoList();
  }
}

// Adding opens a form under the list, as a row of its own, rather than a
// dialog: nothing else needs to stop while it clones.
function renderRepoAdd() {
  const box = $('#repo-add');
  if (!box) return;
  const draft = state.addRepo;
  if (!draft) {
    box.replaceChildren(h('button', { class: 'mds-link-button add-link', type: 'button', onclick: () => {
      state.addRepo = { url: '', upstream: '', error: '', busy: false };
      renderRepoAdd();
      $('#repo-url')?.focus();
    } }, icon('plus'), 'Add repository'));
    return;
  }
  const url = h('input', { class: 'mds-input mds-mono', id: 'repo-url', placeholder: 'https://github.com/owner/repo', required: true, spellcheck: 'false', autocomplete: 'off', value: draft.url, disabled: draft.busy, 'aria-invalid': draft.error ? 'true' : null,
    oninput: (e) => { draft.url = e.currentTarget.value; } });
  const upstream = h('input', { class: 'mds-input mds-mono', placeholder: 'https://github.com/original/repo', spellcheck: 'false', autocomplete: 'off', value: draft.upstream, disabled: draft.busy, 'aria-describedby': 'repo-upstream-hint',
    oninput: (e) => { draft.upstream = e.currentTarget.value; } });
  const cancel = () => { state.addRepo = null; renderRepoAdd(); $('.add-link')?.focus(); };
  box.replaceChildren(h('form', { class: 'add-form', 'aria-label': 'Add a repository',
    onkeydown: (e) => { if (e.key === 'Escape' && !draft.busy) cancel(); },
    onsubmit: async (e) => {
      e.preventDefault();
      draft.busy = true;
      draft.error = '';
      renderRepoAdd();
      try {
        const repo = await call('AddRepo', draft.url.trim(), draft.upstream.trim());
        state.addRepo = null;
        state.repos = await call('Repos');
        toast(`${repo.name} is in the pool`);
        renderRepoList();
      } catch (err) {
        draft.error = errorText(err);
        draft.busy = false;
      }
      renderRepoAdd();
      if (state.addRepo) $('#repo-url')?.focus();
    } },
    h('label', { class: 'field' }, h('span', null, 'Repository URL'), url),
    h('label', { class: 'field' }, h('span', null, 'Forked from', h('span', { class: 'mds-optional' }, 'optional')), upstream,
      h('span', { class: 'mds-hint', id: 'repo-upstream-hint' }, 'If the URL is your fork, the original repository. yesman fetches it as upstream, so agents can branch from upstream/main.')),
    draft.error ? h('p', { class: 'mds-hint', 'data-tone': 'error', role: 'alert' }, draft.error) : null,
    h('div', { class: 'add-actions' },
      h('button', { class: 'mds-button mds-button--small mds-button--subtle', type: 'button', disabled: draft.busy, onclick: cancel }, 'Cancel'),
      h('button', { class: 'mds-button mds-button--small mds-button--primary', type: 'submit', disabled: draft.busy },
        draft.busy ? [h('span', { class: 'mds-spinner on-accent', 'aria-hidden': 'true' }), 'Cloning…'] : 'Add'))));
}

// ---------- Workspace panel ----------

function toggleInfo() {
  state.infoOpen = !state.infoOpen;
  renderInfo();
  renderBar();
}

function renderInfo() {
  const info = $('#info');
  const a = selectedAgent();
  const y = a && yesmanOf(a.id);
  info.hidden = !state.infoOpen || !y;
  if (info.hidden) return;
  const workers = state.snap.agents.filter((x) => x.yesman === y.name && x.role === 'worker');

  const repoSelect = h('select', { class: 'mds-select', 'aria-label': 'Repository' },
    state.repos.length ? state.repos.map((r) => h('option', { value: r.name }, r.name)) : h('option', { value: '' }, 'The pool is empty'));
  const branch = h('input', { class: 'mds-input mds-mono', placeholder: `branch (default yesman/${y.name}/…)`, spellcheck: 'false', 'aria-label': 'Branch' });
  const checkout = h('button', { class: 'mds-button mds-button--small', type: 'submit', disabled: !state.repos.length }, 'Check out');

  info.replaceChildren(
    h('div', { class: 'info-top drag' }, h('strong', null, y.name)),
    h('section', { class: 'info-section' },
      h('h3', null, 'Workspace'),
      h('span', { class: 'path' }, y.path),
      h('div', null, h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => call('Reveal', y.path).catch(fail) }, icon('folder'), 'Reveal'))),
    h('section', { class: 'info-section' },
      h('h3', null, `Worktrees (${y.worktrees.length})`),
      y.worktrees.length
        ? y.worktrees.map((w) => h('div', { class: 'wt' },
            h('span', { class: 'wt-dir' }, w.dir || w.path),
            h('span', { class: 'wt-meta' }, `${w.repo} · ${w.detached ? `detached ${w.head?.slice(0, 8)}` : w.branch}`),
            h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: 'Reveal', 'aria-label': 'Reveal', onclick: () => call('Reveal', w.path).catch(fail) }, icon('folder'))))
        : h('span', { class: 'wt-meta' }, 'None yet. The agents check out what they need, or check one out here.'),
      h('form', { class: 'stack', onsubmit: async (e) => {
        e.preventDefault();
        checkout.disabled = true;
        try {
          const wt = await call('Checkout', y.name, { repo: repoSelect.value, branch: branch.value.trim() });
          toast(`Checked out ${wt.dir || wt.path}`);
          branch.value = '';
        } catch (err) {
          fail(err);
        } finally {
          checkout.disabled = false;
        }
      } }, repoSelect, branch, h('div', null, checkout))),
    h('section', { class: 'info-section' },
      h('h3', null, 'Knowledge'),
      h('span', { class: 'wt-meta' }, 'Notes every agent of this yesman reads and writes.'),
      h('div', null, h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => call('Reveal', `${y.path}/.yesman/knowledge/INDEX.md`).catch(fail) }, icon('file'), 'Reveal notes'))),
    h('section', { class: 'info-section' },
      h('h3', null, `Workers (${workers.length})`),
      workers.length
        ? workers.map((w) => h('div', { class: 'wt' }, h('span', { class: 'wt-dir' }, w.name), h('span', { class: 'wt-meta' }, `${STATUS[w.status] ?? w.status}${w.task ? ` · ${w.task.slice(0, 90)}` : ''}`)))
        : h('span', { class: 'wt-meta' }, 'The lead starts workers when the job splits up.')),
    h('section', { class: 'info-section' },
      h('div', null, h('button', { class: 'mds-button mds-button--small danger', type: 'button', onclick: () => dismiss(y.name) }, 'Dismiss yesman'))),
  );
}

// ---------- Actions ----------

function selectAgent(id) {
  state.view = { kind: 'agent', id };
  const a = agentById(id);
  if (!a) {
    render();
    return;
  }
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
    [state.snap, state.repos] = await Promise.all([call('State'), call('Repos')]);
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
  if (state.view.kind === 'agent' && !after) {
    refreshSoon();
    return;
  }
  if (after && (before?.status !== after.status || before?.model !== after.model || before?.detail !== after.detail || JSON.stringify(before?.dialog) !== JSON.stringify(after.dialog))) {
    renderBar();
    renderDock();
    renderWorking();
    if (before?.status === 'stopped' && after.status !== 'stopped' && !convo(after).loaded) selectAgent(after.id);
  }
  if (state.infoOpen) renderInfo();
}

function render() {
  $('.main').dataset.view = state.view.kind;
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
  $('#hire-button').addEventListener('click', async () => {
    const form = $('#hire-form');
    form.reset();
    const result = await openDialog($('#hire-dialog'));
    if (result === 'hire') hire(form.elements.name.value.trim());
  });
  buildSideFoot();
  document.addEventListener('click', (e) => {
    const link = e.target.closest('a[href]');
    if (!link) return;
    e.preventDefault();
    if (link.dataset.path) {
      call('Reveal', link.dataset.path).catch(fail);
      return;
    }
    if (/^(https?:|mailto:)/.test(link.getAttribute('href'))) call('OpenURL', link.getAttribute('href')).catch(fail);
  });
  mygo.on('agents', onAgents);
  mygo.on('agent-event', onAgentEvent);
  mygo.on('changed', refreshSoon);
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
