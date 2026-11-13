// Settings › pi: the packages pi loads into every agent, and the packages
// of pi's gallery to install, found in the npm registry. Installing,
// updating and removing run pi's own commands in the app, one at a time;
// the "pi-packages" event follows them.
import { $, h, icon } from './dom.js';
import { renderMarkdown } from './markdown.js';
import { initTabs, openDialog } from './mds/js/magpie.js';

const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });
const KINDS = { extensions: 'extensions', skills: 'skills', prompts: 'prompt templates', themes: 'themes' };
const GALLERY = 'https://pi.dev/packages';
const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
const exactVersion = /^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$/;

// What a package gives pi, in words.
const provides = (kinds) => (kinds?.length ? kinds.map((k) => KINDS[k] ?? k).join(', ') : '');

// An npm source pinned to an exact version, which updates keep.
const pinned = (p) => p.kind === 'npm' && exactVersion.test(p.version ?? '');

// A job's state, as a row says it.
const JOB_WORDS = {
  install: { queued: 'waiting to install', running: 'installing…', failed: 'Could not install' },
  update: { queued: 'waiting to update', running: 'updating…', failed: 'Could not update' },
  remove: { queued: 'waiting to remove', running: 'removing…', failed: 'Could not remove' },
};

// READMEs are written for GitHub, in Markdown with HTML in it, which the
// page's renderer shows as text: outside code, drop the HTML, images and
// the empty links badges leave, and links within the repository.
function readmeText(src) {
  let fence = null;
  return (src ?? '').replace(/\r\n?/g, '\n').replace(/<!--[\s\S]*?-->/g, '').split('\n').map((line) => {
    const mark = line.match(/^\s*(```+|~~~+)/);
    if (mark) {
      if (!fence) fence = mark[1];
      else if (line.trim().startsWith(fence)) fence = null;
      return line;
    }
    if (fence) return line;
    return line
      .replace(/<(https?:\/\/[^\s<>]+)>/g, '$1')
      .replace(/<\/?[A-Za-z][\w-]*(?:\s[^<>]*)?\/?>/g, '')
      .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
      .replace(/\[\s*\]\([^)]*\)/g, '')
      // A link within the repository has nowhere to go here: keep its text.
      .replace(/\[([^\]\n]+)\]\((?!https?:|mailto:)[^)\s]*\)/g, '$1');
  }).join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

export function createPiPage({ call, fail, confirm, agents, shown, rerender, ago, preferenceRow, revealButton }) {
  const s = {
    view: null, // Yesman.PiPackages
    tab: 'info', // info, installed or browse
    updates: {}, // npm name -> its latest version
    jobs: { jobs: [], changedAt: null }, // Yesman.PiJobs
    query: '',
    results: null, // { query, total, packages } searched for the query
    searching: false,
    searchError: '',
    seq: 0,
    timer: 0,
  };
  let searchInput = null;
  let tabs = null; // initTabs of the page shown

  const mounted = () => shown() && $('#pi-page');
  const jobs = () => s.jobs.jobs ?? [];
  const jobFor = (op, source) => jobs().find((j) => j.op === op && j.source === source);
  const installedNames = () => new Set((s.view?.packages ?? []).filter((p) => p.kind === 'npm').map((p) => p.name));

  // ---------- Loading ----------

  async function load() {
    let v;
    try {
      v = await call('PiPackages');
    } catch (err) {
      fail(err);
      return;
    }
    s.view = v;
    s.jobs = { jobs: v.jobs, changedAt: v.changedAt };
    if (mounted()) paint();
    else if (shown()) rerender();
    loadUpdates();
  }

  async function loadUpdates() {
    const names = (s.view?.packages ?? []).filter((p) => p.kind === 'npm' && p.path && !pinned(p)).map((p) => p.name);
    if (!names.length) return;
    try {
      s.updates = await call('PiPackageUpdates', names);
    } catch {
      return; // offline: no update hints
    }
    if (mounted()) { paintInstalled(); paintInfo(); }
  }

  // A job that left the list ended: the packages are read again, so its
  // row turns into the package's, or goes, without a gap.
  async function onJobs(next) {
    const ids = new Set(next.jobs.map((j) => j.id));
    const ended = jobs().some((j) => !ids.has(j.id));
    s.jobs = next;
    if (ended) await load();
    else if (mounted()) paint();
  }

  // ---------- Page ----------

  function render(inner) {
    if (!s.view) {
      inner.append(h('p', { class: 'toolbar-lead' }, 'Reading pi’s packages…'));
      return;
    }
    const tab = (id, label) => h('button', { type: 'button', role: 'tab', id: `pi-tab-${id}`, 'aria-controls': `pi-${id}`, 'aria-selected': String(s.tab === id) }, label);
    const tablist = h('div', { class: 'mds-tabs', role: 'tablist', 'aria-label': 'pi' },
      tab('info', 'Info'), tab('installed', h('span', { id: 'pi-installed-label' }, 'Installed')), tab('browse', 'Browse'));
    inner.append(h('div', { id: 'pi-page', class: 'pi-page' },
      h('p', { class: 'toolbar-lead' }, 'Packages add extensions, skills, prompt templates and themes to pi. The ones installed here load into every yesman agent, and into pi wherever you run it.'),
      h('div', { class: 'mds-toolbar' }, tablist, h('div', { class: 'mds-toolbar-actions', id: 'pi-actions' })),
      h('div', { id: 'pi-notice', hidden: true }),
      h('div', { id: 'pi-info', role: 'tabpanel', 'aria-labelledby': 'pi-tab-info' }),
      h('div', { id: 'pi-installed', role: 'tabpanel', 'aria-labelledby': 'pi-tab-installed' },
        h('div', { class: 'card-list', id: 'pi-installed-list', role: 'list', 'aria-label': 'Installed packages' })),
      h('div', { id: 'pi-browse', role: 'tabpanel', 'aria-labelledby': 'pi-tab-browse' },
        h('div', { class: 'card-list', id: 'pi-results', role: 'list', 'aria-label': 'Packages', 'aria-busy': 'false' }),
        h('div', { class: 'pi-browse-foot', id: 'pi-browse-foot' }))));
    tabs = initTabs(tablist, { onSelect: (t) => { s.tab = t.id.replace('pi-tab-', ''); paintActions(); if (s.tab === 'browse') { if (!s.results) search(); searchInput?.focus(); } } });
    paint();
    if (s.tab === 'browse' && !s.results && !s.searching) search();
  }

  function paint() {
    paintActions();
    paintNotice();
    paintInfo();
    paintInstalled();
    paintResults();
  }

  // Shows a tab, as clicking it does.
  function showTab(id) {
    const t = $(`#pi-tab-${id}`);
    if (tabs && t) tabs.select(t);
  }

  function paintActions() {
    const actions = $('#pi-actions');
    if (!actions) return;
    if (s.tab === 'browse') {
      // The search field stays put while the page repaints, keeping focus.
      if (searchInput && actions.contains(searchInput)) {
        $('.pi-search').toggleAttribute('data-busy', s.searching);
        return;
      }
      searchInput ??= h('input', { class: 'mds-input', type: 'search', placeholder: 'Search packages', 'aria-label': 'Search pi packages', spellcheck: 'false', autocomplete: 'off',
        oninput: (e) => { s.query = e.currentTarget.value; clearTimeout(s.timer); s.timer = setTimeout(search, 350); },
        onkeydown: (e) => { if (e.key === 'Enter') { e.preventDefault(); clearTimeout(s.timer); search(); } } });
      searchInput.value = s.query;
      actions.replaceChildren(h('label', { class: 'search pi-search' }, icon('search'), searchInput, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })));
      $('.pi-search').toggleAttribute('data-busy', s.searching);
      return;
    }
    if (s.tab === 'info') {
      actions.replaceChildren();
      return;
    }
    const all = jobFor('update', '');
    const busy = all && all.state !== 'failed';
    const any = (s.view?.packages ?? []).some((p) => p.path);
    actions.replaceChildren(
      h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: openSourceDialog }, icon('plus'), 'Install from source…'),
      h('button', { class: 'mds-button mds-button--small pi-update-all', type: 'button', disabled: busy || !any, title: 'Update every package that is not pinned to a version',
        onclick: () => call('UpdatePiPackage', '', 'all packages').then(acceptJobs, fail) },
        busy ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : icon('up'), 'Update all'));
  }

  function acceptJobs(next) {
    s.jobs = next;
    if (mounted()) paint();
  }

  // Agents started before the packages last changed run with them as
  // they were: pi loads packages when it starts.
  function paintNotice() {
    const el = $('#pi-notice');
    if (!el) return;
    const changed = s.jobs.changedAt ? new Date(s.jobs.changedAt).getTime() : 0;
    const stale = changed ? agents().filter((a) => !['stopped', 'failed'].includes(a.status) && a.startedAt && new Date(a.startedAt).getTime() < changed) : [];
    el.hidden = !stale.length;
    if (!stale.length) {
      el.replaceChildren();
      return;
    }
    const idle = stale.filter((a) => a.status === 'idle').length;
    const busy = stale.length - idle;
    const names = stale.map((a) => a.id).join(', ');
    el.replaceChildren(h('div', { class: 'mds-notice pi-notice', role: 'status' },
      h('div', null,
        h('strong', null, `${plural(stale.length, 'agent')} still ${stale.length === 1 ? 'runs' : 'run'} with the packages as they were`),
        h('p', { title: names }, `pi loads packages when it starts. An agent put to sleep loads them when you next talk to it, and resumes its session.${busy ? ` ${busy === 1 ? 'One is' : `${busy} are`} busy; put ${busy === 1 ? 'it' : 'them'} to sleep from ${busy === 1 ? 'its' : 'their'} conversation once ${busy === 1 ? 'it is' : 'they are'} done.` : ''}`)),
      idle ? h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: sleepStale }, busy ? `Put ${idle} idle to sleep` : stale.length === 1 ? 'Put it to sleep' : 'Put them to sleep') : null));
  }

  async function sleepStale(e) {
    e.currentTarget.disabled = true;
    try {
      await call('SleepStaleAgents');
    } catch (err) {
      fail(err);
    }
    paintNotice();
  }

  // ---------- Installed ----------

  function paintInstalled() {
    const list = $('#pi-installed-list');
    if (!list) return;
    const v = s.view;
    const label = $('#pi-installed-label');
    if (label) label.textContent = v.packages.length ? `Installed · ${v.packages.length}` : 'Installed';
    if (v.piError) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, 'pi was not found'), h('span', { class: 'danger' }, v.piError)));
      return;
    }
    const rows = [];
    const all = jobFor('update', '');
    if (all) rows.push(jobRow(all, 'All packages'));
    const listed = new Set();
    for (const p of v.packages) {
      listed.add(p.raw);
      const job = jobs().find((j) => j.source === p.raw && j.op !== 'install') ?? jobs().find((j) => j.source === p.raw);
      rows.push(packageRow(p, job));
    }
    // Installs of packages the settings do not list yet.
    for (const j of jobs()) {
      if (j.op === 'install' && !listed.has(j.source)) rows.push(jobRow(j, j.title));
    }
    if (v.listError) rows.unshift(h('div', { class: 'card-empty' }, h('span', { class: 'danger' }, `pi list failed: ${v.listError}`)));
    if (!rows.length) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, 'No packages yet'),
        h('span', null, 'Browse pi’s gallery for extensions, skills, prompt templates and themes, or install one from npm, git or a folder.')));
      return;
    }
    list.replaceChildren(...rows);
  }

  function packageRow(p, job) {
    const latest = p.kind === 'npm' && !pinned(p) ? s.updates[p.name] : '';
    const outdated = latest && p.current && latest !== p.current;
    const busy = job && job.state !== 'failed';
    const words = job && JOB_WORDS[job.op];
    const where = p.kind === 'npm' ? (pinned(p) ? `npm, pinned to ${p.version}` : 'npm') : p.kind === 'git' ? p.raw.replace(/^git:/, '') : p.raw;
    const meta = [p.current ? `v${p.current}` : p.path ? null : 'not installed yet', where, provides(p.provides), p.filtered ? 'some resources off' : null]
      .filter(Boolean).join(' · ');
    const action = (label, glyph, onclick, extra = {}) =>
      h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick, ...extra }, icon(glyph));
    const name = p.kind === 'npm'
      ? h('button', { class: 'pi-name', type: 'button', title: `About ${p.name}`, onclick: () => openDetails(p.name) }, p.title)
      : h('strong', { title: p.raw }, p.title);
    return h('div', { class: 'card-row pi-row', role: 'listitem', 'data-busy': busy ? 'true' : null },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('div', { class: 'card-row-text' }, name,
        job?.state === 'failed'
          ? h('span', { class: 'danger', title: job.error }, `${words.failed}: ${job.error}`)
          : h('span', { title: p.description || p.raw }, busy ? words[job.state] : p.description ? `${meta} — ${p.description}` : meta)),
      outdated && !busy ? h('span', { class: 'card-row-aside pi-latest' }, `${latest} available`) : null,
      busy
        ? h('span', { class: 'card-row-actions' }, h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', disabled: true, 'aria-label': `${p.title} is ${words[job.state].replace('…', '')}` }, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })))
        : h('span', { class: 'card-row-actions' },
            job?.state === 'failed' ? action(`Try again`, 'reload', () => call('RetryPiJob', job.id).catch(fail)) : null,
            job?.state === 'failed' ? action(`Dismiss`, 'x', () => call('DismissPiJob', job.id).catch(fail)) : null,
            !job && !pinned(p) ? action(outdated ? `Update ${p.title} to ${latest}` : `Update ${p.title}`, 'up', () => call('UpdatePiPackage', p.raw, p.title).then(acceptJobs, fail)) : null,
            !job ? action(`Remove ${p.title}`, 'trash', () => removePackage(p)) : null));
  }

  // A job with no installed package to show it on: installing a new one,
  // or updating them all.
  function jobRow(j, title) {
    const failed = j.state === 'failed';
    const words = JOB_WORDS[j.op];
    const action = (label, glyph, onclick) =>
      h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', title: label, 'aria-label': label, onclick }, icon(glyph));
    return h('div', { class: 'card-row pi-row', role: 'listitem', 'data-busy': failed ? null : 'true' },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('div', { class: 'card-row-text' }, h('strong', { title: j.source || title }, title),
        failed ? h('span', { class: 'danger', title: j.error }, `${words.failed}: ${j.error}`) : h('span', null, words[j.state])),
      failed
        ? h('span', { class: 'card-row-actions' },
            action('Try again', 'reload', () => call('RetryPiJob', j.id).catch(fail)),
            action(`Dismiss`, 'x', () => call('DismissPiJob', j.id).catch(fail)))
        : h('button', { class: 'mds-icon-button mds-icon-button--compact', type: 'button', disabled: true, 'aria-label': `${title} is ${words[j.state].replace('…', '')}` },
            h('span', { class: 'mds-spinner', 'aria-hidden': 'true' })));
  }

  async function removePackage(p) {
    if (!(await confirm(`Remove ${p.title}?`, 'pi stops loading it, here and wherever you run pi. Agents running now keep it until they restart.', 'Remove'))) return;
    call('RemovePiPackage', p.raw, p.title).then(acceptJobs, fail);
  }

  // ---------- Browse ----------

  async function search(from = 0) {
    const seq = ++s.seq;
    const query = s.query.trim();
    s.searching = true;
    s.searchError = '';
    $('.pi-search')?.toggleAttribute('data-busy', true);
    $('#pi-results')?.setAttribute('aria-busy', 'true');
    if (from === 0) paintResults();
    let r;
    try {
      r = await call('SearchPiPackages', query, from);
    } catch (err) {
      if (seq !== s.seq) return;
      s.searching = false;
      s.searchError = String(err?.message || err?.error || err);
      if (mounted()) paint();
      return;
    }
    if (seq !== s.seq) return;
    s.searching = false;
    s.results = from && s.results?.query === query
      ? { query, total: r.total, packages: [...s.results.packages, ...r.packages.filter((p) => !s.results.packages.some((q) => q.name === p.name))] }
      : { query, total: r.total, packages: r.packages };
    if (mounted()) { paintResults(); $('.pi-search')?.toggleAttribute('data-busy', false); }
  }

  function paintResults() {
    const list = $('#pi-results');
    const foot = $('#pi-browse-foot');
    if (!list) return;
    list.setAttribute('aria-busy', String(s.searching));
    const r = s.results;
    const credit = h('p', { class: 'pi-credit' }, 'From npm packages tagged pi-package, as ',
      h('a', { href: GALLERY }, 'pi’s package gallery'), ' lists them. Packages run code with your account’s permissions: install the ones you trust.');
    if (s.searchError) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('strong', null, 'Could not search'), h('span', { class: 'danger' }, s.searchError),
        h('div', null, h('button', { class: 'mds-link-button', type: 'button', onclick: () => search() }, 'Try again'))));
      foot.replaceChildren(credit);
      return;
    }
    if (!r || (s.searching && r.query !== s.query.trim() && !r.packages.length)) {
      list.replaceChildren(...Array.from({ length: 6 }, skeletonRow));
      foot.replaceChildren(credit);
      return;
    }
    if (!r.packages.length) {
      list.replaceChildren(h('div', { class: 'card-empty' }, h('span', null, r.query ? `No pi package matches “${r.query}”.` : 'The registry lists no pi packages.')));
      foot.replaceChildren(credit);
      return;
    }
    list.replaceChildren(...r.packages.map(resultRow));
    const more = r.packages.length < r.total
      ? h('button', { class: 'mds-button mds-button--small', type: 'button', disabled: s.searching, onclick: () => search(r.packages.length) },
          s.searching ? h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }) : null, `Show more · ${compact.format(r.total - r.packages.length)} left`)
      : null;
    foot.replaceChildren(h('div', { class: 'pi-more' }, h('span', null, `${compact.format(r.total)} ${r.total === 1 ? 'package' : 'packages'}`), more), credit);
  }

  function skeletonRow() {
    const bone = (w) => h('i', { class: 'picker-bone', style: `width:${w}%` });
    return h('div', { class: 'card-row pi-row pi-skeleton', 'aria-hidden': 'true' },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('div', { class: 'card-row-text' }, h('strong', null, bone(28)), h('span', null, bone(72))), null);
  }

  function installButton(name) {
    const source = `npm:${name}`;
    const job = jobFor('install', source);
    if (installedNames().has(name) && !job) return h('span', { class: 'card-row-aside pi-installed' }, icon('check'), 'Installed');
    if (job && job.state !== 'failed') {
      return h('span', { class: 'card-row-aside pi-installing' }, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }), job.state === 'queued' ? 'Waiting' : 'Installing');
    }
    return h('button', { class: 'mds-button mds-button--small', type: 'button', title: job?.error ? `The last install failed: ${job.error}` : `Install ${name}`,
      onclick: () => confirmInstall(name) }, job ? 'Try again' : 'Install');
  }

  function resultRow(p) {
    const meta = [`v${p.version}`, p.weekly ? `${compact.format(p.weekly)} a week` : null, p.date ? ago(p.date, 'published') : null, p.publisher ? `by ${p.publisher}` : null]
      .filter(Boolean).join(' · ');
    return h('div', { class: 'card-row pi-row', role: 'listitem' },
      h('span', { class: 'card-row-icon' }, icon('package')),
      h('button', { class: 'card-row-text pi-open', type: 'button', title: `About ${p.name}`, onclick: () => openDetails(p.name) },
        h('strong', null, p.name),
        p.description ? h('span', { class: 'pi-desc' }, p.description) : null,
        h('span', null, meta)),
      installButton(p.name));
  }

  async function confirmInstall(name) {
    if (!(await confirm(`Install ${name}?`, `pi loads it into every yesman agent and wherever you run pi. Its code runs with your account’s permissions, so install it only if you trust it.`, 'Install'))) return;
    install(name);
  }

  function install(name) {
    call('InstallPiPackage', name).then(acceptJobs, fail);
  }

  // ---------- A package's details ----------

  async function openDetails(name) {
    const dialog = $('#pkg-dialog');
    const close = h('button', { class: 'mds-icon-button mds-icon-button--compact pkg-close', type: 'button', title: 'Close', 'aria-label': 'Close', onclick: () => dialog.close('close') }, icon('x'));
    dialog.replaceChildren(h('div', { class: 'pkg-head' }, h('h2', { class: 'mds-heading', id: 'pkg-title' }, name), close),
      h('div', { class: 'pkg-body' }, h('p', { class: 'pkg-loading' }, h('span', { class: 'mds-spinner', 'aria-hidden': 'true' }), 'Reading it from the npm registry…')));
    const answer = openDialog(dialog);
    close.focus();
    let d;
    try {
      d = await call('PiPackage', name);
    } catch (err) {
      if (dialog.open) dialog.querySelector('.pkg-body').replaceChildren(h('p', { class: 'danger' }, String(err?.message || err?.error || err)));
      return answer;
    }
    if (!dialog.open || $('#pkg-title')?.textContent !== name) return answer;
    paintDetails(dialog, d, close);
    return answer;
  }

  function paintDetails(dialog, d, close) {
    const link = (label, url) => url ? h('a', { class: 'pkg-link', href: url, title: url }, label, icon('external')) : null;
    const meta = [d.author || d.publisher ? `by ${d.author || d.publisher}` : null, d.license, d.weekly ? `${compact.format(d.weekly)} a week` : null,
      d.date ? ago(d.date, 'published') : null, d.versions ? plural(d.versions, 'version') : null].filter(Boolean).join(' · ');
    const readme = h('div', { class: 'md pkg-readme', html: renderMarkdown(readmeText(d.readme)) });
    // Links in a README go to the web, never to files on this computer.
    for (const a of readme.querySelectorAll('a[data-path]')) a.replaceWith(a.textContent);
    const media = d.video
      ? h('video', { class: 'pkg-media', src: d.video, controls: true, muted: true, playsinline: true, preload: 'metadata' })
      : d.image ? h('img', { class: 'pkg-media', src: d.image, alt: '', loading: 'lazy' }) : null;
    const installed = installedNames().has(d.name);
    const job = jobFor('install', `npm:${d.name}`);
    const busy = job && job.state !== 'failed';
    const primary = installed || busy
      ? h('button', { class: 'mds-button mds-button--primary', type: 'button', disabled: true }, installed ? 'Installed' : 'Installing…')
      : h('button', { class: 'mds-button mds-button--primary', type: 'button', onclick: () => { install(d.name); dialog.close('install'); } }, 'Install');
    dialog.replaceChildren(
      h('div', { class: 'pkg-head' },
        h('div', { class: 'pkg-heading' }, h('h2', { class: 'mds-heading', id: 'pkg-title' }, d.name), h('span', { class: 'pkg-version' }, `v${d.version}`)),
        close),
      h('div', { class: 'pkg-body' },
        d.description ? h('p', { class: 'pkg-desc' }, d.description) : null,
        meta ? h('p', { class: 'pkg-meta' }, meta) : null,
        h('div', { class: 'pkg-links' }, link('npm', d.npm), link('Repository', d.repository), d.homepage && d.homepage !== d.repository && !d.homepage.startsWith(`${d.repository}#`) ? link('Homepage', d.homepage) : null),
        d.provides?.length ? h('p', { class: 'pkg-meta' }, `Gives pi ${provides(d.provides)}.`) : null,
        d.isPi ? null : h('p', { class: 'pkg-meta danger' }, 'This npm package is not tagged pi-package, so pi may find nothing to load in it.'),
        media,
        d.readme ? readme : h('p', { class: 'pkg-meta' }, 'It has no README.')),
      h('div', { class: 'mds-dialog-actions pkg-actions' },
        h('p', { class: 'pkg-warning' }, 'Its code runs with your account’s permissions in every agent. Install it only if you trust it.'),
        h('button', { class: 'mds-button', type: 'button', onclick: () => dialog.close('close') }, 'Close'),
        primary));
  }

  // ---------- Installing from a source ----------

  async function openSourceDialog() {
    const dialog = $('#pi-source-dialog');
    const error = h('p', { class: 'danger pi-source-error', id: 'pi-source-error', role: 'alert', hidden: true });
    const input = h('input', { class: 'mds-input mds-mono', name: 'source', required: true, autocomplete: 'off', spellcheck: 'false', autocapitalize: 'off',
      placeholder: 'npm:name, git:github.com/owner/repo, a URL or a folder', 'aria-describedby': 'pi-source-hint pi-source-error' });
    const submit = h('button', { class: 'mds-button mds-button--primary', type: 'submit' }, 'Install');
    dialog.replaceChildren(h('form', { onsubmit: async (e) => {
      e.preventDefault();
      submit.disabled = true;
      try {
        acceptJobs(await call('InstallPiPackage', input.value.trim()));
        dialog.close('install');
      } catch (err) {
        error.hidden = false;
        error.textContent = String(err?.message || err?.error || err);
      } finally {
        submit.disabled = false;
      }
    } },
      h('h2', { class: 'mds-heading' }, 'Install a package'),
      h('p', { class: 'dialog-hint', id: 'pi-source-hint' }, 'Any source pi installs: an npm package, optionally at a version, a git repository at a branch, tag or commit, or a folder on this computer. Its code runs with your account’s permissions in every agent.'),
      h('label', { class: 'field' }, h('span', null, 'Source'), input),
      error,
      h('div', { class: 'mds-dialog-actions' },
        h('button', { class: 'mds-button', type: 'button', onclick: () => dialog.close('cancel') }, 'Cancel'),
        submit)));
    const answer = openDialog(dialog);
    input.focus();
    if ((await answer) === 'install') showTab('installed');
  }

  // ---------- Info: pi itself ----------

  function paintInfo() {
    const panel = $('#pi-info');
    if (!panel) return;
    const v = s.view;
    const status = h('span', { class: 'mds-status', 'data-status': v.piError ? 'failed' : 'idle' }, h('i', { class: 'mds-dot' }), h('span', null, v.piError ? 'Not found' : 'Found'));
    const installed = v.packages.filter((p) => p.path);
    const outdated = installed.filter((p) => p.kind === 'npm' && !pinned(p) && s.updates[p.name] && p.current && s.updates[p.name] !== p.current);
    const failed = jobs().filter((j) => j.state === 'failed').length;
    const running = jobs().filter((j) => j.state !== 'failed').length;
    const summary = [
      installed.length ? plural(installed.length, 'package') + ' installed' : 'None installed yet',
      outdated.length ? `${outdated.length} with ${outdated.length === 1 ? 'an update' : 'updates'}` : null,
      running ? `${running} ${running === 1 ? 'change' : 'changes'} under way` : null,
      failed ? `${failed} failed` : null,
    ].filter(Boolean).join(' · ');
    const kinds = provides([...new Set(installed.flatMap((p) => p.provides ?? []))].sort((a, b) => Object.keys(KINDS).indexOf(a) - Object.keys(KINDS).indexOf(b)));
    panel.replaceChildren(h('div', { class: 'mds-group' },
      preferenceRow(v.version ? `pi ${v.version}` : 'pi',
        v.piError ? h('p', { class: 'danger' }, v.piError) : h('p', null, 'Every lead and worker runs this pi, with your usual pi configuration. Set YESMAN_PI to use another. ', h('span', { class: 'path' }, v.pi)),
        status),
      preferenceRow('Packages', h('p', null, summary, kinds ? h('span', { class: 'pi-info-kinds' }, `They give pi ${kinds}.`) : null),
        h('button', { class: 'mds-button mds-button--small', type: 'button', onclick: () => showTab(installed.length || running || failed ? 'installed' : 'browse') },
          installed.length || running || failed ? 'Manage' : 'Browse')),
      preferenceRow('Settings', h('p', null, 'pi’s own settings, which list the packages installed for you. ', h('span', { class: 'path' }, v.settingsFile)),
        revealButton(v.settingsFile, 'pi’s settings')),
      preferenceRow('Installs with', h('p', null, 'pi installs npm packages with its npmCommand setting, npm unless set. Set it in pi’s settings to use bun or pnpm.'),
        h('code', { class: 'pi-npm' }, v.npmCommand.join(' ')))));
  }

  return {
    load,
    render,
    onJobs,
    onAgents: () => { if (mounted()) paintNotice(); },
  };
}
