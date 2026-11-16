// yesman's window. The Go side owns all state; this page renders it and
// the live event stream of the agents' pi processes.
//
//   mygo.call("Yesman.<Method>", ...)  services in service.go
//   mygo.on("agents" | "agent-event" | "changed" | "clones" | "fetches" | "pi-packages" | "focus-agent" | "update" | "files", fn)

import { orderWorkers } from './sidebar.js';
import { needsYouItems, requestsOf, closedHandoffs, summary as requestSummary, typeLabel } from './needsyou.js';
import { messageActions, messageText, receiptText } from './message-actions.js';
import { mayRecallInComposer, restoredImages, supportsRecall } from './recall.js';
import { renderMarkdown } from './markdown.js';
import { project, originOf, FINISHED, dismissible } from './conversation.js';
import { $, h, icon, openModal } from './dom.js';
import { createFiles } from './files.js';
import { createInfo } from './info.js';
import { createBoard } from './board.js';
import { createContextViewer } from './context.js';
import { createModelPicker } from './models.js';
import { createPiPage } from './pi.js';
import { createCrew } from './crew.js';
import { createGrants } from './grants.js';
import { createUsagePage } from './usage.js';
import { createEnvPage } from './env.js';
import { formatSize } from './diff.js';
import { IMAGE_ACCEPT, imageFileError, readImageFile, imageURL, imagePayload } from './images.js';
import { detectPlatform } from './keys.js';
import { createShortcuts } from './shortcuts.js';
import { createContextMenu } from './contextmenu.js';
import { editorViewOf } from './editor.js';
import { createPalette } from './palette.js';
import { registerActions, paletteItems } from './actions.js';
import { createShortcutsPage } from './shortcuts-page.js';
import { toast, openDialog, initSelect } from './mds/js/magpie.js';
import { t, tn, tNodes, formatNumber, setLocale, locale, translatePage, LANGUAGES } from './i18n.js';

const mygo = window.mygo;
const call = (method, ...args) => mygo.call(`Yesman.${method}`, ...args);
const errorText = (err) => (err && (err.message || err.error)) || String(err);
const fail = (err) => toast(errorText(err), { duration: 6000 });

// The window's actions and their keys: shortcuts.js, with the actions in
// actions.js, the command palette and Settings › Shortcuts below.
const shortcuts = createShortcuts({ platform: detectPlatform(mygo), call, fail, t, builtIn: ['Mod+S'] });

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
// An agent whose last finished run ended on a model error, such as a usage
// limit, shows it while at rest, until a later run ends otherwise. A run in
// progress may be recovering and shows as working; a stopped run, a question
// or a request for the user is no failure. Each agent shows its own: a
// yesman's entry is its lead's.
const runFailed = (a) => !!a.runError && (a.status === 'idle' || a.status === 'stopped');
// An error's text as pi recorded it, or that it recorded none.
const runErrorText = (text) => (text?.trim() ? text : t('The model returned an error without a message.'));
const statusLabel = (a) => (asking(a) ? t('Asking {name}', { name: yesmanLabel(a.yesman) }) : runFailed(a) ? t('Last run ended with an error') : statusText(a.status));
const statusTone = (a) => (asking(a) ? 'asking' : runFailed(a) ? 'error' : a.status);

const state = {
  snap: { yesmen: [], agents: [] },
  view: { kind: 'none' },
  convos: new Map(),
  drafts: new Map(),
  attachments: new Map(),
  modelOptions: new Map(),
  pane: null, // the pane beside the conversation: 'files', 'board', 'needs-you' or null
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
  // The Needs-you request being opened in its conversation, from the Needs
  // you pane: { key, agent, pending (still to be located) }.
  request: null,
  resolving: new Set(), // keys of handoffs being closed
  resetting: new Set(), // yesmen whose request memory is being reset
};

const agentById = (id) => state.snap.agents.find((a) => a.id === id);
const yesmanOf = (id) => state.snap.yesmen.find((y) => y.name === id?.split('/')[0]);
const yesmanLabel = (name) => state.snap.yesmen.find((y) => y.name === name)?.displayName || name;
const selectedAgent = () => (state.view.kind === 'agent' ? agentById(state.view.id) : null);

// ---------- Conversations ----------

class Convo {
  constructor(a) {
    this.id = a.id;
    this.key = keyOf(a);
    this.messages = [];
    this.loaded = false;
    this.error = null;
    this.live = null; // { message }: the reply being written
    this.workOpen = new Set(); // keys of the work rows the user opened
    this.failuresOpen = new Set(); // keys of the folded failed runs the user opened
    this.tools = new Map(); // toolCallId -> { args, status, result }
    this.recalled = new Set();
    this.actions = new Set(); // in-flight exact-item actions
    this.deliveries = new Map(); // message ID -> server receipt (or local Sending only)
    // message ID -> { text, images } this window submitted, shown as sent until
    // a message is tied to it. Only in memory; see holdSent.
    this.sent = new Map();
    this.lastSent = null; // the message ID this window submitted last
    this.handled = handledOf(this.key); // IDs the person dismissed or resent unchanged; see saveHandled
    this.seq = 0;
    this.eventSeqs = new Set();
    this.piQueue = { known: false };
    this.queueSeq = 0;
    this.receiptSetSeq = 0;
    this.retainedDeliveryIDs = null;
  }
}

