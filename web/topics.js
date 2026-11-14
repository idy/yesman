// The topics pane: a yesman's questions and discussions worth finding
// again (internal/yesmen/topics.go). Each topic has a short summary, the
// conclusions the user confirmed, the questions still open and its sources.
// The lead keeps them with yesman_topics; the user creates and edits them
// here. A topic's composer sends a message to the yesman's lead through its
// ordinary submission path, into its one conversation, with a visible line
// naming the topic. Replies stay in that conversation: nothing here guesses
// which reply answered which message.
//
// It shares the place beside the conversation with the board and the files.

import { $, h, icon } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { t, tn, formatNumber } from './i18n.js';
import { IMAGE_ACCEPT, imageFileError, readImageFile, imageURL, imagePayload } from './images.js';
import { DELIVERY_LABELS, newSubmissionID, sameSubmissionImages, topicMarker, isCommand, parseTopicMarker } from './submission.js';
import { originOf } from './conversation.js';

const STATUS = { open: 'Open', resolved: 'Resolved', archived: 'Archived' };
const FILTERS = [['open', 'Open'], ['resolved', 'Resolved'], ['archived', 'Archived'], ['all', 'All']];
const statusLabel = (s) => (STATUS[s] ? t(STATUS[s]) : s);

function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (!Number.isFinite(s)) return '';
  if (s < 60) return t('updated just now');
  if (s < 3600) return t('updated {n} min ago', { n: formatNumber(Math.round(s / 60)) });
  if (s < 86400) return t('updated {n} h ago', { n: formatNumber(Math.round(s / 3600)) });
  return t('updated {n} d ago', { n: formatNumber(Math.round(s / 86400)) });
}

const when = (iso) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
};

function mark(status) {
  switch (status) {
    case 'resolved': return icon('check', 'mds-icon topic-mark');
    case 'archived': return icon('archive', 'mds-icon topic-mark');
    default: return icon('topics', 'mds-icon topic-mark');
  }
}

const sameItems = (a, b) => a.length === b.length && a.every((x, i) => x.text === b[i].text);
const itemsOf = (list) => (list ?? []).map(({ id, text }) => ({ id, text }));
const excerpt = (text, n = 140) => {
  const s = (text ?? '').replace(/\s+/g, ' ').trim();
  return Array.from(s).length > n ? Array.from(s).slice(0, n).join('') + '…' : s;
};
const textOf = (content) => (typeof content === 'string' ? content
  : (content ?? []).filter((b) => b?.type === 'text').map((b) => b.text).join('\n'));

