// The window's actions, for shortcuts.js: what the command palette, Settings
// › Shortcuts and the keys run. Each calls what the window's own buttons
// and menus call, through host, with their checks and confirmations, on
// the agent shown when it runs.
//
// Labels, groups and reasons are English, said in the page's language.

// The yesman in the sidebar's nth place, 1 to 9, has ⌘1 to ⌘9.
const GOTO = [1, 2, 3, 4, 5, 6, 7, 8, 9];

export function registerActions(shortcuts, host) {
  const agent = () => host.selectedAgent();
  const needAgent = () => (agent() ? true : 'Open a conversation first.');
  const needWorker = () => (agent()?.role === 'worker' ? true : 'Open a worker’s conversation first.');
  const yesman = () => host.yesmanOf(agent());

  shortcuts.register([
    // ---------- General ----------
    {
      id: 'palette.open', label: 'Show all commands', group: 'General', keys: ['Mod+K', 'Mod+Shift+P'],
      keywords: ['command palette', 'search', 'find'], palette: false,
      run: () => host.palette().open(),
    },
    {
      id: 'settings.open', label: 'Go to Settings', group: 'General', keys: ['Mod+,'], keywords: ['open', 'preferences'],
      run: () => host.showSettings(),
    },
    {
      id: 'settings.shortcuts', label: 'Open keyboard shortcuts', group: 'General', keys: ['Mod+/'], keywords: ['keys', 'bindings', 'hotkeys'],
      run: () => host.showSettings('shortcuts'),
    },
    {
      id: 'yesman.hire', label: 'Hire a yesman…', group: 'General', keys: ['Mod+N'], keywords: ['new', 'create', 'workspace'],
      when: () => (host.dialogOpen() ? 'A dialog is open.' : true),
      run: () => host.openHire(),
    },
    {
      // Unbound: restarting is not a key to press by accident.
      id: 'app.restart', label: 'Restart and continue', group: 'General', keywords: ['restart', 'relaunch', 'reload', 'resume', 'update'],
      when: () => (host.dialogOpen() ? 'A dialog is open.' : true),
      run: () => host.restart(),
    },

    // ---------- Conversation ----------
    {
      id: 'composer.focus', label: 'Focus the message box', group: 'Conversation', keys: ['Mod+L'], keywords: ['composer', 'input', 'type'],
      when: () => (host.composer() ? true : 'Open a conversation first.'),
      run: () => {
        const box = host.composer();
        box.focus();
        box.setSelectionRange(box.value.length, box.value.length);
      },
    },
    {
      id: 'composer.recall', label: 'Cancel and edit the newest queued message', group: 'Conversation', keys: ['ArrowUp'], palette: false,
      context: { id: 'composer', label: 'In the message box', test: target => !!target?.matches?.('.composer textarea'), bare: ['ArrowUp'] },
      when: ctx => host.canRecall(ctx) ? true : 'An empty message box and a recallable queued input are required.',
      run: () => host.recallLatest(),
    },
    {
      id: 'agent.stop', label: 'Stop the current run', group: 'Conversation', keys: ['Mod+.'], keywords: ['abort', 'interrupt', 'cancel'],
      when: () => {
        const a = agent();
        if (!a) return 'Open a conversation first.';
        return a.status === 'working' || a.status === 'waiting' ? true : 'This agent is not running.';
      },
      run: () => host.abort(agent()),
    },
    {
      id: 'agent.sleep', label: 'Put this agent to sleep', group: 'Conversation', keywords: ['stop process', 'end'],
      when: () => {
        const a = agent();
        if (!a) return 'Open a conversation first.';
        return a.status === 'stopped' ? 'This agent is asleep already.' : true;
      },
      run: () => host.sleep(agent()),
    },

    // ---------- Go to ----------
    {
      id: 'agent.next', label: 'Next conversation', group: 'Go to', keys: { mac: ['Mod+Alt+ArrowDown'], other: ['Ctrl+PageDown'] },
      keywords: ['switch', 'agent', 'worker'], repeat: true,
      when: () => (host.entries().length > 1 ? true : 'There is no other conversation.'),
      run: () => host.step(1),
    },
    {
      id: 'agent.previous', label: 'Previous conversation', group: 'Go to', keys: { mac: ['Mod+Alt+ArrowUp'], other: ['Ctrl+PageUp'] },
      keywords: ['switch', 'agent', 'worker'], repeat: true,
      when: () => (host.entries().length > 1 ? true : 'There is no other conversation.'),
      run: () => host.step(-1),
    },
    {
      id: 'agent.lead', label: 'Go to this yesman’s lead', group: 'Go to', keywords: ['parent', 'manager'],
      when: needWorker,
      run: () => host.selectAgent(`${agent().yesman}/lead`),
    },
    {
      id: 'agent.needs-you', label: 'Go to the yesman that needs you', group: 'Go to', keywords: ['waiting', 'question', 'answer'],
      when: () => (host.needsYou() ? true : 'No yesman needs you now.'),
      run: () => host.selectAgent(host.needsYou().id),
    },
    {
      // Unbound: a key is the user's to give it in Settings › Shortcuts.
      // It opens the Needs you pane of the yesman shown, beside its
      // conversation.
      id: 'needs-you.show', label: 'Show Needs You', group: 'Go to', keywords: ['requests', 'inbox', 'waiting', 'handoffs'],
      when: needAgent, run: () => host.showNeedsYou(),
    },
    ...GOTO.map((n) => ({
      id: `yesman.goto.${n}`, label: 'Go to yesman {n}', params: { n }, group: 'Go to', keys: [`Mod+${n}`], palette: false,
      when: () => (host.leads()[n - 1] ? true : host.t('There is no yesman {n}.', { n })),
      run: () => host.selectAgent(host.leads()[n - 1].id),
    })),

    // ---------- Panels and files ----------
    {
      id: 'pane.files', label: 'Show or hide the files', group: 'Panels', keys: ['Mod+Shift+E'], keywords: ['explorer', 'workspace', 'tree'],
      when: needAgent, run: () => host.togglePane('files'),
    },
    {
      id: 'pane.board', label: 'Show or hide the board', group: 'Panels', keys: ['Mod+Shift+B'], keywords: ['tasks', 'todo'],
      when: needAgent, run: () => host.togglePane('board'),
    },
    {
      id: 'pane.topics', label: 'Show or hide the topics', group: 'Panels', keywords: ['questions', 'discussions', 'conclusions', 'qa'],
      when: needAgent, run: () => host.togglePane('topics'),
    },
    {
      id: 'pane.info', label: 'Show or hide the info', group: 'Panels', keywords: ['worktrees', 'storage', 'panel'],
      when: needAgent, run: () => host.togglePanel(),
    },
    {
      id: 'files.find', label: 'Go to a file…', group: 'Panels', keys: ['Mod+P'], keywords: ['open file', 'find file', 'search'],
      when: needAgent, run: () => host.files().focusFind(),
    },
    {
      id: 'files.tree', label: 'Show or hide the file tree', group: 'Panels', keys: ['Mod+Shift+Y'], keywords: ['sidebar'],
      when: () => (agent() && host.files().current() ? true : 'Open the files first.'),
      run: () => host.files().toggleTree(),
    },
    {
      id: 'files.knowledge', label: 'Open Knowledge', group: 'Panels', keywords: ['notes', 'index'],
      when: needAgent, run: () => host.files().openKnowledge(),
    },
    {
      id: 'files.task-log', label: 'Open Working Logs', group: 'Panels', keywords: ['task log', 'history'],
      when: needAgent, run: () => host.files().openTaskLog(),
    },

    // ---------- This yesman ----------
    {
      id: 'yesman.checkout', label: 'Check out a repository…', group: 'This yesman', keywords: ['worktree', 'branch'],
      when: () => (yesman() ? true : 'Open a conversation first.'),
      run: () => host.openCheckout(yesman()),
    },
    {
      id: 'yesman.rename', label: 'Rename this yesman…', group: 'This yesman', keywords: ['label', 'name'],
      when: () => (yesman() ? true : 'Open a conversation first.'),
      run: () => host.renameYesman(yesman()),
    },
    {
      id: 'worker.remove', label: 'Remove this worker…', group: 'This yesman', keywords: ['delete'],
      when: needWorker, run: () => host.removeWorker(agent()),
    },
    {
      id: 'yesman.dismiss', label: 'Dismiss this yesman…', group: 'This yesman', keywords: ['archive', 'delete'],
      when: () => (yesman() ? true : 'Open a conversation first.'),
      run: () => host.dismiss(agent().yesman),
    },

    // ---------- Settings ----------
    ...host.settingsPages().map((p) => ({
      id: `settings.page.${p.page}`, label: 'Settings: {page}', params: () => ({ page: host.t(p.label) }), group: 'Settings', keywords: ['preferences', p.label],
      run: () => host.showSettings(p.page),
    })),
    {
      id: 'repos.add', label: 'Add repositories…', group: 'Settings', keywords: ['pool', 'clone', 'github'],
      when: () => (host.dialogOpen() ? 'A dialog is open.' : true),
      run: () => host.addRepos(),
    },
    {
      id: 'repos.fetch-all', label: 'Fetch all repositories', group: 'Settings', keywords: ['pool', 'update', 'pull'],
      run: () => host.fetchAll(),
    },
  ]);
}

// paletteItems are the palette's yesmen and workers, by their stable IDs;
// labels only show.
export function paletteItems(host) {
  const items = [];
  for (const a of host.entries()) {
    const current = host.selectedAgent()?.id === a.id;
    const label = a.role === 'lead' ? host.label(a.yesman) : `${a.number ? `a${a.number} ` : ''}${a.title || a.name}`;
    items.push({
      key: a.id,
      label,
      group: a.role === 'lead' ? host.t('yesman') : host.t('Worker of {name}', { name: host.label(a.yesman) }),
      keywords: [a.id, a.name, host.label(a.yesman)],
      when: () => (current ? 'This is the conversation shown.' : true),
      run: () => host.selectAgent(a.id),
    });
  }
  return items;
}