// A submission the person dismissed, or resent unchanged from the draft it
// left, is only a matter of what this page shows: its ID is kept in this
// browser by conversation, never sent anywhere, and its receipt does not
// change. Hiding covers what the person saw: when they send that ID again,
// or the server takes it up again (sending, queued, ready to return), the
// Hide is dropped, so the outcome of that new attempt shows (see
// reopenHandled). Only IDs the server still keeps are kept, for the
// conversations last used.
const HANDLED_STORE = 'yesman.handledSubmissions';
const readHandled = () => {
  try {
    const all = JSON.parse(localStorage.getItem(HANDLED_STORE) ?? '{}');
    return all && typeof all === 'object' && !Array.isArray(all) ? all : {};
  } catch { return {}; }
};
const handledOf = (key) => {
  const ids = readHandled()[key];
  return new Set((Array.isArray(ids) ? ids : []).filter((id) => typeof id === 'string' && /^[a-f0-9]{48}$/.test(id)));
};
function saveHandled(c) {
  const all = readHandled();
  delete all[c.key];
  if (c.handled.size) all[c.key] = [...c.handled].slice(-256);
  const keys = Object.keys(all);
  for (const key of keys.slice(0, Math.max(0, keys.length - 64))) delete all[key];
  try { localStorage.setItem(HANDLED_STORE, JSON.stringify(all)); } catch {
    // Unwritable: this page still hides what it hid until it reloads. Drop
    // what was stored so a reload never hides what this page shows again.
    try { localStorage.removeItem(HANDLED_STORE); } catch { /* Nothing stored can be read either. */ }
  }
}
// reopenHandled drops the Hide of a submission tried again; true if it had one.
function reopenHandled(c, id) {
  if (!c.handled.delete(id)) return false;
  saveHandled(c);
  return true;
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
const messageKey = (m) => originOf(m)?.submission ? `submission:${originOf(m).submission}` : `${m?.role}:${m?.timestamp}`;

const DELIVERY_LABELS = {
  sending: 'Sending…', accepted: 'Accepted · delivery unconfirmed',
  steering: 'Accepted for steering · delivery unconfirmed', queued: 'Accepted for follow-up · delivery unconfirmed',
  unconfirmed: 'Delivery unconfirmed', failed: 'Not sent', interrupted: 'Delivery interrupted', cancelled: 'Recalled',
};
const deliveryLabel = (d) => d.nativePending ? (d.state === 'queued' ? 'Waiting · follow-up' : 'Steering · waiting for input checkpoint') : DELIVERY_LABELS[d.state];

function pruneDeliveryRows(c, ids) {
  let changed = false, handled = false;
  for (const id of ids ?? []) {
    c.recalled.delete(id);
    c.sent.delete(id);
    handled = c.handled.delete(id) || handled;
    changed = c.deliveries.delete(id) || changed;
  }
  if (handled) saveHandled(c);
  if (changed) schedulePatch(c, { frame: true });
}

function applyDeliveryReceipt(c,e,seq) {
  if (Array.isArray(e.retained) && seq > c.receiptSetSeq) {
    c.receiptSetSeq = seq;
    c.retainedDeliveryIDs = new Set(e.retained);
    pruneDeliveryRows(c,[...c.deliveries.values()].filter(d => d.revision > 0 && !c.retainedDeliveryIDs.has(d.id)).map(d => d.id));
  }
  if (e.delivery) {
    if (c.retainedDeliveryIDs && seq <= c.receiptSetSeq && !c.retainedDeliveryIDs.has(e.delivery.id)) return;
    updateDelivery(c,e.delivery);
  }
  pruneDeliveryRows(c,e.removed);
}

function updateDelivery(c, d) {
  if (d?.agentSince && `${c.id}@${d.agentSince}` !== c.key) return;
  if (!d?.id || (c.deliveries.get(d.id)?.revision ?? -1) >= d.revision) return;
  c.deliveries.set(d.id, d);
  // Only an update that passed the checks above (this generation, a newer
  // revision) can say the server took a hidden submission up again.
  if (!dismissible(d)) reopenHandled(c, d.id);
  letGo(c);
  if (c.deliveries.size > 256) {
    const excess = [...c.deliveries.keys()].filter(id => id !== d.id).slice(0,c.deliveries.size-256);
    pruneDeliveryRows(c,excess);
  }
}

function copyActions(text, c = null, d = null) {
  const eligible = (d?.canCancel || d?.canRestore) && !c?.recalled.has(d.id);
  const actions = messageActions({ text,
    loadText: text == null && d?.originalText ? () => call('SubmissionText', c.id, d.id, d.agentSince) : null,
    copy: (value) => call('CopyValue', value), done: () => toast(t('Copied.')), fail,
    recall: eligible ? () => recallMessage(c, d.id) : null, restoring: d?.canRestore,
    promote: d?.canPromote && d.nativePending && d.state === 'queued' ? () => promoteMessage(c, d.id) : null,
    busy: c?.actions.has(d?.id), dismiss: c && d && dismissible(d) ? () => dismissReceipt(c, d.id) : null });
  actions._copyText = text;
  actions._recall = `${!!d?.canCancel}:${!!d?.canRestore}:${!!d?.originalText}`;
  return actions;
}

function refreshDelivery(c, d) { if (d) schedulePatch(c, { frame: true }); }

// The person is done with a submission nothing waits on: its row leaves this
// page (and focus moves on to the next such row, or the input).
function dismissReceipt(c, id) {
  const d = c.deliveries.get(id);
  if (!d || !dismissible(d)) return;
  const el = document.querySelector(`#thread > [data-delivery="${CSS.escape(id)}"]`);
  const focused = !!el?.contains(document.activeElement);
  const after = focused ? [...document.querySelectorAll('#thread > .msg-delivery .message-dismiss')]
    .map((b) => b.closest('.msg-delivery')?.dataset.delivery).filter((other) => other && other !== id &&
      (el.compareDocumentPosition(document.querySelector(`#thread > [data-delivery="${CSS.escape(other)}"]`)) & Node.DOCUMENT_POSITION_FOLLOWING)) : [];
  c.handled.add(id);
  saveHandled(c);
  schedulePatch(c);
  if (!focused) return;
  const next = after.length ? document.querySelector(`#thread > [data-delivery="${CSS.escape(after[0])}"] .message-dismiss`) : null;
  (next ?? document.querySelector('.composer textarea'))?.focus({ preventScroll: true });
}

async function promoteMessage(c, id) {
  const a = selectedAgent();
  const receipt = c.deliveries.get(id);
  if (!a || keyOf(a) !== c.key || !receipt?.nativePending || !receipt.canPromote || receipt.state !== 'queued' || c.actions.has(id)) return;
  c.actions.add(id);
  schedulePatch(c);
  const current = () => state.convos.get(c.key) === c && keyOf(agentById(a.id) ?? {}) === c.key;
  try {
    const result = await call('PromoteSubmission', a.id, id, a.since);
    if (!current()) return;
    updateDelivery(c, result.delivery);
  } catch (error) {
    if (current() && keyOf(selectedAgent() ?? {}) === c.key) fail(t(errorText(error)));
  } finally {
    c.actions.delete(id);
    if (current()) schedulePatch(c);
  }
}

async function recallMessage(c, id) {
  const a = selectedAgent();
  if (!a || keyOf(a) !== c.key) return;
  const key = c.key;
  const attachments = state.attachments.get(key);
  if (!attachments || attachments.sending || attachments.reading || attachments.recalling) return;
  if ((state.drafts.get(key) ?? '') !== '' || attachments.items.length) return fail(t('Send or clear the current draft before recalling a message.'));
  const receipt = c.deliveries.get(id);
  if ((!receipt?.canCancel && !receipt?.canRestore) || c.recalled.has(id) || c.actions.has(id)) return;
  c.actions.add(id);
  schedulePatch(c);
  attachments.recalling = true;
  let focusAllowed = true;
  const movedFocus = (event) => { if (!event.target.matches('.composer textarea')) focusAllowed = false; };
  document.addEventListener('focusin', movedFocus);
  const current = () => state.attachments.get(key) === attachments && keyOf(agentById(a.id) ?? {}) === key;
  const redraw = () => { if (current() && keyOf(selectedAgent() ?? {}) === key) renderDock(); };
  redraw();
  try {
    const result = await call('CancelSubmission', a.id, id, a.since);
    if (!current()) return;
    updateDelivery(c, result.delivery);
    refreshDelivery(c, c.deliveries.get(id));
    if (result.delivery?.id !== id || result.delivery?.state !== 'cancelled' || typeof result.payload?.text !== 'string') throw new Error(t('Cancellation could not be confirmed.'));
    const images = restoredImages(result.payload.images ?? []);
    if ((state.drafts.get(key) ?? '') !== '' || attachments.items.length) throw new Error(t('The message was recalled. Clear the current draft to return it to the input.'));
    state.drafts.set(key, result.payload.text);
    c.recalled.add(id);
    attachments.items = images;
    attachments.submissionID = null;
    attachments.submission = null;
    attachments.retryOf = null;
    const d = c.deliveries.get(id);
    if (d) { d.canCancel = false; d.canRestore = false; d.originalText = false; refreshDelivery(c, d); }
    call('AckRecall', a.id, id, a.since).catch(() => {});
  } catch (error) {
    if (current() && keyOf(selectedAgent() ?? {}) === key) fail(t(errorText(error)));
  } finally {
    document.removeEventListener('focusin', movedFocus);
    attachments.recalling = false;
    c.actions.delete(id);
    if (current()) schedulePatch(c);
    if (current() && keyOf(selectedAgent() ?? {}) === key) renderDock({ focusComposer: focusAllowed });
  }
}

// deliveryStatus is the state of a submission still open, lightly, under it.
function deliveryStatus(d) {
  const label = deliveryLabel(d);
  const title = d.nativePending ? (d.state === 'queued' ? t('This input is still in pi’s follow-up queue.') : t('Steering is read at the next eligible input checkpoint, after the current response and tool batch.'))
    : d.state === 'cancelled' ? t('This input was removed from the queue.')
      : d.error || (d.state === 'sending' ? null : t('pi has not reported whether this particular message was taken for processing.'));
  return h('div', { class: 'delivery-status', role: 'status', 'aria-live': 'polite', 'data-state': d.state,
    hidden: !label, title },
  label ? t(label) : '');
}

// receiptEl shows a submission the conversation does not show yet as the
// person's message, with its state under it: exactly as this window sent it
// when it has that, or else what the receipt keeps (a preview and the number
// of images), never a guess at the original.
const receiptForm = (d, sent) => `${sent ? 'sent' : 'kept'}:${d.text ?? ''}:${d.imageCount ?? 0}`;

function receiptEl(c, d, { sent } = {}) {
  const el = receiptBody(d, sent);
  const status = el.querySelector('.delivery-status');
  status.remove();
  el.append(h('div', { class: 'message-footer' }, status, copyActions(sent?.text ?? receiptText(d), c, d)));
  if (!sent && d.originalPayload) hydrateReceipt(c, el, d);
  el._variant = receiptForm(d, sent);
  return el;
}

// A visible row can retrieve originals from the Manager's bounded memory.
// Keep them only in this row, never another persistent or conversation cache.
async function hydrateReceipt(c, el, d) {
  el._hydration = true;
  try {
    const payload = await call('SubmissionPayload', c.id, d.id, d.agentSince);
    const current = c.deliveries.get(d.id);
    if (!el.isConnected || state.convos.get(c.key) !== c || keyOf(selectedAgent() ?? {}) !== c.key || !current?.originalPayload || typeof payload?.text !== 'string' || !Array.isArray(payload.images)) return;
    el._payload = payload;
    el.querySelector('.msg-user').replaceWith(userBubble({ content: [{ type: 'text', text: payload.text }, ...payload.images] }));
    el.querySelector('.delivery-image-count')?.remove();
    el.querySelector('.message-actions')?.replaceWith(copyActions(payload.text, c, current));
    el.dataset.original = 'true';
  } catch { /* Missing after intake/retirement stays an honest bounded preview. */ }
}

const imageNote = (n) => h('div', { class: 'delivery-image-count' }, tn(n, '{n} image submitted', '{n} images submitted'));

function receiptBody(d, sent) {
  const imageCount = d.imageCount ?? d.images?.length ?? 0;
  const text = sent ? sent.text : d.text ? (d.preview ? `${d.text}…` : d.text) : '';
  const images = (sent ? sent.images : d.images ?? []).filter((image) => imageURL(image));
  return h('div', { class: 'msg-delivery', 'data-delivery': d.id, 'data-sending': String(d.state === 'sending') },
    h('div', { class: 'msg-user' }, text || (imageCount && !images.length ? t('Attached images') : ''),
      images.length ? h('div', { class: 'message-images' }, images.map((image) => h('img', { src: imageURL(image), alt: t('Attached image'), loading: 'lazy' }))) : null),
    !images.length && imageCount ? imageNote(imageCount) : null, deliveryStatus(d));
}

// receiptEl brought up to date: a receipt changing state keeps its element
// (and what is selected in it) unless it changes form.
function updateReceipt(c, el, d, options = {}) {
  if (el._variant === receiptForm(d, options.sent) && !d.images?.length) {
    el.dataset.sending = String(d.state === 'sending');
    el.querySelector('.delivery-status').replaceWith(deliveryStatus(d));
    el.querySelector('.message-actions')?.replaceWith(copyActions(el._payload?.text ?? options.sent?.text ?? receiptText(d), c, d));
    if (!options.sent && d.originalPayload && !el._hydration) hydrateReceipt(c, el, d);
    return el;
  }
  return receiptEl(c, d, options);
}

const receiptSig = (d, sent) => `${d.state}:${d.revision}:${d.error ?? ''}:${d.images?.length ?? 0}:${sent ? 1 : 0}:${!!d.originalPayload}:${!!d.canPromote}:${!!d.canCancel}:${!!d.canRestore}`;

function renderQueue(c) {
  const thread = $('#thread');
  if (!thread || keyOf(selectedAgent() ?? {}) !== c.key) return;
  thread.querySelector('.pi-queue-status')?.remove();
  const q = c.piQueue;
  if (q.known && q.steering + q.followUp > 0) {
    const el = h('div', { class: 'pi-queue-status note note--quiet', role: 'status', 'aria-live': 'polite',
      title: q.recallSupported ? t('These counts describe pi’s whole queue, not an individual submission.') : t('This pi runtime does not support recalling individual queued messages.') },
      t('pi queue: {steering} steering · {followUp} follow-up', { steering: q.steering, followUp: q.followUp }),
      !q.recallSupported ? h('div', null, t('Individual recall is unavailable in this pi runtime.')) : null);
    const working = thread.querySelector('.working-live');
    working ? working.before(el) : thread.append(el);
  }
}

async function loadConvo(c) {
  c.queue = [];
  try {
    const data = await call('Messages', c.id);
    c.messages = data?.messages ?? [];
    const local = [...c.deliveries.values()].filter((d) => d.revision === 0);
    c.deliveries = new Map();
    for (const d of data?.deliveries ?? []) updateDelivery(c,d);
    for (const d of local) if (!c.deliveries.has(d.id)) {
      const request = state.attachments.get(c.key);
      const sending = request?.sending && request.submissionID === d.id;
      c.deliveries.set(d.id, { ...d, state: sending ? 'sending' : 'unconfirmed' });
    }
    letGo(c);
    c.seq = data?.seq ?? 0;
    c.eventSeqs.clear();
    c.piQueue = data?.queue ?? { known: false };
    c.queueSeq = c.seq;
    c.receiptSetSeq = c.seq;
    c.retainedDeliveryIDs = new Set((data?.deliveries ?? []).map(d => d.id));
    c.tools.clear();
    for (const message of c.messages) indexMessage(c, message);
    for (const id of c.sent.keys()) if (!c.deliveries.has(id)) c.sent.delete(id);
    const kept = c.handled.size;
    for (const id of c.handled) if (!c.deliveries.has(id)) c.handled.delete(id);
    if (c.handled.size !== kept) saveHandled(c);
    c.live = null;
    c.loaded = true;
    c.error = null;
    // The reply being written when the history was read...
    for (const ev of data?.live ?? []) applyEvent(c, ev.data, true);
    // ...then what arrived since. An event numbered at or before the
    // history is in it, except a message that ended in between.
    const seen = new Set(c.messages.map(messageKey));
    for (const ev of c.queue.sort((a,b) => a.seq-b.seq)) {
      const e = ev.data;
      if (ev.seq <= (data?.seq ?? 0) && !(e.type === 'message_end' && !seen.has(messageKey(e.message)))) continue;
      if (e.type === 'message_end' && seen.has(messageKey(e.message))) continue;
      if (c.eventSeqs.has(ev.seq)) continue;
      applyEvent(c, e, true,ev.seq);
      c.eventSeqs.add(ev.seq);
      if (e.type === 'queue_update' || e.type === 'delivery_queue') c.queueSeq = Math.max(c.queueSeq,ev.seq);
      if (e.type === 'message_end') seen.add(messageKey(e.message));
    }
  } catch (err) {
    c.error = errorText(err);
  }
  c.queue = null;
  const a = selectedAgent();
  if (a && keyOf(a) === c.key) renderThread();
}

const textOf = messageText;

// Tool state is built from history and live events; rendering only reads it.
function indexTool(c, block) {
  const tool = c.tools.get(block.id) ?? { status: 'pending' };
  tool.args = block.arguments ?? tool.args ?? {};
  c.tools.set(block.id, tool);
}

function indexMessage(c, message) {
  // A message tied to a submission shows it from here on.
  const submission = originOf(message)?.submission;
  if (submission) c.sent.delete(submission);
  if (message.role === 'assistant') {
    for (const block of message.content ?? []) {
      if (block.type === 'toolCall') indexTool(c, block);
    }
  } else if (message.role === 'toolResult') {
    // The result message is final: an extension may have changed what the
    // execution returned, its output and whether it failed, as it ended.
    const tool = c.tools.get(message.toolCallId) ?? {};
    const status = message.isError ? 'error' : 'done', result = resultText(message);
    if (tool.status !== status || tool.result !== result) tool.revision = (tool.revision ?? 0) + 1;
    Object.assign(tool, { status, result, final: true });
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

const toolMark = (status) => icon(status === 'done' ? 'check' : status === 'error' ? 'alert' : 'spinner', 'mds-icon tool-mark');

function toolEl(c, block) {
  const tool = c.tools.get(block.id) ?? { status: 'pending' };
  const el = h('details', { class: 'tool', 'data-tool': block.id, 'data-status': tool.status });
  el.append(
    h('summary', null,
      toolMark(tool.status),
      h('span', { class: 'tool-name' }, block.name.replace(/^yesman_/, 'yesman · ')),
      h('span', { class: 'tool-sum' }, toolSummary(tool.args ?? block.arguments)),
      h('span', { class: 'tool-state' }, tool.status === 'error' ? t('failed') : '')),
  );
  el.addEventListener('toggle', () => {
    if (!el.open) return;
    const cur = c.tools.get(block.id) ?? {};
    el.querySelector('.tool-body')?.remove();
    el.append(
      h('div', { class: 'tool-body' },
        h('pre', null, JSON.stringify(cur.args ?? block.arguments ?? {}, null, 2)),
        cur.result ? h('pre', null, cur.result) : null),
    );
  });
  return el;
}

function refreshTool(c, el, id) {
  const tool = c.tools.get(id);
  if (!tool) return;
  el.dataset.status = tool.status;
  el.querySelector('.tool-mark')?.replaceWith(toolMark(tool.status));
  el.querySelector('.tool-state').textContent = tool.status === 'error' ? t('failed') : '';
  el.querySelector('.tool-sum').textContent = toolSummary(tool.args);
  if (el.open) el.dispatchEvent(new Event('toggle'));
}

// ---------- Conversation rows ----------
//
// conversation.js projects a conversation into rows: what people and the
// agent said, and one folded row for the work between. They are drawn and
// patched here by key, the same from history and live, so streaming updates
// only what changed and keeps what is open, focused and scrolled.

const md = (text) => h('div', { class: 'md', html: renderMarkdown(text) });

// A message from before yesman recorded who wrote what is shown as it was:
// these are the forms yesman's own messages had then. It is never folded.
function legacyNoteEl(text) {
  const report = text.match(/^\[yesman\] Worker "([^"]+)" finished a run\. Its final message:\n\n([\s\S]*)$/);
  if (report) {
    return h('details', { class: 'note', open: true },
      h('summary', null, t('Report from worker {name}', { name: report[1] })), md(report[2]));
  }
  const fromLead = text.match(/^\[from your lead\] ([\s\S]*)$/);
  if (fromLead) return h('div', { class: 'note' }, h('strong', null, t('From the lead')), md(fromLead[1]));
  // What yesman tells the lead: a worker's question, how a run ended
  // when it did not finish, or the tasks still open.
  const notice = text.match(/^\[yesman\] ([\s\S]*)$/);
  if (notice) return h('div', { class: 'note note--quiet' }, md(notice[1]));
  return null;
}

function userBubble(m) {
  const images = Array.isArray(m.content) ? m.content.filter((b) => b.type === 'image' && imageURL(b)) : [];
  return h('div', { class: 'msg-user' }, textOf(m.content),
    images.length ? h('div', { class: 'message-images' }, images.map((image) =>
      h('img', { src: imageURL(image), alt: t('Attached image'), loading: 'lazy' }))) : null);
}

function userRowEl(row, c) {
  // yesman wrote its notices as text alone: a message with images is a
  // person's whatever its text says, and is shown in full with them.
  const images = Array.isArray(row.message.content) && row.message.content.some((b) => b?.type === 'image');
  const legacy = !row.origin && !images && legacyNoteEl(textOf(row.message.content));
  if (legacy) return legacy;
  const bubble = userBubble(row.message);
  bubble.append(copyActions(textOf(row.message.content)));
  // Tied to its submission: pi has it, so it reads as any message, without a status.
  return row.delivery ? h('div', { class: 'msg-delivery', 'data-delivery': row.delivery.id, 'data-bound': 'true' }, bubble) : bubble;
}

function leadRowEl(row) {
  const text = textOf(row.message.content);
  return h('div', { class: 'note note--lead' }, h('strong', null, t('From the lead')),
    md(row.origin.kind === 'message' ? text.replace(/^\[from your lead\] /, '') : text));
}

function noteRowEl(row) {
  if (row.stopped) return h('div', { class: 'note note--quiet' }, t('Stopped.'));
  if (row.summary) return h('div', { class: 'note note--quiet' }, t('Earlier conversation was summarized to save context.'));
  return h('div', { class: row.tone === 'error' ? 'note note--error' : 'note note--quiet' }, row.tone === 'error' ? runErrorText(row.text) : row.text);
}

// The work row: one line saying what happened, opened to read it in order.
const WORK_STATES = {
  done: 'Done', ended: 'Ended', stopped: 'Stopped', error: 'Ended with an error',
  interrupted: 'Interrupted', unfinished: 'Unfinished',
};
const WORK_MARKS = { running: 'spinner', waiting: 'info', stopped: 'stop', error: 'alert', interrupted: 'alert', unfinished: 'alert' };

function workFacts({ thinking, tools, failed, workers, reports, notices }) {
  return [
    thinking && tn(thinking, '{n} thought', '{n} thoughts'),
    tools && tn(tools, '{n} tool call', '{n} tool calls'),
    failed && tn(failed, '{n} failed', '{n} failed'),
    workers && tn(workers, '{n} worker started', '{n} workers started'),
    reports && tn(reports, '{n} worker report', '{n} worker reports'),
    notices && tn(notices, '{n} notice', '{n} notices'),
  ].filter(Boolean).join(' · ');
}

function workLabel(row, a) {
  if (row.state === 'running') return workingLabel(a);
  if (row.state === 'waiting') return a.role === 'worker' ? statusLabel(a) : t('Waiting for an answer');
  return t(WORK_STATES[row.state]);
}

const reportBody = (text) => text.replace(/^\[yesman\] Worker "[^"]*" finished a run\. Its final message:\n\n/, '');

function workItemEl(c, item) {
  if (item.type === 'thinking') return h('div', { class: 'work-thinking md', html: renderMarkdown(item.text) });
  if (item.type === 'tool') return toolEl(c, item.block);
  const text = textOf(item.message.content);
  const worker = item.origin.worker;
  if (item.origin.kind === 'report') {
    const el = h('details', { class: 'work-report' },
      h('summary', null, icon('fileText', 'mds-icon work-report-mark'), h('span', null, t('Report from worker {name}', { name: worker }))));
    el.addEventListener('toggle', () => { if (el.open && !el.querySelector('.md')) el.append(md(reportBody(text))); });
    return el;
  }
  return h('div', { class: 'work-notice' },
    item.origin.kind === 'question' ? h('strong', null, t('Question from worker {name}', { name: worker })) : null,
    md(text.replace(item.origin.kind === 'question' ? /^\[yesman\] Worker "[^"]*" asks:\n\n/ : /^\[yesman\] /, '')));
}

function itemSig(c, item) {
  if (item.type === 'thinking') return item.live ? `l:${item.text}` : 'f';
  if (item.type === 'tool') {
    const tool = c.tools.get(item.block.id);
    return `${tool?.status ?? 'pending'}:${tool?.revision ?? 0}`;
  }
  return 'f';
}

// patchChildren keeps parent's keyed children in the order of entries,
// reusing each element of the same kind: update brings it up to date in
// place, or returns a replacement. Children without the key stay last.
function patchChildren(parent, attr, entries, { make, sig, update }) {
  const old = new Map();
  for (const el of parent.querySelectorAll(`:scope > [${attr}]`)) old.set(el.getAttribute(attr), el);
  let prev = null;
  for (const entry of entries) {
    let el = old.get(entry.key);
    const s = sig(entry);
    if (el && el._kind === entry.type) {
      old.delete(entry.key);
      if (el._sig !== s || entry.type === 'work' || entry.type === 'failures' || entry.type === 'failure') {
        const fresh = update(el, entry);
        if (fresh && fresh !== el) {
          el.replaceWith(fresh);
          el = fresh;
        }
      }
    } else {
      el = make(entry);
    }
    el.setAttribute(attr, entry.key);
    el._kind = entry.type;
    el._sig = s;
    // Only keyed children move; other elements keep their places.
    let at = prev ? prev.nextElementSibling : parent.firstElementChild;
    while (at && at !== el && !at.hasAttribute(attr)) at = at.nextElementSibling;
    if (at !== el) {
      if (at) at.before(el);
      else if (prev) prev.after(el);
      else parent.prepend(el);
    }
    prev = el;
  }
  for (const el of old.values()) el.remove();
}

function patchWorkItems(c, body, items) {
  patchChildren(body, 'data-item', items, {
    make: (item) => workItemEl(c, item),
    sig: (item) => itemSig(c, item),
    update: (el, item) => {
      if (item.type === 'tool') refreshTool(c, el, item.block.id);
      else if (item.type === 'thinking') el.innerHTML = renderMarkdown(item.text);
      else return workItemEl(c, item);
    },
  });
}

function workEl(c, row) {
  const el = h('details', { class: 'work' });
  el.append(
    h('summary', null, h('span', { class: 'work-mark' }), h('span', { class: 'work-label' }), h('span', { class: 'work-facts' }), icon('chevron', 'mds-icon work-caret')),
    h('div', { class: 'work-body' }));
  el.open = c.workOpen.has(row.key);
  el.addEventListener('toggle', () => {
    if (el.open) c.workOpen.add(row.key);
    else c.workOpen.delete(row.key);
    if (el.open) patchWorkItems(c, el.lastElementChild, el._items ?? []);
  });
  updateWork(c, el, row);
  return el;
}

function updateWork(c, el, row) {
  const a = agentById(c.id);
  const label = workLabel(row, a ?? {}), facts = workFacts(row.counts);
  const failed = row.counts.failed > 0 && (row.state === 'done' || row.state === 'ended');
  const mark = WORK_MARKS[row.state] ?? (failed ? 'alert' : 'check');
  const summary = `${row.state}|${mark}|${label}|${facts}`;
  if (el._summary !== summary) {
    el._summary = summary;
    el.dataset.state = row.state;
    el.toggleAttribute('data-failed', failed);
    const s = el.firstElementChild;
    s.children[0].replaceChildren(icon(mark, 'mds-icon'));
    s.children[1].textContent = label;
    s.children[2].textContent = facts;
    s.title = t('Work between replies: thinking, tool calls and notices. Open to read them in order.');
  }
  el._items = row.items;
  if (el.open) patchWorkItems(c, el.lastElementChild, row.items);
  return el;
}

// Runs in a row that ended on nothing but an error: one line saying how many
// and on what, opened to read each error in full under its count, when each
// run failed and the work before it. It is no alert: the error is history.
function failuresEl(c, row) {
  const el = h('details', { class: 'failures' });
  el.append(
    h('summary', null, icon('error', 'mds-icon failures-mark'), h('span', { class: 'failures-label' }), h('span', { class: 'failures-facts' }), icon('chevron', 'mds-icon failures-caret')),
    h('div', { class: 'failures-body' }));
  el.open = c.failuresOpen.has(row.key);
  el.addEventListener('toggle', () => {
    if (el.open) c.failuresOpen.add(row.key);
    else c.failuresOpen.delete(row.key);
    if (el.open && el._row) patchFailures(c, el.lastElementChild, el._row);
  });
  return updateFailures(c, el, row);
}

const oneLine = (text) => text.trim().replace(/\s+/g, ' ');

function updateFailures(c, el, row) {
  const label = tn(row.runs.length, 'Ended with an error', 'Ended with an error {n} times');
  const facts = row.errors.length === 1 ? oneLine(runErrorText(row.errors[0].text)) : tn(row.errors.length, '{n} error', '{n} different errors');
  const summary = `${label}|${facts}`;
  if (el._summary !== summary) {
    el._summary = summary;
    const s = el.firstElementChild;
    s.children[1].textContent = label;
    s.children[2].textContent = facts;
    s.title = t('Runs in a row that ended on nothing but an error. Open to read each error, when it happened and the work before it.');
  }
  el._row = row;
  if (el.open) patchFailures(c, el.lastElementChild, row);
  return el;
}

function patchFailures(c, body, row) {
  patchChildren(body, 'data-error', row.errors.map((e) => ({ type: 'failure', key: e.key, error: e })), {
    make: (entry) => updateFailure(c, failureEl(entry.error), entry.error),
    sig: () => '',
    update: (el, entry) => updateFailure(c, el, entry.error),
  });
}

const failureTime = (ms) => (ms == null ? '' : new Intl.DateTimeFormat(locale(), { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(ms)));

// One error, as pi recorded it, with the runs that ended on it.
function failureEl(e) {
  const copy = h('button', {
    class: 'message-copy', type: 'button', title: t('Copy error text'), 'aria-label': t('Copy error text'),
    onclick: async () => {
      try {
        await call('CopyValue', copy._text);
        toast(t('Copied.'));
      } catch (err) {
        fail(err);
      }
    },
  }, icon('copy'));
  return h('div', { class: 'failure' },
    h('div', { class: 'failure-head' }, h('span', { class: 'failure-count' }), h('span', { class: 'failure-source' }), copy),
    h('pre', { class: 'failure-text' }),
    h('div', { class: 'failure-runs' }));
}

function updateFailure(c, el, e) {
  const [head, text, runs] = el.children;
  head.children[0].textContent = tn(e.runs.length, 'Once', '{n} times');
  head.children[1].textContent = [e.provider, ...e.models].filter(Boolean).join(' · ');
  const said = runErrorText(e.text);
  head.children[2]._text = said;
  text.classList.toggle('failure-text--blank', !!e.blank);
  if (text.textContent !== said) text.textContent = said;
  patchChildren(runs, 'data-run', e.runs.map((run) => ({ type: 'run', key: run.key, run })), {
    make: (entry) => h('div', { class: 'failure-run' },
      h('time', null, failureTime(entry.run.note.failure.at)),
      entry.run.work ? workEl(c, entry.run.work) : h('span', { class: 'failure-run-empty' }, t('No work before the error'))),
    sig: () => '',
    update: (el, entry) => {
      const work = el.querySelector(':scope > .work');
      if (work && entry.run.work) updateWork(c, work, entry.run.work);
      return el;
    },
  });
  return el;
}

function rowEl(c, row) {
  switch (row.type) {
    case 'user': return userRowEl(row, c);
    case 'lead': return leadRowEl(row);
    case 'text': return h('div', { class: 'msg-assistant' }, md(row.text));
    case 'note': return noteRowEl(row);
    case 'work': return workEl(c, row);
    case 'failures': return failuresEl(c, row);
    case 'receipt': return receiptEl(c, row.delivery, { sent: row.sent });
    default: return h('div');
  }
}

function rowSig(row) {
  switch (row.type) {
    case 'user': return row.delivery ? `bound:${row.delivery.id}` : '';
    case 'text': return row.live ? `l:${row.text}` : 'f';
    case 'receipt': return receiptSig(row.delivery, row.sent);
    default: return 'f';
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

// patchThread brings the shown thread up to date with its conversation.
function patchThread(c) {
  const thread = $('#thread');
  const a = selectedAgent();
  if (!thread || !a || keyOf(a) !== c.key || !c.loaded || c.error) return;
  const scroller = $('#scroll');
  if (scroller) scroller.dataset.stick = atBottom(scroller) ? 'true' : 'false';
  const rows = project(c, a);
  if (rows.length) thread.querySelector('.thread-empty')?.remove();
  patchChildren(thread, 'data-row', rows, {
    make: (row) => rowEl(c, row),
    sig: (row) => `${rowSig(row)}:${c.actions.has(row.delivery?.id)}`,
    update: (el, row) => {
      if (row.type === 'work') return updateWork(c, el, row);
      if (row.type === 'failures') return updateFailures(c, el, row);
      if (row.type === 'text') {
        el.firstElementChild.innerHTML = renderMarkdown(row.text);
        return el;
      }
      if (row.type === 'receipt') return updateReceipt(c, el, row.delivery, { sent: row.sent });
      return rowEl(c, row);
    },
  });
  renderWorking(rows);
  renderQueue(c);
  scrollDown();
}

// Streaming deltas and tool output draw once per frame; other records,
// which come one at a time, draw at once.
let patchFrame = 0, patchConvo = null;
function schedulePatch(c, { frame = false } = {}) {
  if (keyOf(selectedAgent() ?? {}) !== c.key) return;
  if (!frame) return patchThread(c);
  patchConvo = c;
  if (patchFrame) return;
  patchFrame = requestAnimationFrame(() => {
    patchFrame = 0;
    const cv = patchConvo;
    patchConvo = null;
    if (cv) patchThread(cv);
  });
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
  } else if (!c.messages.length && !c.live && !c.deliveries.size) {
    const y = yesmanOf(a.id);
    thread.append(
      a.role === 'lead'
        ? h('div', { class: 'thread-empty' }, h('strong', null, t('{name} is ready', { name: yesmanLabel(a.yesman) })),
            t('Tell {name} what you want done. It works in {path}, checks out repos from the pool, and starts workers when the job splits up.', { name: yesmanLabel(a.yesman), path: y?.path ?? t('its workspace') }))
        : h('div', { class: 'thread-empty' }, h('strong', null, a.name), t('This worker has not started yet.')),
    );
  }
  if (c.loaded && !c.error) {
    patchThread(c);
  } else {
    renderWorking([]);
    renderQueue(c);
  }
  scrollDown(true);
}

// renderWorking shows what the agent is doing at the foot of its thread,
// unless the work row there already says it.
function renderWorking(rows) {
  const thread = $('#thread');
  if (!thread) return;
  const a = selectedAgent();
  const last = rows?.findLast((r) => r.type !== 'receipt');
  const show = a?.status === 'working' && last?.state !== 'running';
  let el = thread.querySelector('.working-live');
  if (!show) {
    el?.remove();
    return;
  }
  if (!el) el = h('div', { class: 'working working-live', 'data-status': 'working' }, h('i', { class: 'mds-dot', 'aria-hidden': 'true' }), h('span', { class: 'working-label' }));
  el.querySelector('.working-label').textContent = workingLabel(a);
  if (thread.lastElementChild !== el) thread.append(el);
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
  const agent = agentById(id);
  if (ev.agentSince && agent && `${id}@${ev.agentSince}` !== keyOf(agent)) return;
  info.onAgentEvent(ev);
  if (e.type === 'extension_ui_request' && e.method === 'notify') {
    toast(`${id}: ${e.message}`);
    return;
  }
  if (e.type === 'extension_error') {
    toast(t('{agent}: extension error: {error}', { agent: id, error: e.error }), { duration: 6000 });
    return;
  }
  if (contextEvents.has(e.type)) contextViewer.refresh(agent);
  const c = agent && state.convos.get(keyOf(agent));
  if (!c) return;
  // While the history is read, every record is kept to replay over it:
  // before the first read, and when a loaded conversation is read again,
  // whose records also show at once.
  c.queue?.push(ev);
  if (!c.loaded) return;
  if (ev.seq <= c.seq || c.eventSeqs.has(ev.seq)) return;
  c.eventSeqs.add(ev.seq);
  if (e.type === 'queue_update' || e.type === 'delivery_queue') {
    if (ev.seq <= c.queueSeq) return;
    c.queueSeq = ev.seq;
    c.piQueue = e.type === 'delivery_queue' ? e.queue : { known: true, steering: e.steering?.length ?? 0, followUp: e.followUp?.length ?? 0, recallSupported: supportsRecall(e.pending?.capability) };
    if (keyOf(selectedAgent() ?? {}) === c.key) {
      const scroller = $('#scroll');
      if (scroller) scroller.dataset.stick = atBottom(scroller) ? 'true' : 'false';
      renderQueue(c);
      scrollDown();
    }
    return;
  }
  if (e.type === 'delivery_update' || e.type === 'delivery_retained') {
    applyDeliveryReceipt(c,e,ev.seq);
    schedulePatch(c);
    return;
  }
  applyEvent(c, e, false);
}

// applyEvent updates a conversation with one record, and the thread when it
// is shown, unless the record is being replayed before the thread renders.
function applyEvent(c, e, replaying,seq = 0) {
  const visible = !replaying && state.view.kind === 'agent' && state.view.id === c.id && $('#thread');
  switch (e.type) {
    case 'delivery_queue':
      c.piQueue = e.queue;
      if (visible) renderQueue(c);
      return;
    case 'queue_update':
      c.piQueue = { known: true, steering: e.steering?.length ?? 0, followUp: e.followUp?.length ?? 0, recallSupported: supportsRecall(e.pending?.capability) };
      if (visible) renderQueue(c);
      return;
    case 'delivery_retained':
    case 'delivery_update':
      applyDeliveryReceipt(c,e,seq);
      break;
    case 'message_start': {
      if (e.message?.role !== 'assistant') return;
      c.live = { message: { ...e.message, content: [] } };
      break;
    }
    case 'message_update': {
      if (!c.live) return;
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
      break;
    }
    case 'message_end': {
      const m = e.message;
      indexMessage(c, m);
      if (m.role === 'assistant' && c.live) {
        // The final message normally keeps the streaming one's timestamp, and
        // so its rows' keys. If not, what was open stays open.
        const before = `w:assistant:${c.live.message.timestamp ?? ''}:`;
        const after = `w:assistant:${m.timestamp ?? ''}:`;
        if (before !== after) {
          for (const key of [...c.workOpen]) if (key.startsWith(before)) { c.workOpen.delete(key); c.workOpen.add(after + key.slice(before.length)); }
          for (const key of [...c.failuresOpen]) if (key.startsWith(`f:${before}`)) { c.failuresOpen.delete(key); c.failuresOpen.add(`f:${after}${key.slice(before.length + 2)}`); }
        }
      }
      const submission = originOf(m)?.submission;
      const existing = submission ? c.messages.findIndex(old => originOf(old)?.submission === submission) : -1;
      if (existing >= 0) c.messages[existing] = m;
      else c.messages.push(m);
      if (m.role === 'assistant') c.live = null;
      break;
    }
    case 'tool_execution_start':
    case 'tool_execution_update':
    case 'tool_execution_end': {
      const tool = c.tools.get(e.toolCallId) ?? { args: e.args };
      if (tool.final) break; // its result message came first, and is final
      tool.args = e.args ?? tool.args;
      if (e.type === 'tool_execution_end') {
        tool.status = e.isError ? 'error' : 'done';
        tool.result = resultText(e.result);
      } else {
        tool.status = 'running';
        if (e.partialResult) tool.result = resultText(e.partialResult);
      }
      tool.revision = (tool.revision ?? 0) + 1; // partial output changes a running tool
      c.tools.set(e.toolCallId, tool);
      break;
    }
    case 'compaction_start':
      if (visible) {
        const scroller = $('#scroll');
        if (scroller) scroller.dataset.stick = atBottom(scroller) ? 'true' : 'false';
        const note = h('div', { class: 'note note--quiet' }, t('Summarizing earlier conversation to save context…'));
        const tail = $('#thread .pi-queue-status') ?? $('#thread .working-live');
        tail ? tail.before(note) : $('#thread').append(note);
        scrollDown();
      }
      return;
    case 'auto_retry_start':
      if (visible) toast(t('Retrying after: {error}', { error: e.errorMessage }));
      return;
    default:
      return;
  }
  if (visible) schedulePatch(c, { frame: e.type === 'message_update' || e.type === 'tool_execution_update' });
}

// ---------- Sidebar ----------

function statusDot(status) {
  // A failure is a red exclamation mark, named for those who cannot see it.
  if (status === 'error' || status === 'failed') {
    const label = status === 'error' ? t('Last run ended with an error') : t('Stopped unexpectedly');
    return h('span', { class: 'dot-wrap', 'data-status': status, role: 'img', 'aria-label': label, title: label }, icon('error', 'mds-icon status-error-mark'));
  }
  return h('span', { class: 'dot-wrap', 'data-status': status }, h('i', { class: 'mds-dot', 'aria-hidden': 'true' }));
}

// How long a run has gone on: 8s, 4m, 1h 5m.
function elapsed(since) {
  return span(Math.max(0, Math.floor((Date.now() - new Date(since).getTime()) / 1000)));
}

function span(s) {
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
  // A worker that finished on an error shows the error, not a check.
  const done = worker && a.finishedAt && !runFailed(a);
  let status = done ? t('Done') : statusLabel(a);
  if (worker && a.status === 'failed') status = t('{status}; {name} was told', { status, name: yesmanLabel(a.yesman) });
  // While a later run is going, the error it may recover from stays in the tooltip.
  const lastError = a.runError ? `\n${runFailed(a) ? '' : `${t('The last run ended with an error')}: `}${runErrorText(a.runError.message)}` : '';
  return h('button', {
    class: `agent-row ${cls}`, type: 'button', 'data-agent': a.id, 'data-status': statusTone(a), 'data-done': done ? 'true' : null,
    'data-sidebar-entry': worker ? 'worker' : cls.includes('agent-row--yesman') ? 'yesman' : 'needs-you',
    'aria-current': current ? 'page' : null, 'aria-haspopup': 'menu',
    title: `${a.id}: ${status}${a.title ? `\n${a.title}` : ''}${a.detail ? `\n${a.detail}` : ''}${lastError}`,
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
  for (const el of document.querySelectorAll('[data-elapsed]')) el.textContent = elapsed(el.dataset.elapsed);
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
      case 'task-log': await call('Reveal', `${workspace.path}/.yesman/task-log.md`); break;
      case 'logs': await call('Reveal', `${workspace.path}/.yesman/sessions/${a.name}`); break;
      case 'abort': await call('Abort', a.id); break;
      case 'sleep': await call('Stop', a.id); break;
      case 'rename': await renameYesman(workspace); break;
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
  const list = $('#yesmen');
  if (!row?.dataset.agent) return;
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
  // What waits on you shows by yesman, in the Needs you pane beside its
  // conversation, never gathered here.
  list.append(h('div', { class: 'side-group' }, h('span', null, t('Yesmen')),
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: shortcuts.hint(t('Hire a yesman'), 'yesman.hire'), 'aria-label': t('Hire a yesman'),
      'aria-keyshortcuts': shortcuts.aria('yesman.hire'), onclick: openHire }, icon('plus'))));
  for (const y of state.snap.yesmen) {
    const agents = state.snap.agents.filter((a) => a.yesman === y.name);
    const lead = agents.find((a) => a.role === 'lead');
    if (lead) list.append(agentRow(lead, 'agent-row--yesman', y.displayName || y.name));
    // Active workers come first; preserve start order within each class.
    // The yesman's menu can hide done workers, except the one being viewed.
    const hide = state.hideDone.has(y.name);
    for (const w of orderWorkers(agents.filter((a) => a.role === 'worker'))) {
      if (hide && w.finishedAt && !(state.view.kind === 'agent' && state.view.id === w.id)) continue;
      list.append(agentRow(w, 'agent-row--worker', w.name));
    }
  }
  for (const issue of state.snap.workspaceErrors ?? []) {
    list.append(h('div', { class: 'workspace-error', role: 'note', 'data-workspace-error': issue.name },
      h('p', null, h('strong', null, issue.name), ': ', t('Workspace unavailable')),
      h('p', null, t('Metadata is {size} bytes; the limit is {limit} bytes.', { size: issue.size, limit: issue.limit })),
      h('p', { class: 'path' }, issue.path),
      h('p', null, t('Quit yesman before editing. Back up this file, reduce its size, and preserve its hiredAt field.'))));
  }
  restoreSidebarFocus(focusedRow);
  renderSideFoot();
}

// ---------- Needs you ----------

// Needs you is a pane beside the conversation, in the place of the board and
// the files: what the yesman shown waits on the user for, for its current
// hire only. A request opens where it was asked; opening it closes nothing:
// answering it there, or marking it done or not needed, does.

const REQUEST_ICONS = {
  confirm: 'check', select: 'listChecks', input: 'pencil', editor: 'pencil',
  decision: 'listChecks', login: 'lock', install: 'package', manual: 'info',
};
const requestIcon = (item, cls = 'mds-icon') => icon(REQUEST_ICONS[item.type] ?? 'bell', cls);
const RESOLUTIONS = { done: 'Marked done', 'not-needed': 'Marked not needed', resolved: 'Resolved by the lead' };

// Where a request comes from: the yesman, and for a worker its number and
// subject, by stable IDs.
function requestFrom(item) {
  const label = yesmanLabel(item.yesman);
  if (item.role === 'lead') return label;
  const a = agentById(item.agent);
  return `${label} › ${a ? workerNumber(a) : item.agent.split('/')[1]}${a?.title ? ` ${a.title}` : ''}`;
}

const requestWhat = (item) => item.title || t(typeLabel(item));

// requestSource is the badge of who in the yesman asks: its lead, or a
// worker by its number.
function requestSource(item) {
  if (item.role === 'lead') return t('Lead');
  const a = agentById(item.agent);
  return a ? workerNumber(a) : item.agent.split('/')[1];
}

// What waits on the user from y, for its current hire (needsyou.js).
const needsYouOf = (y) => requestsOf(state.snap, y);

// The yesman the pane shows: the one whose conversation is shown. y is null
// when its workspace cannot be read; the pane says so, never that nothing
// waits.
function needsYouShown() {
  const a = state.pane === 'needs-you' ? selectedAgent() : null;
  return a ? { name: a.yesman, y: yesmanOf(a.id) ?? null } : null;
}

// needsYouScope names the yesman and the hire the pane shows, or null.
function needsYouScope() {
  const s = needsYouShown();
  return s ? JSON.stringify([s.name, s.y ? s.y.hiredAt ?? '' : null]) : null;
}

let needsYouHistoryOpen = false;

// showNeedsYou opens the pane for the yesman shown, and puts focus on the
// request named or on the pane's heading. Outside a conversation there is no
// yesman to show it for.
function showNeedsYou(focusKey = null) {
  if (!selectedAgent()) return false;
  if (state.pane !== 'needs-you') setPane('needs-you');
  const aside = $('#needs-you');
  ((focusKey && aside.querySelector(`[data-request="${CSS.escape(focusKey)}"]`)) || aside.querySelector('.needs-you-heading'))?.focus({ preventScroll: true });
  return true;
}

// closeNeedsYou closes the pane and gives focus back to what opens it: the
// info panel's row, or the message box.
function closeNeedsYou() {
  setPane(null);
  const row = $('#yesman-info [data-info-entry="needs-you"]');
  (row?.getClientRects().length ? row : $('#dock textarea'))?.focus({ preventScroll: true });
}

// Open a request where it was asked: its agent's conversation, with the
// request found and focused there.
function openRequest(item) {
  if (!agentById(item.agent)) {
    toast(t('This request is no longer open.'));
    refreshSoon();
    return;
  }
  state.request = { key: item.key, agent: item.agent, pending: true };
  selectAgent(item.agent);
}

function renderNeedsYouPane() {
  const aside = $('#needs-you');
  const shown = needsYouShown();
  aside.hidden = !shown;
  aside.setAttribute('aria-label', t('Needs You'));
  document.documentElement.dataset.needsYou = shown ? 'open' : 'closed';
  if (!shown) {
    aside.replaceChildren();
    delete aside.dataset.scope;
    return;
  }
  const { name, y } = shown;
  const scope = needsYouScope();
  const same = aside.dataset.scope === scope;
  // What had focus, to give back after the redraw: the request (or the one
  // now in its place), the request memory, the history, the close button or
  // the heading. A control gone with another yesman or hire leaves focus on
  // the heading; focus outside the pane stays where it is.
  const active = aside.contains(document.activeElement) ? document.activeElement : null;
  const rows = [...aside.querySelectorAll('[data-request]')];
  const at = rows.indexOf(active?.closest('[data-request]'));
  const key = active?.closest('[data-request]')?.dataset.request;
  const memory = active?.closest('[data-memory]') ? 'memory' : null;
  const control = memory ?? active?.closest('[data-pane-control]')?.dataset.paneControl;
  const scroll = same ? $('.board-body', aside)?.scrollTop ?? 0 : 0;

  const label = yesmanLabel(name);
  const items = y ? needsYouOf(y) : [];
  const body = h('div', { class: 'board-body needs-you-body' });
  if (!y) {
    body.append(h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null,
      h('strong', null, t('What {name} needs from you cannot be read', { name: label })),
      h('p', null, t('Its workspace is unavailable; the sidebar says why.')))));
  } else {
    if (y.handoffsError) {
      body.append(h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null,
        h('strong', null, t('What {name} handed to you cannot be read', { name: label })), h('p', null, y.handoffsError))));
    }
    const notice = callMemoryNotice(y);
    if (notice) body.append(notice);
    if (items.length) {
      body.append(h('ol', { class: 'board-list needs-you-list', 'aria-label': t('Waiting for you') }, items.map(needsYouPaneRow)));
    } else if (!y.handoffsError) {
      body.append(h('div', { class: 'board-empty needs-you-empty', role: 'status' },
        h('strong', null, t('Nothing from {name} needs you right now.', { name: label })),
        h('p', null, t('When a yesman needs your answer, a decision or a step only you can take, it shows here.'))));
    }
    const closed = closedHandoffs(state.snap, 20, y.name);
    if (closed.length) {
      const history = h('details', { class: 'board-done needs-you-history', open: needsYouHistoryOpen, 'data-pane-control': 'history' },
        h('summary', null, icon('chevron', 'mds-icon board-fold'), t('Recently handled ({n})', { n: formatNumber(closed.length) })),
        h('ul', null, closed.map(({ handoff }) => h('li', null,
          h('span', { class: 'needs-you-history-title' }, handoff.title),
          h('span', { class: 'needs-you-history-meta' },
            `${t(RESOLUTIONS[handoff.resolution] ?? 'Closed')} · ${t('{time} ago', { time: elapsed(handoff.closedAt) })}`)))));
      history.addEventListener('toggle', () => { needsYouHistoryOpen = history.open; });
      body.append(history);
    }
  }
  const count = items.length ? tn(items.length, '{n} waiting', '{n} waiting') : '';
  aside.replaceChildren(
    h('div', { class: 'files-head drag' },
      h('h2', { class: 'files-title needs-you-heading', tabindex: '-1' }, t('Needs You')),
      count ? h('span', { class: 'board-count', 'data-tone': 'waiting' }, count) : null,
      h('div', { class: 'files-head-actions' },
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', 'data-pane-control': 'close', title: t('Close Needs You'), 'aria-label': t('Close Needs You'), onclick: closeNeedsYou }, icon('x')))),
    body);
  aside.dataset.scope = scope;
  body.scrollTop = scroll;
  if (!active) return;
  let target = null;
  if (same && key) {
    const now = [...aside.querySelectorAll('[data-request]')];
    target = now.find((el) => el.dataset.request === key) ?? (now.length ? now[Math.min(at, now.length - 1)] : null);
  } else if (same && control === 'memory') target = aside.querySelector('[data-memory] button');
  else if (same && control === 'history') target = aside.querySelector('[data-pane-control="history"] > summary');
  else if (control === 'close') target = aside.querySelector('[data-pane-control="close"]');
  (target ?? aside.querySelector('.needs-you-heading'))?.focus({ preventScroll: true });
}

// A request in the pane, as a task on the board: what it asks, a line of its
// text, its kind and how long it has waited, and who asks.
function needsYouPaneRow(item) {
  const text = requestSummary(item.summary);
  const opened = state.request?.key === item.key && state.view.kind === 'agent' && state.view.id === item.agent;
  return h('li', { class: 'board-task needs-you-item', 'data-type': item.type, 'data-kind': item.kind },
    h('button', {
      class: 'board-toggle needs-you-open', type: 'button', 'data-request': item.key, 'aria-current': opened ? 'true' : null,
      title: `${requestFrom(item)}: ${requestWhat(item)} (${t(typeLabel(item))})`,
      onclick: () => openRequest(item),
    }, requestIcon(item, 'mds-icon board-mark'),
    h('span', { class: 'needs-you-item-text' },
      h('span', { class: 'board-title' }, requestWhat(item)),
      text ? h('span', { class: 'needs-you-item-summary' }, text) : null,
      h('span', { class: 'needs-you-item-meta' },
        h('span', null, t(typeLabel(item))),
        item.since ? h('span', null, t('Waiting'), ' ', h('span', { 'data-elapsed': item.since }, elapsed(item.since))) : null,
        item.timeout ? h('span', null, t('Goes on without you after {time}', { time: span(Math.round(item.timeout / 1000)) })) : null))),
    h('span', { class: 'board-owner needs-you-source', title: requestFrom(item) }, requestSource(item)));
}

// What a yesman's memory of its lead's tool calls means for the user, when
// it matters: nearly full, full, or kept by a pre-release version; with the
// way to start a new one, which only the user can.
const CALL_MEMORY = {
  near: { tone: 'warn', title: "{name}'s request memory is nearly full", body: 'It remembers {used} of {capacity} of the tool calls its lead used to hand you requests, so that none registers twice. Once it is full, its lead cannot hand you new requests until you start a new memory.' },
  full: { tone: 'error', title: "{name}'s lead cannot hand you new requests", body: 'Its request memory is full: it remembers {used} of {capacity} tool calls. Start a new memory to let its lead hand you requests again.' },
  blocked: { tone: 'error', title: "{name}'s lead cannot hand you new requests", body: 'Its requests were kept by a pre-release version of yesman, which forgot some of its lead\'s tool calls without a trace, so a new one cannot be told from a repeat. Start a new memory to let its lead hand you requests again.' },
  legacy: { tone: 'warn', title: "{name}'s requests were kept by a pre-release version", body: 'It remembers older tool calls of its lead in a way that may refuse some new requests. If its lead says a request was refused, start a new memory.' },
  'legacy-full': { tone: 'error', title: "{name}'s lead cannot hand you most new requests", body: 'Its requests were kept by a pre-release version of yesman, whose memory of older tool calls is so full that it refuses most new ones. Start a new memory to let its lead hand you requests again.' },
};

function callMemoryNotice(y) {
  const m = y.callMemory;
  const text = m && CALL_MEMORY[m.state];
  if (!text) return null;
  const name = yesmanLabel(y.name);
  const busy = state.resetting.has(y.name);
  return h('div', { class: 'mds-notice needs-you-memory', role: m.state === 'near' || m.state === 'legacy' ? 'status' : 'alert', 'data-tone': text.tone, 'data-memory': y.name, 'data-state': m.state },
    h('div', null,
      h('strong', null, t(text.title, { name })),
      h('p', null, t(text.body, { used: formatNumber(m.used), capacity: formatNumber(m.capacity) }))),
    // Busy, it keeps focus, so that focus stays in the pane while it resets.
    h('button', { class: 'mds-button mds-button--small', type: 'button', 'aria-disabled': busy ? 'true' : null, onclick: () => resetCallMemory(y, m) }, t('Start a new memory…')));
}

async function resetCallMemory(y, m) {
  if (state.resetting.has(y.name)) return;
  const name = yesmanLabel(y.name);
  const forgets = m.forgettable > 0
    ? t('This forgets {n} records of older tool calls {name}\'s lead used to hand you requests.', { n: formatNumber(m.forgettable), name })
    : t('This forgets what a pre-release version remembered of the tool calls {name}\'s lead used to hand you requests.', { name });
  const body = [forgets,
    t('Requests waiting for you, recently handled ones and the latest tool calls stay.'),
    t('A request handled long ago may show up once more if its lead repeats an old tool call; it is remembered again then. A later reset can let it back once more.')].join(' ');
  if (!(await confirmAction(t('Start a new request memory for {name}?', { name }), body, t('Start a new memory')))) return;
  // Where the reset was asked: focus goes back there only if the pane still
  // shows that yesman and hire, and the user has not put it on something
  // else meanwhile. A closed confirmation can keep focus (WebKit): it goes
  // back on the button now, before the conversation is drawn again.
  const asked = JSON.stringify([y.name, y.hiredAt ?? '']);
  if (needsYouScope() === asked && focusIsFree(y.name)) $(`#needs-you [data-memory="${CSS.escape(y.name)}"] button`)?.focus({ preventScroll: true });
  state.resetting.add(y.name);
  render();
  try {
    await call('ResetCallMemory', y.name, y.hiredAt, m.epoch, m.used, m.forgettable);
    toast(t('{name}\'s lead can hand you new requests again.', { name }));
  } catch (err) {
    fail(err);
  } finally {
    state.resetting.delete(y.name);
    await refreshState();
    if (needsYouScope() === asked && focusIsFree(y.name)) {
      ($(`#needs-you [data-memory="${CSS.escape(y.name)}"] button`) || $('#needs-you .needs-you-heading'))?.focus();
    }
  }
}

// focusIsFree reports whether focus is on nothing the user chose: nowhere,
// left in a closed dialog (WebKit keeps it on the confirmation a click
// closed), the pane's heading, or this yesman's request memory.
function focusIsFree(name) {
  const a = document.activeElement;
  const dialog = a?.closest('dialog');
  return !a || a === document.body || (dialog && !dialog.open) || a.matches('#needs-you .needs-you-heading') || !!a.closest(`#needs-you [data-memory="${CSS.escape(name)}"]`);
}

// The handoffs of a lead's yesman, in its conversation, the one being opened
// first among them.
function handoffEls(a) {
  const y = yesmanOf(a.id);
  if (!y || a.role !== 'lead') return [];
  const items = needsYouItems({ agents: [], yesmen: [y] });
  if (!items.length) return [];
  const focus = state.request?.agent === a.id ? items.find((item) => item.key === state.request.key) : null;
  let shown = items.slice(0, 3);
  if (focus && !shown.includes(focus)) shown = [focus, ...shown.slice(0, 2)];
  const els = shown.map((item) => handoffEl(a, y, item));
  if (items.length > shown.length) {
    els.push(h('button', { class: 'mds-button mds-button--subtle mds-button--small needs-you-more', type: 'button', onclick: () => showNeedsYou(items.find((item) => !shown.includes(item))?.key) },
      t('{n} more under Needs You', { n: formatNumber(items.length - shown.length) })));
  }
  return els;
}

function handoffEl(a, y, item) {
  const ho = item.handoff;
  const busy = state.resolving.has(item.key);
  const resolve = async (resolution) => {
    if (state.resolving.has(item.key)) return;
    state.resolving.add(item.key);
    renderDock();
    let closed = false;
    try {
      await call('ResolveHandoff', y.name, y.hiredAt, ho.id, resolution);
      closed = true;
      await refreshState();
    } catch (err) {
      fail(err);
    } finally {
      state.resolving.delete(item.key);
      if (selectedAgent()?.id === a.id) {
        renderDock();
        if (closed) $('#dock textarea')?.focus();
      }
    }
  };
  const confirmedByUser = ['login', 'install', 'manual'].includes(ho.type);
  const facts = [ho.task && t('Task {id}', { id: ho.task }), ho.worker && t('Worker {name}', { name: ho.worker })].filter(Boolean).join(' · ');
  return h('div', { class: 'ask handoff', role: 'group', 'aria-label': `${t(typeLabel(item))}: ${ho.title}`, 'data-request': item.key, 'data-type': ho.type },
    h('span', { class: 'needs-you-type' }, requestIcon(item), t(typeLabel(item))),
    h('strong', null, ho.title),
    ho.detail ? h('div', { class: 'md handoff-detail', html: renderMarkdown(ho.detail) }) : null,
    facts ? h('p', { class: 'ask-hint' }, facts) : null,
    h('p', { class: 'ask-hint' }, confirmedByUser
      ? t('Mark it done once you have done it yourself; {name} checks it before relying on it.', { name: yesmanLabel(y.name) })
      : t('Answer in the message box below if it needs words, then mark it done.')),
    h('div', { class: 'ask-actions' },
      h('button', { class: 'mds-button mds-button--primary mds-button--small', type: 'button', disabled: busy, onclick: () => resolve('done') }, t('Mark done')),
      h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: busy, onclick: () => resolve('not-needed') }, t('Not needed')),
      ho.toolCall ? h('button', { class: 'mds-button mds-button--subtle mds-button--small', type: 'button', onclick: () => showToolCall(a, ho.toolCall) }, t('Show where it was asked')) : null));
}

// locateRequest marks the request being opened in the dock and, once, moves
// focus and the view to it. A request answered meanwhile says so instead.
function locateRequest(dock, a) {
  const r = state.request;
  if (!r || r.agent !== a.id) return;
  const target = dock.querySelector(`[data-request="${CSS.escape(r.key)}"]`);
  if (!target) {
    if (r.pending) {
      r.pending = false;
      dock.prepend(h('div', { class: 'mds-notice needs-you-gone', role: 'status' }, h('div', null,
        h('p', null, t('This request is no longer open: it was answered, cancelled or timed out.')),
        backToNeedsYou(r.key))));
    }
    return;
  }
  target.classList.add('needs-you-target');
  target.append(backToNeedsYou(r.key));
  if (!r.pending) return;
  r.pending = false;
  target.scrollIntoView({ block: 'nearest' });
  target.querySelector('input, textarea, button')?.focus({ preventScroll: true });
}

// Back to the request in the Needs you pane, opening it if it was closed.
const backToNeedsYou = (key) => h('button', { class: 'mds-button mds-button--subtle mds-button--small needs-you-back', type: 'button', onclick: () => showNeedsYou(key) },
  icon('arrowLeft'), t('Back to Needs You'));

// showToolCall finds the lead's tool call that registered a handoff in the
// folded work log of its conversation, when the loaded history has it.
async function showToolCall(a, id) {
  const c = convo(a);
  // The call's work row may be one of the failed runs folded together.
  const has = (r) => r?.type === 'work' && r.items.some((item) => item.type === 'tool' && item.block?.id === id);
  const find = () => {
    for (const r of project(c, a)) {
      if (has(r)) return { row: r, work: r };
      const run = r.type === 'failures' ? r.runs.find((x) => has(x.work)) : null;
      if (run) return { row: r, work: run.work };
    }
    return null;
  };
  if (c.loading) await c.loading.catch(() => {});
  // A window can hold an older copy of the history: read it again once.
  if (!find() && !c.loading) {
    c.loading = loadConvo(c).finally(() => { c.loading = null; });
    await c.loading;
  }
  if (selectedAgent()?.id !== a.id) return;
  const found = find();
  const open = async (el) => {
    if (el.open) return;
    const opened = new Promise((resolve) => el.addEventListener('toggle', resolve, { once: true }));
    el.open = true;
    await opened;
  };
  let rowEl = found && $(`#thread [data-row="${CSS.escape(found.row.key)}"]`);
  if (rowEl && found.work !== found.row) {
    await open(rowEl);
    rowEl = rowEl.querySelector(`[data-run="${CSS.escape(found.work.key)}"] > .work`);
  }
  if (!rowEl) {
    toast(t('Where it was asked is not in the loaded conversation.'));
    return;
  }
  await open(rowEl);
  const el = rowEl.querySelector(`[data-tool="${CSS.escape(id)}"]`);
  if (!el) {
    toast(t('Where it was asked is not in the loaded conversation.'));
    return;
  }
  for (const old of document.querySelectorAll('#thread .needs-you-target')) old.classList.remove('needs-you-target');
  el.classList.add('needs-you-target');
  el.scrollIntoView({ block: 'center' });
  el.querySelector('summary')?.focus({ preventScroll: true });
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
    const here = h('span', { 'aria-current': 'page', title: y?.path ?? '' }, a.role === 'lead' ? yesmanLabel(a.yesman) : a.name);
    crumbs.replaceChildren(...(a.role === 'lead' ? [here] : [h('span', null, yesmanLabel(a.yesman)), sep(), here]));
  } else {
    crumbs.replaceChildren();
  }
  renderBarActions();
}

function renderBarActions() {
  const actions = $('#bar-actions');
  const a = state.view.kind === 'agent' ? selectedAgent() : null;
  const button = (iconName, label, onclick, extra = {}, action = null) =>
    h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: action ? shortcuts.hint(label, action) : label, 'aria-label': label,
      'aria-keyshortcuts': action ? shortcuts.aria(action) : null, onclick, ...extra }, icon(iconName));
  actions.replaceChildren(...[
    a ? h('span', { class: 'mds-status', 'data-status': statusTone(a), role: 'status' }, h('i', { class: 'mds-dot', 'aria-hidden': 'true' }), h('span', null, statusLabel(a))) : null,
    updatePill(),
    a && a.status !== 'stopped'
      ? button('power', t('Put this agent to sleep: end its pi process. It resumes its session when you next talk to it.'), () => call('Stop', a.id).catch(fail))
      : null,
    a?.role === 'worker' ? button('x', t('Remove worker'), () => removeWorker(a)) : null,
    a ? button('listDashes', state.panelOpen ? t('Hide the info') : t('Show the info'), togglePanel, { 'aria-pressed': String(state.panelOpen) }, 'pane.info') : null,
    a ? button('listChecks', state.pane === 'board' ? t('Hide the board') : t('Show the board'), () => togglePane('board'), { 'aria-pressed': String(state.pane === 'board') }, 'pane.board') : null,
    a ? button('panel', state.pane === 'files' ? t('Hide the files') : t('Show the files'), () => togglePane('files'), { 'aria-pressed': String(state.pane === 'files') }, 'pane.files') : null,
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
  const dialog = $('#hire-dialog');
  if (dialog.open) return;
  const form = $('#hire-form');
  form.reset();
  const cleanup = bindHireForm(form, $('#hire-error'), { dialog, cancel: $('#hire-cancel') });
  // Its lead runs as the model Settings › yesman chooses for new yesmen.
  const hint = $('#hire-model-hint');
  crewPage.describe(hint);
  crewPage.load().then(() => { if (dialog.open) crewPage.describe(hint); });
  await openDialog(dialog);
  cleanup();
}

// Both production creation forms submit a display name. Keep failures inline
// with the input intact, and prevent cancellation/double submission during IO.
function bindHireForm(form, error, { dialog, cancel } = {}) {
  const input = form.elements.name;
  const save = form.querySelector('[type="submit"]');
  const hint = form.querySelector('#hire-model-hint');
  let busy = false;
  error.hidden = true;
  input.oninput = () => { error.hidden = true; };
  if (cancel) cancel.onclick = () => dialog.close('cancel');
  if (dialog) dialog.oncancel = (e) => { if (busy) e.preventDefault(); };
  form.onsubmit = async (e) => {
    e.preventDefault();
    if (busy) return;
    const label = input.value.trim();
    if (!validDisplayName(label)) {
      error.textContent = t('Enter a name without control characters.');
      error.hidden = false;
      input.focus();
      return;
    }
    busy = true;
    save.disabled = input.disabled = true;
    if (cancel) cancel.disabled = true;
    if (hint) hint.inert = true;
    let hired;
    try {
      hired = await call('HireNamed', label);
    } catch (err) {
      error.textContent = errorText(err);
      error.hidden = false;
    } finally {
      busy = false;
      save.disabled = input.disabled = false;
      if (cancel) cancel.disabled = false;
      if (hint) hint.inert = false;
    }
    if (!hired) {
      input.focus();
      return;
    }
    if (dialog) dialog.close('hire');
    else form.reset();
    // A refresh failure happens after creation has committed. Close/reset the
    // form first so retrying a State read cannot hire a second workspace.
    try {
      await refreshState();
      selectAgent(`${hired.name}/lead`);
    } catch (err) {
      fail(err);
    }
  };
  return () => {
    form.onsubmit = input.oninput = null;
    if (dialog) dialog.oncancel = null;
    if (cancel) cancel.onclick = null;
  };
}

function renderBody() {
  if (state.view.kind !== 'settings' || state.view.page !== 'shortcuts') shortcutsPage.leave();
  if (state.view.kind !== 'settings' || state.view.page !== 'grant') grantsPage.leave();
  const body = $('#body');
  // A refresh while typing, say in the search box, keeps the caret there.
  const active = body.contains(document.activeElement) ? document.activeElement : null;
  const held = requestFocus($('#dock'));
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
  renderDock({ held });
  renderThread();
}

// requestFocus is where focus is in a request of the dock, to keep across a
// redraw: the request, which of its controls, what was typed in it, and its
// place among the requests.
function requestFocus(dock) {
  const el = dock?.contains(document.activeElement) ? document.activeElement : null;
  const request = el?.closest('[data-request]');
  if (!request) return null;
  return {
    key: request.dataset.request,
    index: [...request.querySelectorAll('button, input, textarea')].indexOf(el),
    value: request.querySelector('input, textarea')?.value,
    at: [...dock.querySelectorAll('[data-request]')].indexOf(request),
  };
}

function renderDock({ focusComposer = false, held = null } = {}) {
  const dock = $('#dock');
  const a = selectedAgent();
  const keep = dock?.querySelector('textarea');
  const focused = keep && document.activeElement === keep;
  // Focus on the Needs you pane beside stays there when the conversation is
  // drawn again; opening a request from it moves focus to the request.
  const beside = !!document.activeElement?.closest?.('#needs-you');
  const actionFocused = dock?.querySelector('.composer-action') === document.activeElement;
  const caret = focused ? [keep.selectionStart, keep.selectionEnd, keep.selectionDirection] : null;
  const control = dock?.contains(document.activeElement) && document.activeElement.getAttribute('role') === 'combobox'
    ? document.activeElement.getAttribute('aria-label') : null;
  // A request being handled keeps its focus and typed answer across redraws,
  // also when the whole body was drawn again (held).
  const heldAt = held ?? requestFocus(dock);
  for (const select of composerSelects) select.destroy();
  composerSelects = [];
  if (!dock || !a) return;
  dock.replaceChildren();

  // What waits above the composer takes at most part of the window, and
  // scrolls, so that the conversation and the composer stay in view.
  const requests = [];
  if (a.status === 'failed' && a.detail) {
    requests.push(h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null, h('strong', null, t('This agent stopped')), h('p', null, a.detail))));
  } else if (a.detail) {
    requests.push(h('div', { class: 'mds-notice', role: 'status' }, h('div', null, h('strong', null, t('Needs a look')), h('p', null, a.detail))));
  }
  if (a.status === 'waiting' && a.dialog) requests.push(askEl(a));
  requests.push(...handoffEls(a));
  if (requests.length) dock.append(h('div', { class: 'dock-requests' }, ...requests));

  const working = a.status === 'working' || a.status === 'waiting';
  const key = keyOf(a);
  let attachments = state.attachments.get(key);
  if (!attachments) state.attachments.set(key, (attachments = { items: [], reading: false, sending: false }));
  const busy = attachments.reading || attachments.sending || attachments.recalling;
  const previews = h('div', { class: 'composer-images', hidden: !attachments.items.length }, attachments.items.map((image) =>
    h('div', { class: 'composer-image' },
      h('img', { src: imageURL(image), alt: image.name, title: image.name }),
      h('button', { class: 'round', type: 'button', disabled: busy, title: t('Remove image'), 'aria-label': t('Remove image'), onclick: () => {
        attachments.items = attachments.items.filter((item) => item !== image);
        renderDock();
      } }, icon('trash')))));
  const refresh = () => { if (keyOf(selectedAgent() ?? {}) === key) renderDock(); };
  const addImages = async (files) => {
    if (attachments.reading || attachments.sending || attachments.recalling || !files.length) return;
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
    rows: 1, 'aria-label': t('Message'), readonly: attachments.sending || attachments.recalling, 'data-paste-images': true,
    title: working ? t('Enter to send after the current step; Shift+Enter for a new line') : '',
    placeholder: a.role === 'lead' ? t('Message {name}…', { name: yesmanLabel(a.yesman) }) : t('Message worker {name}…', { name: a.name }),
  });
  textarea.value = state.drafts.get(keyOf(a)) ?? '';
  const action = working
    ? h('button', { class: 'round round--stop composer-action', type: 'button', 'aria-label': t('Stop'), title: shortcuts.hint(t('Stop the current run'), 'agent.stop'),
      'aria-keyshortcuts': shortcuts.aria('agent.stop'), onclick: () => call('Abort', a.id).catch(fail) }, icon('stop'))
    : h('button', { class: 'round composer-action', type: 'submit', 'aria-label': t('Send'), title: t('Send') }, icon('enter'));
  const sync = () => {
    // A run can be interrupted even with an empty draft or a pending image/send.
    if (!working) action.disabled = (!textarea.value.trim() && !attachments.items.length) || busy || !!state.modelOptions.get(keyOf(a))?.busy;
    textarea.style.height = 'auto';
    textarea.style.height = `${textarea.scrollHeight}px`;
  };
  textarea.addEventListener('input', () => { state.drafts.set(keyOf(a), textarea.value); sync(); });
  let composing = false;
  textarea.addEventListener('compositionstart', () => { composing = true; });
  textarea.addEventListener('compositionend', () => { composing = false; });
  textarea.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229 && !composing) {
      e.preventDefault();
      form.requestSubmit();
    }
  });
  textarea.addEventListener('paste', (e) => {
    const files = [...(e.clipboardData?.items ?? [])].filter((item) => item.kind === 'file' && item.type.startsWith('image/')).map((item) => item.getAsFile()).filter(Boolean);
    if (files.length) { e.preventDefault(); addImages(files); }
  });
  const form = h('form', { class: 'composer', onsubmit: (e) => { e.preventDefault(); sendMessage(a, textarea, e.submitter); } },
    fileInput, previews,
    h('div', { class: 'composer-input' }, attach, textarea, action),
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
    el.classList.add('mds-select--font-caret');
    const select = initSelect(el);
    select.trigger.title = el.title;
    select.trigger.parentElement.append(icon('caret-down', 'mds-icon mds-select-caret'));
    return select;
  }));
  sync();
  const nextControl = composerSelects.find((select) => select.trigger.getAttribute('aria-label') === control);
  if (nextControl && !nextControl.trigger.disabled) nextControl.trigger.focus({ preventScroll: true });
  else if (actionFocused && !focusComposer && !action.disabled) action.focus({ preventScroll: true });
  else if (focusComposer || focused || actionFocused || (!keep && !beside)) {
    textarea.focus();
    if (caret) textarea.setSelectionRange(...caret);
  }
  // A long detail scrolls in its request; then it can be reached and
  // scrolled with the keyboard too.
  for (const detail of dock.querySelectorAll('.handoff-detail')) {
    if (detail.scrollHeight <= detail.clientHeight + 1) continue;
    Object.assign(detail, { tabIndex: 0 });
    detail.setAttribute('role', 'region');
    detail.setAttribute('aria-label', t('Details'));
  }
  if (heldAt) {
    const again = dock.querySelector(`[data-request="${CSS.escape(heldAt.key)}"]`);
    const field = again?.querySelector('input, textarea');
    if (field && heldAt.value !== undefined) field.value = heldAt.value;
    const control = again?.querySelectorAll('button, input, textarea')[heldAt.index];
    if (control && !control.disabled) control.focus({ preventScroll: true });
    else if (!again) {
      // It was answered or closed elsewhere: the request now in its place,
      // or the composer.
      const rest = dock.querySelectorAll('[data-request]');
      const next = rest[Math.min(heldAt.at, rest.length - 1)]?.querySelector('button:not(:disabled), input, textarea');
      (next ?? textarea).focus({ preventScroll: true });
    }
  }
  locateRequest(dock, a);
}