// createTopics returns the pane. host connects it to the page:
//   call(method, ...args), fail(err)
//   yesman()                the yesman whose agent is shown, or null
//   isOpen(), setOpen(on)   whether the pane shows
//   lead(y)                 the yesman's lead agent, or null
//   leadConvo(y)            its loaded conversation { messages, deliveries }, or null
//   openLead(y)             shows the lead's conversation
//   showMessage(y, source)  shows one message of it; resolves to 'shown', 'missing', 'ambiguous' or 'other'
//   openAgent(id), openBoard(), openKnowledge(path), openURL(url)
//   track(y, id, text, imageCount)   shows a submission as sending in the lead's conversation
//   receipt(y, delivery)             gives the lead's conversation a receipt
export function createTopics(host) {
  const data = new Map(); // `${name}@${since}` -> { topics, error, loading, again }
  const views = new Map(); // `${name}@${since}` -> { selected, query, filter, editing, notice }
  const drafts = new Map(); // `${name}@${since}/${topic}` -> composer draft

  const keyOf = (y) => `${y.name}@${y.hiredAt ?? ''}`;
  const current = () => (host.isOpen() && host.yesman()) || null;
  function viewOf(y) {
    let v = views.get(keyOf(y));
    if (!v) views.set(keyOf(y), (v = { selected: null, query: '', filter: 'open', editing: null, notice: null }));
    return v;
  }
  const entry = (y) => data.get(keyOf(y));
  const topicOf = (y, id) => entry(y)?.topics?.find((x) => x.id === id) ?? null;

  async function load(y) {
    const key = keyOf(y);
    let e = data.get(key);
    if (e?.loading) { e.again = true; return; }
    if (!e) data.set(key, (e = { topics: null, error: null, loading: false, again: false }));
    e.loading = true;
    try {
      e.topics = (await host.call('Topics', y.name, y.hiredAt)) ?? [];
      e.error = null;
    } catch (err) {
      e.error = err?.message || String(err);
    }
    e.loading = false;
    if (e.again) { e.again = false; return load(y); }
    if (current() && keyOf(current()) === key) render();
  }

  // ---------- Rendering ----------

  function render() {
    const aside = $('#topics');
    if (!aside) return;
    const y = current();
    aside.hidden = !y;
    aside.setAttribute('aria-label', t('Topics'));
    document.documentElement.dataset.topics = y ? 'open' : 'closed';
    if (!y) return;
    const e = entry(y);
    if (!e) load(y);
    const v = viewOf(y);
    // Keep focus and caret across a redraw, by the element's data-key.
    const active = aside.contains(document.activeElement) ? document.activeElement : null;
    const focusKey = active?.closest?.('[data-key]')?.dataset.key;
    const caret = active && typeof active.selectionStart === 'number' ? [active.selectionStart, active.selectionEnd] : null;
    const scroll = $('.topics-body', aside)?.scrollTop ?? 0;

    const topic = v.selected ? topicOf(y, v.selected) : null;
    let body;
    if (v.editing?.id === 'new') body = editor(y, v, null);
    else if (v.selected && v.editing?.id === v.selected && topic) body = editor(y, v, topic);
    else if (v.selected) body = detail(y, v, topic, e);
    else body = list(y, v, e);
    aside.replaceChildren(head(y, v, e), body);
    if (v.selected === v.lastSelected) body.scrollTop = scroll;
    v.lastSelected = v.selected;
    if (focusKey) {
      const again = aside.querySelector(`[data-key="${CSS.escape(focusKey)}"]`);
      if (again) {
        again.focus({ preventScroll: true });
        if (caret && typeof again.setSelectionRange === 'function') try { again.setSelectionRange(...caret); } catch {}
      }
    }
  }

  function head(y, v, e) {
    const open = (e?.topics ?? []).filter((x) => x.status === 'open').length;
    return h('div', { class: 'files-head drag' },
      h('strong', { class: 'files-title' }, t('Topics')),
      e?.topics ? h('span', { class: 'board-count' }, open ? t('{n} open', { n: formatNumber(open) }) : '') : null,
      h('div', { class: 'files-head-actions' },
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', 'data-key': 'new', title: t('New topic'), 'aria-label': t('New topic'),
          disabled: !!v.editing || !e?.topics, onclick: () => startEdit(y, 'new') }, icon('plus')),
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: t('Close the topics'), 'aria-label': t('Close the topics'), onclick: () => host.setOpen(false) }, icon('x'))));
  }

  function list(y, v, e) {
    const body = h('div', { class: 'topics-body board-body' });
    const search = h('input', { class: 'mds-input topics-search', type: 'search', 'data-key': 'search', value: v.query, placeholder: t('Search topics'), 'aria-label': t('Search topics'), spellcheck: 'false' });
    search.addEventListener('input', () => { v.query = search.value; render(); });
    const filters = h('div', { class: 'topics-filter', role: 'group', 'aria-label': t('Show topics') }, FILTERS.map(([value, label]) =>
      h('button', { class: 'topics-filter-button', type: 'button', 'data-key': `filter-${value}`, 'aria-pressed': String(v.filter === value), onclick: () => { v.filter = value; render(); } }, t(label))));
    body.append(h('div', { class: 'topics-tools' }, search, filters));
    if (!e || (e.loading && !e.topics)) {
      body.append(h('p', { class: 'board-empty' }, t('Loading…')));
      return body;
    }
    if (e.error) body.append(h('p', { class: 'board-empty', 'data-tone': 'error', role: 'alert' }, e.error));
    if (!e.topics) return body;
    if (!e.topics.length) {
      body.append(h('div', { class: 'board-empty' }, h('strong', null, t('No topics yet')),
        h('p', null, t('Questions and discussions worth finding again: a summary, the conclusions you confirmed and the questions still open. {name} adds them as you talk, or add one yourself.', { name: y.displayName || y.name }))));
      return body;
    }
    const q = v.query.trim().toLowerCase();
    const shown = e.topics.filter((x) => (v.filter === 'all' || x.status === v.filter) && (!q || [x.id, x.title, x.summary, ...x.conclusions.map((c) => c.text), ...x.questions.map((c) => c.text)]
      .some((s) => (s ?? '').toLowerCase().includes(q))))
      .sort((a, b) => (b.updatedAt ?? '').localeCompare(a.updatedAt ?? ''));
    if (!shown.length) {
      body.append(h('p', { class: 'board-empty' }, q ? t('No topics match.') : t('No topics here.')));
      return body;
    }
    body.append(h('ol', { class: 'board-list topics-list', 'aria-label': t('Topics') }, shown.map((x) => {
      const meta = [x.id, x.questions.length ? tn(x.questions.length, '{n} open question', '{n} open questions') : null, ago(x.updatedAt)].filter(Boolean).join(' · ');
      return h('li', { class: 'board-task topic-row', 'data-status': x.status },
        h('button', { class: 'board-toggle topic-open', type: 'button', 'data-key': `topic-${x.id}`, title: `${statusLabel(x.status)}: ${x.title}`,
          onclick: () => { v.selected = x.id; render(); $('#topics [data-key="back"]')?.focus({ preventScroll: true }); } },
        mark(x.status), h('span', { class: 'topic-row-text' }, h('span', { class: 'board-title' }, x.title), h('span', { class: 'topic-meta' }, meta))));
    })));
    return body;
  }

  function back(y, v, key) {
    v.selected = null;
    v.notice = null;
    render();
    $(`#topics [data-key="${CSS.escape(key)}"]`)?.focus({ preventScroll: true });
  }

  function detail(y, v, x, e) {
    const body = h('div', { class: 'topics-body board-body topic-detail' });
    body.append(h('div', { class: 'topic-nav' },
      h('button', { class: 'mds-link-button topic-back', type: 'button', 'data-key': 'back', onclick: () => back(y, v, `topic-${v.selected}`) }, icon('arrowLeft'), t('All topics'))));
    if (!x) {
      body.append(h('p', { class: 'board-empty', role: 'status' }, e?.topics
        ? t('There is no topic {id} in {name}.', { id: v.selected, name: y.displayName || y.name })
        : (e?.error || t('Loading…'))));
      return body;
    }
    const lead = host.lead(y);
    const actions = h('div', { class: 'topic-actions' },
      h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': 'edit', onclick: () => startEdit(y, x.id) }, icon('pencil'), t('Edit')),
      x.status !== 'archived'
        ? h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': 'resolve', onclick: () => change(y, x, { status: x.status === 'resolved' ? 'open' : 'resolved' }) },
          icon(x.status === 'resolved' ? 'undo' : 'check'), x.status === 'resolved' ? t('Reopen') : t('Mark resolved'))
        : null,
      x.status === 'archived'
        ? h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': 'restore', onclick: () => change(y, x, { restore: true }) }, icon('undo'), t('Restore'))
        : h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': 'archive', onclick: () => change(y, x, { archive: true }) }, icon('archive'), t('Archive')));
    body.append(
      h('h3', { class: 'topic-title' }, x.title),
      h('p', { class: 'board-meta topic-status', 'data-status': x.status },
        `${x.id} · ${statusLabel(x.status)} · ${ago(x.updatedAt)}${x.updatedBy === 'lead' ? ` · ${t('by {name}', { name: y.displayName || y.name })}` : ''}`),
      actions);
    if (v.notice) body.append(h('p', { class: 'topic-notice', role: v.notice.tone === 'error' ? 'alert' : 'status', 'data-tone': v.notice.tone }, v.notice.text));
    body.append(section(t('Summary'), x.summary ? h('div', { class: 'md', html: renderMarkdown(x.summary) }) : h('p', { class: 'topic-none' }, t('No summary yet.'))));
    body.append(section(t('Confirmed conclusions'), x.conclusions.length
      ? h('ul', { class: 'topic-items', 'data-kind': 'conclusion' }, x.conclusions.map((c) => h('li', null, icon('check', 'mds-icon topic-item-mark'), h('div', { class: 'md', html: renderMarkdown(c.text) }))))
      : h('p', { class: 'topic-none' }, t('Nothing confirmed yet.'))));
    body.append(section(t('Open questions'), x.questions.length
      ? h('ul', { class: 'topic-items', 'data-kind': 'question' }, x.questions.map((c) => h('li', null, icon('question', 'mds-icon topic-item-mark'), h('div', { class: 'md', html: renderMarkdown(c.text) }))))
      : h('p', { class: 'topic-none' }, t('No open questions.'))));
    if (x.sources.length) body.append(section(t('Sources'), h('ul', { class: 'topic-sources' }, x.sources.map((s) => sourceRow(y, s)))));
    body.append(conversation(y, x, lead));
    body.append(composer(y, x, lead));
    return body;
  }

  function section(title, ...content) {
    return h('section', { class: 'topic-section' }, h('h4', { class: 'board-label topic-heading' }, title), ...content);
  }

  // ---------- Sources ----------

  function sourceText(y, s) {
    switch (s.kind) {
      case 'message': return s.role === 'assistant' ? t('Reply in the conversation') : t('Your message in the conversation');
      case 'submission': return t('Message sent from this topic');
      case 'task': return t('Task {id}', { id: s.ref });
      case 'worker': return t('Report from worker {name}', { name: s.agent });
      case 'conversation': return t('The conversation with {name}', { name: y.displayName || y.name });
      case 'knowledge': return t('Knowledge: {path}', { path: s.ref });
      case 'link': return s.ref;
      default: return s.kind;
    }
  }

  async function openSource(y, s) {
    const v = viewOf(y);
    const say = (text, tone = 'quiet') => { v.notice = { text, tone }; render(); };
    switch (s.kind) {
      case 'message': {
        const r = await host.showMessage(y, s);
        if (r === 'missing') say(t('That message is not in the conversation’s current history: it may have been summarized, or be in an earlier session. The conversation is open.'));
        else if (r === 'ambiguous') say(t('More than one message matches this source, so none is picked out. The conversation is open.'));
        else if (r === 'other') say(t('That message belongs to an earlier yesman of the same name, so it cannot be shown.'));
        return;
      }
      case 'conversation': case 'submission': return host.openLead(y);
      case 'worker': {
        const id = `${y.name}/${s.agent}`;
        if (!host.agent(id) || (s.since && host.agent(id).since !== s.since)) return say(t('{name} is not one of {yesman}’s agents now.', { name: s.agent, yesman: y.displayName || y.name }));
        return host.openAgent(id);
      }
      case 'task': return host.openBoard();
      case 'knowledge': return host.openKnowledge(s.ref);
      case 'link': return host.openURL(s.ref);
    }
  }

  function sourceRow(y, s) {
    const label = s.label && s.kind !== 'link' ? s.label : '';
    return h('li', { class: 'topic-source', 'data-kind': s.kind },
      h('button', { class: 'mds-link-button topic-source-open', type: 'button', 'data-key': `source-${s.id}`, title: s.kind === 'link' ? s.ref : null, onclick: () => openSource(y, s) },
        sourceText(y, s)),
      label ? h('span', { class: 'topic-source-label' }, label) : null);
  }

  // ---------- The conversation ----------

  // The lead's messages about this topic: those the app recorded as sent from
  // it (each native message's origin names its submission), and any other
  // carrying its visible line. Then the submissions the topic recorded, with
  // what their receipts say, each tied to its message when the history has it.
  function conversation(y, x, lead) {
    const convo = host.leadConvo(y);
    const sendIDs = new Set(x.sends.map((s) => s.id));
    const native = new Map(); // submission ID -> message
    const rows = [];
    for (const m of convo?.messages ?? []) {
      if (m.role !== 'user') continue;
      const origin = originOf(m);
      if (origin?.source && origin.source !== 'user') continue; // a lead's task or yesman's notice
      const marker = parseTopicMarker(textOf(m.content));
      const exact = origin?.submission && sendIDs.has(origin.submission);
      if (exact) native.set(origin.submission, m);
      if (!exact && marker?.id !== x.id) continue;
      const text = marker ? marker.rest : textOf(m.content);
      rows.push(h('li', { class: 'topic-message' },
        h('button', { class: 'mds-link-button', type: 'button', 'data-key': `msg-${m.timestamp}`, onclick: () => locate(y, { kind: 'message', agent: 'lead', role: 'user', timestamp: m.timestamp, since: lead?.since }) },
          excerpt(text) || t('Attached images')),
        h('span', { class: 'topic-meta' }, when(m.timestamp))));
    }
    const sends = [...x.sends].reverse();
    const sendRows = sends.slice(0, 10).map((s) => {
      const live = convo?.deliveries?.get?.(s.id);
      const state = live?.state ?? s.state;
      const label = live ? DELIVERY_LABELS[state] : state === 'failed' ? 'Not sent' : state ? 'Submitted · delivery unconfirmed' : '';
      const m = native.get(s.id);
      return h('li', { class: 'topic-send', 'data-state': state ?? '' },
        h('span', { class: 'topic-send-text' }, excerpt(s.preview) || t('Attached images'), s.truncated ? '…' : ''),
        h('span', { class: 'topic-meta' }, [when(s.at), s.imageCount ? tn(s.imageCount, '{n} image', '{n} images') : null, m ? t('In the conversation') : label ? t(label) : null].filter(Boolean).join(' · ')),
        m ? h('button', { class: 'mds-link-button', type: 'button', 'data-key': `send-${s.id}`, onclick: () => locate(y, { kind: 'message', agent: 'lead', role: 'user', timestamp: m.timestamp, since: lead?.since }) }, t('Show in the conversation')) : null);
    });
    const content = [];
    if (rows.length) content.push(h('ul', { class: 'topic-messages' }, rows));
    else if (convo) content.push(h('p', { class: 'topic-none' }, t('No message about this topic in the conversation’s current history.')));
    else content.push(h('p', { class: 'topic-none' }, t('Open the conversation with {name} to see its messages about this topic.', { name: y.displayName || y.name })));
    if (sendRows.length) {
      content.push(h('details', { class: 'topic-sends' },
        h('summary', { title: t('What the app recorded when each message was submitted. It does not say whether the lead has read or answered it.') },
          tn(sends.length, '{n} message sent from here', '{n} messages sent from here')),
        h('ul', null, sendRows)));
    }
    content.push(h('button', { class: 'mds-link-button topic-open-convo', type: 'button', 'data-key': 'open-convo', disabled: !lead, onclick: () => host.openLead(y) }, t('Open the conversation with {name}', { name: y.displayName || y.name })));
    return section(t('In the conversation'), ...content);
  }

  // locate shows a message and says when it cannot.
  const locate = (y, source) => openSource(y, source);

  // ---------- Sending ----------

  const draftKey = (y, id) => `${keyOf(y)}/${id}`;
  function draftOf(y, id) {
    let d = drafts.get(draftKey(y, id));
    if (!d) drafts.set(draftKey(y, id), (d = { text: '', items: [], reading: false, sending: false, submissionID: null, submission: null, error: '' }));
    return d;
  }
  const shownTopic = (y, id) => current() && keyOf(current()) === keyOf(y) && viewOf(y).selected === id && !viewOf(y).editing;

  function composer(y, x, lead) {
    const d = draftOf(y, x.id);
    const name = y.displayName || y.name;
    if (x.status === 'archived') {
      return h('div', { class: 'topic-composer', 'data-archived': 'true' }, h('p', { class: 'topic-none' }, t('Restore this topic to send a message about it.')));
    }
    const busy = d.reading || d.sending;
    const refresh = () => { if (shownTopic(y, x.id)) render(); };
    const sync = () => {
      send.disabled = (!textarea.value.trim() && !d.items.length) || busy || !lead;
      textarea.style.height = 'auto';
      textarea.style.height = `${Math.min(textarea.scrollHeight, 220)}px`;
      const command = isCommand(textarea.value);
      context.hidden = command;
      commandNote.hidden = !command;
    };
    const addImages = async (files) => {
      if (d.reading || d.sending || !files.length) return;
      const error = imageFileError(files, d.items);
      if (error) return host.fail(t(error));
      d.reading = true;
      refresh();
      try {
        d.items.push(...await Promise.all(files.map(readImageFile)));
      } catch (err) {
        host.fail(t(err?.message || String(err)));
      } finally {
        d.reading = false;
        refresh();
      }
    };
    const fileInput = h('input', { type: 'file', accept: IMAGE_ACCEPT, multiple: true, hidden: true, onchange: () => { addImages([...fileInput.files]); fileInput.value = ''; } });
    const previews = h('div', { class: 'composer-images', hidden: !d.items.length }, d.items.map((image) =>
      h('div', { class: 'composer-image' },
        h('img', { src: imageURL(image), alt: image.name, title: image.name }),
        h('button', { class: 'round', type: 'button', disabled: busy, title: t('Remove image'), 'aria-label': t('Remove image'), onclick: () => { d.items = d.items.filter((i) => i !== image); refresh(); } }, icon('trash')))));
    const attach = h('button', { class: 'round', type: 'button', disabled: busy, 'aria-label': t('Attach images'),
      title: t('Attach images (PNG, JPEG, GIF, WebP; 4 images, 5 MB each, 10 MB total)'), onclick: () => fileInput.click() }, icon('plus'));
    const textarea = h('textarea', { rows: 2, 'data-key': `compose-${x.id}`, 'aria-label': t('Message {name} about this topic', { name }), readonly: d.sending,
      placeholder: t('Message {name} about this topic…', { name }), 'aria-describedby': `topic-context-${x.id}` });
    textarea.value = d.text;
    const send = h('button', { class: 'round composer-action', type: 'submit', 'data-key': `send-${x.id}`, 'aria-label': t('Send'), title: t('Send to {name}', { name }) }, icon('enter'));
    const frozen = d.submissionID ? x.sends.find((s) => s.id === d.submissionID)?.context : null;
    const context = h('p', { class: 'topic-context', id: `topic-context-${x.id}` },
      t('Goes to {name}’s conversation, beginning with this line:', { name }), ' ', h('code', null, frozen ?? topicMarker(x)));
    const commandNote = h('p', { class: 'topic-context', hidden: true }, t('Commands are sent as typed, without the topic line.'));
    textarea.addEventListener('input', () => { d.text = textarea.value; d.error = ''; sync(); });
    textarea.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
        e.preventDefault();
        form.requestSubmit();
      }
    });
    textarea.addEventListener('paste', (e) => {
      const files = [...(e.clipboardData?.items ?? [])].filter((i) => i.kind === 'file' && i.type.startsWith('image/')).map((i) => i.getAsFile()).filter(Boolean);
      if (files.length) { e.preventDefault(); addImages(files); }
    });
    const form = h('form', { class: 'composer topic-composer', onsubmit: (e) => { e.preventDefault(); submit(y, x.id, textarea); } },
      fileInput, context, commandNote, previews,
      h('div', { class: 'composer-input' }, attach, textarea, send),
      d.error ? h('p', { class: 'topic-notice', role: 'alert', 'data-tone': 'error' }, d.error) : null,
      !lead ? h('p', { class: 'topic-none' }, t('{name} has no lead to talk to now.', { name })) : null);
    form.addEventListener('dragover', (e) => { if (e.dataTransfer?.types.includes('Files')) { e.preventDefault(); e.dataTransfer.dropEffect = busy ? 'none' : 'copy'; } });
    form.addEventListener('drop', (e) => { if (e.dataTransfer?.files.length) { e.preventDefault(); addImages([...e.dataTransfer.files]); } });
    queueMicrotask(sync);
    return section(t('Continue the discussion'), form);
  }

  // submit sends the draft of topic id of yesman y. Everything it needs is
  // taken from its arguments, never from what the pane shows by then.
  async function submit(y, id, textarea) {
    const d = draftOf(y, id);
    const x = topicOf(y, id);
    const lead = host.lead(y);
    const text = textarea.value.trim();
    if (!x || !lead || (!text && !d.items.length) || d.reading || d.sending) return;
    const images = imagePayload(d.items);
    // An edited draft is a new intent; an unchanged retry reuses its ID.
    if (d.submissionID && d.submission && (d.submission.text !== text || !sameSubmissionImages(d.submission.images, images))) {
      d.submissionID = null;
      d.submission = null;
    }
    const messageID = d.submissionID ?? newSubmissionID();
    d.submissionID = messageID;
    d.submission ??= { text, images };
    const frozen = x.sends.find((s) => s.id === messageID)?.context;
    const line = isCommand(text) ? '' : (frozen ?? topicMarker(x));
    host.track(y, messageID, line ? (text ? `${line}\n\n${text}` : line) : text, images.length);
    d.sending = true;
    d.error = '';
    render();
    try {
      const r = await host.call('SubmitTopic', y.name, y.hiredAt, id, messageID, text, images);
      if (r?.delivery?.id) host.receipt(y, r.delivery);
      const receipt = r?.delivery ?? {};
      if (receipt.state === 'sending') return; // keep the whole draft until acceptance is known
      if (receipt.state === 'failed' || receipt.state === 'interrupted' || (receipt.state === 'unconfirmed' && receipt.error)) {
        if (receipt.state === 'failed') { d.submissionID = null; d.submission = null; }
        throw new Error(receipt.error || t(DELIVERY_LABELS[receipt.state]));
      }
      d.submissionID = null;
      d.submission = null;
      d.text = '';
      d.items = [];
    } catch (err) {
      d.error = err?.message || String(err);
      host.receipt(y, null, messageID); // a local Sending row becomes unconfirmed
    } finally {
      d.sending = false;
      load(y);
      if (shownTopic(y, id)) render();
    }
  }

  // ---------- Changing ----------

  // change applies a quick change (status, archive) at the revision shown.
  async function change(y, x, patch) {
    const v = viewOf(y);
    try {
      const r = await host.call('UpdateTopic', y.name, y.hiredAt, x.id, { revision: x.revision, ...patch });
      replace(y, r.topic);
      v.notice = r.conflict ? { text: t('This topic changed meanwhile; nothing was changed. Here it is as it is now.'), tone: 'warn' } : null;
    } catch (err) {
      v.notice = { text: err?.message || String(err), tone: 'error' };
    }
    render();
  }

  function replace(y, topic) {
    const e = entry(y);
    if (!e?.topics || !topic) return;
    const i = e.topics.findIndex((x) => x.id === topic.id);
    if (i >= 0) e.topics[i] = topic;
    else e.topics.push(topic);
  }

  function startEdit(y, id) {
    const v = viewOf(y);
    const base = id === 'new' ? null : topicOf(y, id);
    v.editing = {
      id, base,
      form: base
        ? { title: base.title, summary: base.summary ?? '', conclusions: itemsOf(base.conclusions), questions: itemsOf(base.questions), addSources: [], removeSources: [] }
        : { title: '', summary: '', conclusions: [], questions: [], addSources: [], removeSources: [] },
      conflict: null, saving: false, error: '',
    };
    if (id !== 'new') v.selected = id;
    render();
    $('#topics [data-key="edit-title"]')?.focus();
  }

  function cancelEdit(y) {
    const v = viewOf(y);
    const id = v.editing?.id;
    v.editing = null;
    render();
    $(`#topics [data-key="${id === 'new' ? 'new' : 'edit'}"]`)?.focus({ preventScroll: true });
  }

  function itemList(ed, field, label, addLabel, placeholder) {
    const items = ed.form[field];
    return h('fieldset', { class: 'topic-edit-items' }, h('legend', { class: 'board-label' }, label),
      items.map((item, i) => {
        const area = h('textarea', { class: 'mds-input', rows: 2, 'data-key': `edit-${field}-${i}`, 'aria-label': `${label} ${i + 1}`, placeholder, maxlength: 1000 });
        area.value = item.text;
        area.addEventListener('input', () => { item.text = area.value; });
        return h('div', { class: 'topic-edit-item' }, area,
          h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', 'data-key': `edit-${field}-${i}-remove`, title: t('Remove'), 'aria-label': t('Remove {what}', { what: `${label} ${i + 1}` }),
            onclick: () => { items.splice(i, 1); render(); } }, icon('trash')));
      }),
      h('button', { class: 'mds-link-button', type: 'button', 'data-key': `edit-${field}-add`, disabled: items.length >= 30,
        onclick: () => { items.push({ id: '', text: '' }); render(); $(`#topics [data-key="edit-${field}-${items.length - 1}"]`)?.focus(); } }, icon('plus'), addLabel));
  }

  function editor(y, v, x) {
    const ed = v.editing;
    const body = h('div', { class: 'topics-body board-body topic-detail topic-editor' });
    const title = h('input', { class: 'mds-input', 'data-key': 'edit-title', required: true, maxlength: 120, value: ed.form.title, 'aria-label': t('Title'), autocomplete: 'off' });
    title.addEventListener('input', () => { ed.form.title = title.value; });
    const summary = h('textarea', { class: 'mds-input', rows: 4, 'data-key': 'edit-summary', maxlength: 4000, 'aria-label': t('Summary'), placeholder: t('What the question or discussion is about') });
    summary.value = ed.form.summary;
    summary.addEventListener('input', () => { ed.form.summary = summary.value; });
    const current = x ? x.sources.filter((s) => !ed.form.removeSources.includes(s.id)) : [];
    const sources = h('fieldset', { class: 'topic-edit-items' }, h('legend', { class: 'board-label' }, t('Sources')),
      current.map((s) => h('div', { class: 'topic-edit-item topic-edit-source' }, h('span', null, sourceText(y, s), s.label ? ` — ${s.label}` : ''),
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', 'data-key': `edit-source-${s.id}-remove`, title: t('Remove'), 'aria-label': t('Remove {what}', { what: sourceText(y, s) }),
          onclick: () => { ed.form.removeSources.push(s.id); render(); } }, icon('trash')))),
      ed.form.addSources.map((s, i) => h('div', { class: 'topic-edit-item topic-edit-source' }, h('span', null, sourceText(y, s), s.label ? ` — ${s.label}` : ''),
        h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', 'data-key': `edit-newsource-${i}-remove`, title: t('Remove'), 'aria-label': t('Remove {what}', { what: sourceText(y, s) }),
          onclick: () => { ed.form.addSources.splice(i, 1); render(); } }, icon('trash')))),
      sourcePicker(y, ed));
    const conflict = ed.conflict ? h('div', { class: 'topic-notice', role: 'alert', 'data-tone': 'warn' },
      h('p', null, t('This topic changed while you edited it ({fields}). Your version is still in the form; nothing was saved.', { fields: ed.conflict.fields.map((f) => t(f)).join(', ') })),
      h('div', { class: 'topic-actions' },
        h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': 'conflict-keep', onclick: () => { ed.base = ed.conflict.current; ed.conflict = null; save(y); } }, t('Save my version')),
        h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': 'conflict-load', onclick: () => { replace(y, ed.conflict.current); startEdit(y, ed.id); } }, t('Discard mine and load theirs')))) : null;
    const form = h('form', { class: 'topic-edit-form', onsubmit: (e) => { e.preventDefault(); save(y); } },
      h('label', { class: 'field' }, h('span', null, t('Title')), title),
      h('label', { class: 'field' }, h('span', null, t('Summary')), summary),
      itemList(ed, 'conclusions', t('Confirmed conclusions'), t('Add a conclusion'), t('What the user agreed or decided')),
      itemList(ed, 'questions', t('Open questions'), t('Add a question'), t('A question still open')),
      sources,
      conflict,
      ed.error ? h('p', { class: 'topic-notice', role: 'alert', 'data-tone': 'error' }, ed.error) : null,
      h('div', { class: 'mds-dialog-actions topic-edit-actions' },
        h('button', { class: 'mds-button', type: 'button', 'data-key': 'edit-cancel', disabled: ed.saving, onclick: () => cancelEdit(y) }, t('Cancel')),
        h('button', { class: 'mds-button mds-button--primary', type: 'submit', 'data-key': 'edit-save', disabled: ed.saving }, ed.id === 'new' ? t('Create') : t('Save'))));
    form.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !ed.saving) { e.preventDefault(); cancelEdit(y); } });
    body.append(h('h3', { class: 'topic-title' }, ed.id === 'new' ? t('New topic') : t('Edit {id}', { id: ed.id })), form);
    return body;
  }

  // sourcePicker adds a source: a message of the lead's loaded conversation,
  // picked by its exact role and timestamp, a task, a Knowledge note or a link.
  function sourcePicker(y, ed) {
    const convo = host.leadConvo(y);
    const lead = host.lead(y);
    const picks = (convo?.messages ?? []).filter((m) => (m.role === 'user' || m.role === 'assistant') && m.timestamp && excerpt(textOf(m.content)))
      .slice(-40).reverse();
    const messageSelect = h('select', { class: 'mds-select', 'data-key': 'pick-message', 'aria-label': t('A message of the conversation') },
      h('option', { value: '' }, convo ? t('Add a message of the conversation…') : t('Open the conversation to pick its messages')),
      picks.map((m, i) => h('option', { value: String(i) }, `${m.role === 'user' ? t('You') : t('Lead')}: ${excerpt(parseTopicMarker(textOf(m.content))?.rest ?? textOf(m.content), 70)}`)));
    messageSelect.disabled = !picks.length;
    messageSelect.addEventListener('change', () => {
      const m = picks[Number(messageSelect.value)];
      if (!m) return;
      ed.form.addSources.push({ kind: 'message', agent: 'lead', since: lead?.since, role: m.role, timestamp: m.timestamp, label: excerpt(parseTopicMarker(textOf(m.content))?.rest ?? textOf(m.content), 120) });
      render();
    });
    const kind = h('select', { class: 'mds-select', 'data-key': 'pick-kind', 'aria-label': t('Kind of source') },
      h('option', { value: 'task' }, t('Task')), h('option', { value: 'link' }, t('Link')), h('option', { value: 'knowledge' }, t('Knowledge note')));
    const ref = h('input', { class: 'mds-input', 'data-key': 'pick-ref', 'aria-label': t('Task ID, link or note'), placeholder: 't3 · https://… · note.md', autocomplete: 'off', spellcheck: 'false' });
    const add = h('button', { class: 'mds-button mds-button--small', type: 'button', 'data-key': 'pick-add', onclick: () => {
      if (!ref.value.trim()) return;
      ed.form.addSources.push({ kind: kind.value, ref: ref.value.trim() });
      render();
    } }, t('Add'));
    return h('div', { class: 'topic-source-picker' }, messageSelect, h('div', { class: 'topic-source-ref' }, kind, ref, add));
  }

  // save creates the topic, or saves the form against the revision it was
  // started from. When the topic changed meanwhile, the parts only one side
  // changed are combined and saved; parts both changed are left to the user.
  async function save(y) {
    const v = viewOf(y);
    const ed = v.editing;
    if (!ed || ed.saving) return;
    ed.saving = true;
    ed.error = '';
    render();
    const f = ed.form;
    try {
      if (ed.id === 'new') {
        const created = await host.call('CreateTopic', y.name, y.hiredAt, {
          title: f.title, summary: f.summary,
          conclusions: f.conclusions.map((c) => c.text).filter((s) => s.trim()),
          questions: f.questions.map((c) => c.text).filter((s) => s.trim()),
          sources: f.addSources,
        });
        entry(y)?.topics?.push(created);
        v.editing = null;
        v.selected = created.id;
        render();
        $('#topics [data-key="edit"]')?.focus({ preventScroll: true });
        return;
      }
      const base = ed.base;
      const patch = { revision: base.revision };
      if (f.title !== base.title) patch.title = f.title;
      if (f.summary !== (base.summary ?? '')) patch.summary = f.summary;
      if (!sameItems(f.conclusions, base.conclusions)) patch.conclusions = f.conclusions;
      if (!sameItems(f.questions, base.questions)) patch.questions = f.questions;
      if (f.addSources.length) patch.addSources = f.addSources;
      if (f.removeSources.length) patch.removeSources = f.removeSources;
      let r = await host.call('UpdateTopic', y.name, y.hiredAt, ed.id, patch);
      if (r.conflict) {
        const merged = merge(base, f, r.topic);
        if (merged.fields.length) {
          ed.conflict = { fields: merged.fields, current: r.topic };
          replace(y, r.topic);
          return;
        }
        r = await host.call('UpdateTopic', y.name, y.hiredAt, ed.id, { ...merged.patch, revision: r.topic.revision });
        if (r.conflict) {
          ed.conflict = { fields: ['Another change'], current: r.topic };
          replace(y, r.topic);
          return;
        }
        v.notice = { text: t('Saved, together with a change made meanwhile.'), tone: 'quiet' };
      }
      replace(y, r.topic);
      v.editing = null;
      render();
      $('#topics [data-key="edit"]')?.focus({ preventScroll: true });
    } catch (err) {
      ed.error = err?.message || String(err);
    } finally {
      if (v.editing === ed) { ed.saving = false; render(); }
    }
  }

  // merge combines the form with the topic as it is now, field by field,
  // against the version the form started from.
  function merge(base, f, now) {
    const fields = [];
    const patch = {};
    const field = (name, label, mine, was, theirs, same) => {
      if (same(mine, was)) return; // only they changed it, or nobody
      if (!same(theirs, was) && !same(mine, theirs)) { fields.push(label); return; }
      patch[name] = mine;
    };
    const eq = (a, b) => a === b;
    field('title', 'Title', f.title, base.title, now.title, eq);
    field('summary', 'Summary', f.summary, base.summary ?? '', now.summary ?? '', eq);
    field('conclusions', 'Confirmed conclusions', f.conclusions, base.conclusions, now.conclusions, sameItems);
    field('questions', 'Open questions', f.questions, base.questions, now.questions, sameItems);
    if (f.addSources.length) patch.addSources = f.addSources;
    const still = f.removeSources.filter((id) => now.sources.some((s) => s.id === id));
    if (still.length) patch.removeSources = still;
    return { fields, patch };
  }

  // ---------- Following changes ----------

  // onChange follows topics changed by the lead or another window.
  function onChange(name) {
    for (const [key] of data) if (key.startsWith(`${name}@`)) {
      const y = current();
      if (y && keyOf(y) === key) load(y);
      else data.delete(key); // read again when shown
    }
    const y = current();
    if (y?.name === name && !data.has(keyOf(y))) load(y);
  }

  // open shows the pane at a topic of yesman y, or its list.
  function open(y, id = null) {
    const v = viewOf(y);
    if (id && !v.editing) { v.selected = id; v.notice = null; }
    host.setOpen(true);
    render();
    const target = id && !v.editing ? 'back' : 'search';
    $(`#topics [data-key="${target}"]`)?.focus({ preventScroll: true });
  }

  const title = (y, id) => topicOf(y, id)?.title ?? null;
  const counts = (y) => {
    const topics = entry(y)?.topics;
    return topics ? { open: topics.filter((x) => x.status === 'open').length } : null;
  };

  return { render, onChange, open, title, counts, rerender: () => { if (current()) render(); } };
}