// holdSent keeps what this window submitted, exactly as it was sent, to show
// while its row is open until pi's own message is tied to it. A new
// submission lets go of earlier ones nothing more will be known about: a row
// still open for them shows what the receipt keeps. It is never stored or
// sent anywhere, and at most a few are kept.
const SENT_KEPT = 8;
function holdSent(c, id, submission) {
  if (!c.sent.has(id)) {
    for (const [held] of c.sent) if (canReleaseSent(c.deliveries.get(held))) c.sent.delete(held);
  }
  c.sent.delete(id);
  c.sent.set(id, { text: submission.text, images: submission.images });
  for (const held of [...c.sent.keys()].slice(0, Math.max(0, c.sent.size - SENT_KEPT))) c.sent.delete(held);
}

// Release superseded terminal display payloads (PR67); membership and restore
// authority override terminal-looking metadata and never come from this cache.
const canReleaseSent = (d) => d && FINISHED.has(d.state) && !d.nativePending && !d.canRestore;
function letGo(c) {
  for (const id of [...c.sent.keys()]) if (id !== c.lastSent && canReleaseSent(c.deliveries.get(id))) c.sent.delete(id);
}

const sameSubmissionImages = (left, right) => left.length === right.length && left.every((image, i) =>
  image.type === right[i].type && image.mimeType === right[i].mimeType && image.data === right[i].data);

async function sendMessage(a, textarea, submitter = null) {
  const key = keyOf(a);
  const attachments = state.attachments.get(key);
  const text = textarea.value;
  if ((!text.trim() && !attachments?.items.length) || attachments?.reading || attachments?.sending || attachments?.recalling || state.modelOptions.get(key)?.busy) return;
  const images = imagePayload(attachments?.items ?? []);
  const c = convo(a);
  // Editing after an uncertain submission is a new user intent. Retrying the
  // unchanged draft reuses its receipt, even when the bridge response was lost.
  if (attachments.submissionID) {
    const previous = attachments.submission;
    if (previous && (previous.text !== text || !sameSubmissionImages(previous.images, images))) {
      attachments.submissionID = null;
      attachments.submission = null;
    }
  }
  // The draft a refusal left, sent again unchanged, is that submission's
  // retry: once the server has the new one, the refused one has been dealt
  // with. An edited draft is a new intent and leaves it in view.
  const retry = attachments.retryOf;
  if (retry && (retry.text !== text || !sameSubmissionImages(retry.images, images))) attachments.retryOf = null;
  const retried = attachments.retryOf?.id ?? null;
  const messageID = attachments.submissionID ?? Date.now().toString(16).padStart(12, '0') + [...crypto.getRandomValues(new Uint8Array(18))].map((b) => b.toString(16).padStart(2, '0')).join('');
  {
    // Sending a hidden submission's draft again is a new attempt the person
    // asked for: what comes of it shows, even if it fails again.
    reopenHandled(c, messageID);
    attachments.submissionID = messageID;
    attachments.submission ??= { text, images };
    if (!c.deliveries.has(messageID)) {
      const preview = Array.from(text).slice(0,128).join('');
      c.deliveries.set(messageID, { id: messageID, revision: 0, state: 'sending', text:preview, imageCount:images.length, preview:preview !== text });
      const excess = [...c.deliveries.keys()].slice(0,Math.max(0,c.deliveries.size-256));
      pruneDeliveryRows(c,excess);
    }
    holdSent(c, messageID, attachments.submission);
    c.lastSent = messageID;
    schedulePatch(c);
  }
  // SubmitEvent retains the activated Send button even after WebKit's mouse
  // default action blurs the textarea without focusing the button. Use that
  // submission intent only for the initial render; subsequent renders follow
  // actual focus so moving to another control or agent cancels restoration.
  const focusComposer = submitter?.matches('button[type="submit"]') || document.activeElement === textarea ||
    document.activeElement === textarea.form?.querySelector('button[type="submit"]');
  attachments.sending = true;
  renderDock({ focusComposer });
  try {
    const receipt = await call('Submit', a.id, messageID, text, images, a.since);
    updateDelivery(c, receipt);
    if (retried && receipt?.id === messageID && receipt.revision > 0) {
      if (attachments.retryOf?.id === retried) attachments.retryOf = null;
      c.handled.add(retried);
      saveHandled(c);
    }
    schedulePatch(c);
    // A duplicate may find registration durable while pi's response is still
    // pending. Keep the only full draft/attachments until acceptance is known.
    if (receipt.state === 'sending') return;
    if (receipt.state === 'failed' || receipt.state === 'interrupted' || receipt.state === 'unconfirmed' && receipt.error) {
      // Only a definite rejection permits a fresh ID on unchanged retry. An
      // interrupted process may already have admitted this input before exit.
      if (receipt.state === 'failed') {
        attachments.retryOf = { id: messageID, text, images };
        attachments.submissionID = null;
        attachments.submission = null;
      }
      throw new Error(receipt.error || t(DELIVERY_LABELS[receipt.state]));
    }
    attachments.submissionID = null;
    attachments.submission = null;
    state.drafts.delete(key);
    attachments.items = [];
  } catch (err) {
    // Keep both text and images for retry if pi rejects the prompt. An
    // uncertain bridge failure keeps the same ID, so retry cannot duplicate it.
    const d = c.deliveries.get(messageID);
    if (d?.revision === 0) { d.state = 'unconfirmed'; schedulePatch(c); }
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
  const box = h('div', { class: 'ask', role: 'alert', 'data-request': `d:${a.id}@${a.since ?? ''}:${d.id ?? ''}` },
    h('strong', null, d.title || t('Question')), d.message ? h('p', null, d.message) : null,
    a.role === 'worker' ? h('p', { class: 'ask-hint' }, a.handedOver
      ? t('{name} handed this question to you.', { name: yesmanLabel(a.yesman) })
      : t('{name} has this question and answers it. You can answer here instead.', { name: yesmanLabel(a.yesman) })) : null);
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
  const input = h('input', { class: 'mds-input', name: 'name', placeholder: t('name, e.g. GizMoth'), required: true, spellcheck: 'false', 'aria-label': t('Name'), 'aria-describedby': 'welcome-name-hint welcome-hire-error' });
  const error = h('p', { class: 'welcome-hint', id: 'welcome-hire-error', role: 'alert', hidden: true });
  const form = h('form', null, input, h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, t('Hire')));
  bindHireForm(form, error);
  const hint = h('p', { class: 'welcome-hint' });
  crewPage.describe(hint);
  crewPage.load().then(() => { if (hint.isConnected) crewPage.describe(hint); });
  body.append(
    h('div', { class: 'page' },
      h('div', { class: 'welcome' },
        h('h1', { class: 'mds-title' }, t('Hire a yesman')),
        h('p', null, t('A yesman takes on a project. It gets a workspace of its own, checks out the repositories it needs from the shared pool, remembers what it learns, and starts workers when the job splits up.')),
        state.snap.piError ? h('div', { class: 'mds-notice', role: 'alert', 'data-tone': 'error' }, h('div', null, h('strong', null, t('pi was not found')), h('p', null, state.snap.piError))) : null,
        form,
        h('p', { class: 'welcome-hint', id: 'welcome-name-hint' }, t('A display name. Uppercase letters, spaces and Chinese characters are welcome.')),
        error,
        hint,
      )),
  );
  input.focus();
}

// ---------- Settings ----------

// The pages, labelled in English: each is said in the page's language.
const SETTINGS = [
  { page: 'general', label: 'General', icon: 'sliders' },
  { page: 'shortcuts', label: 'Shortcuts', icon: 'keyboard' },
  { page: 'grant', label: 'Grant', icon: 'lock' },
  { page: 'yesman', label: 'yesman', icon: 'users' },
  { page: 'usage', label: 'Model usage', icon: 'chart' },
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
  $('#settings-button').title = shortcuts.hint(t('Settings'), 'settings.open');
  const keys = shortcuts.aria('settings.open');
  if (keys) $('#settings-button').setAttribute('aria-keyshortcuts', keys);
  else $('#settings-button').removeAttribute('aria-keyshortcuts');
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
    else if (page === 'shortcuts') { await shortcutsPage.load(); return; }
    else if (page === 'grant') { await grantsPage.load(); return; }
    else if (page === 'pi') { await piPage.load(); return; }
    else if (page === 'yesman') { await crewPage.load(); return; }
    else if (page === 'usage') { usagePage.load(); return; }
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
  else if (state.view.page === 'shortcuts') shortcutsPage.render(inner);
  else if (state.view.page === 'grant') grantsPage.render(inner);
  else if (state.view.page === 'pi') piPage.render(inner);
  else if (state.view.page === 'yesman') crewPage.renderPage(inner);
  else if (state.view.page === 'usage') usagePage.render(inner);
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

// Settings › Grant keeps its requests and DOM lifecycle in grants.js.
const grantsPage = createGrants({ call, shown: () => state.view.kind === 'settings' && state.view.page === 'grant' });

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

// Settings › yesman: the crew, and what new yesmen run as, in crew.js.
const crewPage = createCrew({
  call,
  fail,
  shown: () => state.view.kind === 'settings' && state.view.page === 'yesman',
  rerender: () => renderBody(),
  openPage: () => showSettings('yesman'),
  openUsage: () => showSettings('usage'),
  preferenceRow,
});
// Settings › Model usage: what agents actually used, in usage.js.
const usagePage = createUsagePage({
  call,
  shown: () => state.view.kind === 'settings' && state.view.page === 'usage',
  openScores: () => showSettings('yesman'),
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
      row(!v ? 'pi' : v.piManaged ? t('Managed pi') : t('External pi'), v ? (v.pi || (v.piState && v.piState !== 'failed' ? t({ preparing: 'Preparing…', unprepared: 'Not prepared', closed: 'Stopped.' }[v.piState] || 'Version unknown') : h('span', { class: 'danger' }, v.piError || t('Version unknown')))) : checking()),
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
        h('p', null, tNodes('Exported {repos} and the settings to {path}', { repos: repositories(e.repos), path: h('span', { class: 'path', title: e.path }, e.path) }),
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
    shortcuts.load().catch(fail); // the file may carry shortcuts
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
  const keys = plan.shortcuts ?? { merge: [], replace: [] };
  const shortcutLine = (n) => tn(n, 'Keyboard shortcuts: 1 action changes', 'Keyboard shortcuts: {n} actions change');
  if (keys.merge.length) lines.push({ text: shortcutLine(keys.merge.length) });
  // Replacing also gives the actions the file leaves out their defaults.
  if (keys.replace.length !== keys.merge.length || keys.replace.some((id) => !keys.merge.includes(id))) {
    lines.push({ text: shortcutLine(keys.replace.length), replaceOnly: true });
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
  // Labels may be shared; deduplicate users by workspace identity first.
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
      ? h('span', { class: 'card-row-aside', title: worktrees.map((w) => `${yesmanLabel(w.yesman)}: ${w.dir}`).join('\n') },
          `${tn(worktrees.length, '1 worktree', '{n} worktrees')} · ${users.map(yesmanLabel).join(', ')}`)
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
  keys: () => ({ 'files.find': shortcuts.primary('files.find'), 'files.tree': shortcuts.primary('files.tree') }),
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
  openTaskLog: () => files.openTaskLog(),
  // The same requests as the Needs you pane shows for this yesman.
  needsYou: (y) => {
    const v = state.snap.yesmen.find((v) => v.name === y.name);
    return {
      count: v ? needsYouOf(v).length : 0,
      error: v?.handoffsError ?? '',
      // New requests are refused until the user starts a new memory.
      refused: ['full', 'blocked', 'legacy-full'].includes(v?.callMemory?.state),
      open: state.pane === 'needs-you',
    };
  },
  openNeedsYou: () => showNeedsYou(),
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

// Where focus was when opening a pane hid it (an info panel row, in a
// narrow window), to go back to when the pane closes.
let paneFocusFrom = null;

function setPane(pane) {
  const had = document.activeElement;
  const shown = (el) => !!el?.isConnected && el.getClientRects().length > 0;
  const visible = had && had !== document.body && shown(had);
  const inPane = !!had?.closest?.('#board, #needs-you, #files');
  const entry = had?.closest?.('#yesman-info [data-info-entry]')?.dataset.infoEntry;
  state.pane = pane;
  renderInfo();
  renderBarActions();
  // Only focus this change hid moves: to the pane opened, or, once it
  // closes, back to where it was or to the message box. Focus the user can
  // still see stays where it is.
  const now = document.activeElement;
  if (!visible || (now && now !== document.body && shown(now))) {
    if (!pane) paneFocusFrom = null;
    return;
  }
  let target = null;
  if (pane) {
    target = pane === 'needs-you' ? $('#needs-you .needs-you-heading') : pane === 'board' ? $('#board .files-head-actions button') : null;
    if (target && !inPane) paneFocusFrom = entry ? { entry } : had;
  } else {
    const from = paneFocusFrom;
    paneFocusFrom = null;
    target = (from?.entry ? $(`#yesman-info [data-info-entry="${CSS.escape(from.entry)}"]`) : from) ?? null;
    if (!shown(target)) target = $('#dock textarea');
  }
  if (shown(target)) target.focus({ preventScroll: true });
}

function togglePane(pane) {
  setPane(state.pane === pane ? null : pane);
}

function renderInfo() {
  files.render();
  board.render();
  renderNeedsYouPane();
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
  if (state.request?.agent !== id) state.request = null;
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

// The same display-name rules apply to creation and rename; the backend also
// checks UTF-8. Only internal workspace IDs have a 32-character slug limit.
function validDisplayName(label) {
  return !!label && !/[\u0000-\u001f\u007f-\u009f]/u.test(label);
}

async function renameYesman(y) {
  if (!y) return;
  const dialog = $('#rename-dialog');
  const form = $('#rename-form');
  const input = form.elements.name;
  const error = $('#rename-error');
  const save = form.querySelector('[type="submit"]');
  const cancel = $('#rename-cancel');
  let busy = false;
  input.value = y.displayName || y.name;
  error.hidden = true;
  input.oninput = () => { error.hidden = true; input.setCustomValidity(''); };
  cancel.onclick = () => dialog.close('cancel');
  dialog.oncancel = (e) => { if (busy) e.preventDefault(); };
  form.onsubmit = async (e) => {
    e.preventDefault();
    if (busy) return;
    const label = input.value.trim();
    if (!validDisplayName(label)) {
      error.textContent = t('Enter a name without control characters.');
      error.hidden = false;
      input.focus();
      return;
    }
    busy = true;
    save.disabled = cancel.disabled = input.disabled = true;
    try {
      const renamed = await call('Rename', y.name, label);
      // Update every visible label immediately; the changed event also refreshes
      // other windows. IDs and conversation/draft caches stay untouched.
      const current = state.snap.yesmen.find((item) => item.name === y.name);
      if (current) current.displayName = renamed.displayName;
      render();
      dialog.close('save');
    } catch (err) {
      error.textContent = errorText(err);
      error.hidden = false;
    } finally {
      busy = false;
      save.disabled = cancel.disabled = input.disabled = false;
      if (dialog.open) input.focus();
    }
  };
  const answer = openDialog(dialog);
  input.focus();
  input.select();
  await answer;
  form.onsubmit = input.oninput = dialog.oncancel = cancel.onclick = null;
}

async function confirmAction(title, body, ok) {
  const dialog = $('#confirm-dialog');
  $('#confirm-title').textContent = title;
  $('#confirm-body').textContent = body;
  $('#confirm-ok').textContent = ok;
  // Not openDialog: a close that arrives late must not answer the next
  // confirmation.
  return (await openModal(dialog)) === 'ok';
}

async function dismiss(name) {
  if (!(await confirmAction(t('Dismiss {name}?', { name: yesmanLabel(name) }), t('Its agents stop and its workspace moves to the archive. Branches stay in the repo pool.'), t('Dismiss')))) return;
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

// ---------- Shortcuts ----------

// The conversations in the sidebar's order, as it shows them: each yesman's
// lead, then the workers it lists.
const sidebarAgents = () => [...document.querySelectorAll('#yesmen .agent-row:is([data-sidebar-entry="yesman"], [data-sidebar-entry="worker"])')]
  .map((row) => agentById(row.dataset.agent)).filter(Boolean);

function latestRecallCandidate() {
  const a = selectedAgent();
  const textarea = state.view.kind === 'agent' ? $('.composer textarea') : null;
  if (!a || !mayRecallInComposer(textarea, state.attachments.get(keyOf(a)))) return null;
  const c = convo(a);
  const delivery = [...c.deliveries.values()].filter(d => d.canCancel && !c.recalled.has(d.id) && !c.actions.has(d.id)).sort((a,b) => b.order - a.order)[0];
  return delivery ? { c, delivery } : null;
}

// What the actions do, by the window's own functions.
const actionHost = {
  t,
  selectedAgent,
  yesmanOf: (a) => (a ? yesmanOf(a.id) : null),
  label: yesmanLabel,
  entries: sidebarAgents,
  leads: () => state.snap.yesmen.map((y) => agentById(`${y.name}/lead`)).filter(Boolean),
  needsYou: () => state.snap.agents.find((a) => a.role === 'lead' && a.status === 'waiting') ?? null,
  // The Needs you pane of the yesman shown, with focus on its heading.
  showNeedsYou: () => showNeedsYou(),
  step(by) {
    const list = sidebarAgents();
    const at = list.findIndex((a) => a.id === state.view.id);
    const next = at < 0 ? list[by > 0 ? 0 : list.length - 1] : list[(at + by + list.length) % list.length];
    if (next) selectAgent(next.id);
  },
  selectAgent,
  composer: () => (state.view.kind === 'agent' ? $('#dock textarea') : null),
  canRecall: ({ event } = {}) => !(event?.isComposing || event?.keyCode === 229) && !!latestRecallCandidate(),
  recallLatest() {
    const candidate = latestRecallCandidate();
    if (candidate) return recallMessage(candidate.c, candidate.delivery.id);
  },
  abort: (a) => call('Abort', a.id),
  sleep: (a) => call('Stop', a.id),
  // Another dialog than the palette, which closes before an action runs.
  dialogOpen: () => !!document.querySelector('dialog[open]:not(.palette-dialog)'),
  showSettings(page) {
    showSettings(page ?? state.settingsPage);
    // The page replaced what had focus: Settings › Shortcuts focuses its
    // search, another page its own item in Settings.
    const now = document.activeElement;
    if (!now || now === document.body) {
      (state.view.page === 'shortcuts' ? $('#shortcut-search') : $('#settings-nav [aria-current="page"]'))?.focus({ preventScroll: true });
    }
  },
  settingsPages: () => SETTINGS,
  openHire,
  togglePane,
  togglePanel,
  files: () => files,
  openCheckout,
  renameYesman,
  removeWorker,
  dismiss,
  addRepos() {
    showSettings('repositories');
    openAddRepos();
  },
  fetchAll: () => call('FetchAll'),
  palette: () => palette,
};
registerActions(shortcuts, actionHost);

// The message box is drawn again as its agent starts or stops: closing the
// palette gives focus back to the same agent's box, never another's.
const composerKey = () => (state.view.kind === 'agent' ? keyOf(selectedAgent() ?? {}) : null);
const palette = createPalette({
  shortcuts, items: () => paletteItems(actionHost), fail,
  place: () => ({ agent: composerKey(), composer: !!document.activeElement?.matches?.('#dock textarea') }),
  back: (where) => (where?.composer && where.agent && composerKey() === where.agent ? $('#dock textarea') : null),
});

// Settings › Shortcuts, in shortcuts-page.js.
const shortcutsPage = createShortcutsPage({
  shortcuts,
  confirm: (title, body, ok) => confirmAction(title, body, ok),
  shown: () => state.view.kind === 'settings' && state.view.page === 'shortcuts',
  rerender: () => renderBody(),
  settingsSection,
});

// Controls that show their action's keys show the keys chosen now.
let started = false;
shortcuts.onChange(() => {
  if (!started) return;
  renderSidebar();
  renderBarActions();
  if (state.view.kind === 'settings' && state.view.page === 'shortcuts') shortcutsPage.renderList();
  else if (state.view.kind === 'agent') renderDock();
});

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
  const sidebarFocus = $('#yesmen').contains(document.activeElement) ? document.activeElement : null;
  render();
  palette.refresh();
  // The rebuilt composer can take initial focus during a full state refresh.
  // Preserve the sidebar's stable agent + semantic entry after that rebuild.
  restoreSidebarFocus(sidebarFocus);
}

function onAgents(agents) {
  const before = selectedAgent();
  const ids = state.snap.agents.map((a) => a.id).join();
  const requests = needsYouItems(state.snap).map((item) => item.key).join();
  state.snap.agents = agents;
  pruneCaches();
  renderSidebar();
  palette.refresh();
  const changed = needsYouItems(state.snap).map((item) => item.key).join() !== requests;
  if (changed) info.render(); // its Needs you row counts them
  // The board links a task's owner while it is one of the agents, and so
  // does Needs you who asks.
  if (state.pane === 'board' && agents.map((a) => a.id).join() !== ids) board.render();
  if (state.pane === 'needs-you' && (changed || agents.map((a) => a.id).join() !== ids)) renderNeedsYouPane();
  piPage.onAgents();
  const after = selectedAgent();
  // The work row or the line at the foot says what the agent does now.
  const shown = after && state.convos.get(keyOf(after));
  if (shown?.loaded && (after.status !== before?.status || after.activity !== before?.activity || keyOf(after) !== keyOf(before ?? {}))) schedulePatch(shown);
  if (after?.status === 'idle' && before?.status === 'starting') loadModelOptions(after);
  if (state.view.kind === 'agent' && !after) {
    refreshSoon();
    return;
  }
  if (after && (before?.status !== after.status || before?.model !== after.model || before?.provider !== after.provider || before?.effort !== after.effort || JSON.stringify(before?.effortLevels) !== JSON.stringify(after.effortLevels) || before?.detail !== after.detail || JSON.stringify(before?.dialog) !== JSON.stringify(after.dialog))) {
    renderBar();
    renderDock();
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
  palette.relocalize();
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
  started = true;
  // yesman answers every context menu: the rows' own, or the page's
  // (contextmenu.js), never the web view's.
  createContextMenu({ call, fail, platform: detectPlatform(mygo), editorOf: editorViewOf }).install();
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
  // After the files pane's ⌘S, which keeps its key.
  shortcuts.listen();
  shortcuts.load().catch(fail);
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
