(() => {
  const site = window.IdySite.init({
    en: {
      pageTitle: 'yesman — Yes-man Ships!',
      pageDescription: 'Yes-man Ships! Hire a yesman to help you ship to production. A desktop tray app with a lead, workers, and a shared workspace for each project.',
      tagline: 'Yes-man Ships!',
      pitch: 'AI has a reputation for saying yes to everything. Put that yes-man energy to work: hire a yesman to help you ship to production.',
      workspaceNav: 'Why yesman', installNav: 'Install', source: 'Source',
      intro: 'yesman is a desktop tray app. Each yesman takes on one project, with a lead that talks with you and manages, and workers that do the work across whichever repositories the project needs.',
      getYesman: 'Hire a yesman', readGuide: 'Read the guide', statusLabel: 'Status', prototype: 'Early prototype', runtimeLabel: 'Agent runtime', licenseLabel: 'License', platformLabel: 'Platforms',
      workspaceTitle: 'A manager, not a coder.',
      workspaceIntro: 'Hire a yesman for a project, not a repository. You talk only to its lead. It hands the work to workers, manages them, and brings you only the calls it cannot make.',
      treeTitle: 'Example workspace', treeLabel: 'Example workspace directory layout', worktreeComment: '# worktree',
      treeNote: 'Each repository is cloned once into a shared pool of bare repositories. A yesman checks out the ones its project needs, a worktree and branch per worker.',
      leadTitle: 'The lead only talks and manages', leadBody: 'It has no shell and never does the work itself. It turns your goal into tasks on a board and hands each to a worker, without waiting on any of them.',
      workersTitle: 'It manages upward for you', workersBody: 'Workers bring their questions, failures and reports to the lead, which answers them itself. Workers never interrupt you.',
      hardCallsTitle: 'Only the hard calls reach you', hardCallsBody: 'The lead does everything it can decide. What it cannot, it brings you together, once everything else is done.',
      poolTitle: 'A project, not a repository', poolBody: 'A yesman works across whichever repositories its project needs, checked out from one pool that only yesman writes.',
      knowledgeTitle: 'It picks up where it left off', knowledgeBody: 'Shared knowledge, the board and every agent’s session survive restarts.',
      nextTitle: 'Coming next: always on', nextBody: 'A yesman connected to a channel, talking with users and customers, turning what they need into tasks, and shipping release after release.',
      installTitle: 'Install yesman', requirements: 'Install git and pi 1.5 or later, with a model configured. yesman uses your usual pi configuration.',
      platformNote: 'macOS has been run. Linux and Windows builds are available but have not been runtime-tested. macOS downloads are signed ad hoc; Windows builds are unsigned.',
      scopeNote: 'Pull-request watching is not built yet.',
      downloadsTitle: 'Downloads', releaseFallback: 'Choose a package from the latest GitHub release.',
      releaseAvailable: 'Latest release', allReleases: 'Latest release and all packages', buildSource: 'Build from source', allProjects: 'All projects', reportIssue: 'Report an issue',
    },
    zh: {
      pageTitle: 'yesman — Yes-man Ships!',
      pageDescription: 'Yes-man Ships! 雇一个 yesman，帮你把产品交付上线。一款桌面托盘应用，为每个项目配好 lead、worker 和共享工作空间。',
      tagline: 'Yes-man Ships!', workspaceNav: '为什么是 yesman', installNav: '安装', source: '源码',
      pitch: 'AI 就像个 yes-man，你说什么它都说好。那就雇一个 yesman，帮你把产品交付上线！',
      intro: 'yesman 是一款桌面托盘应用。每个 yesman 负责一个项目：lead 跟你沟通、负责管理，worker 负责干活，项目需要哪些仓库就用哪些仓库。',
      getYesman: '雇一个 yesman', readGuide: '阅读指南', statusLabel: '状态', prototype: '早期原型', runtimeLabel: '智能体运行时', licenseLabel: '许可证', platformLabel: '平台',
      workspaceTitle: '它是管理者，不是码农。',
      workspaceIntro: '为一个项目雇 yesman，而不是为一个仓库。你只跟它的 lead 对话；lead 把活派给 worker、管着它们，只把自己拿不了主意的事交给你。',
      treeTitle: '工作空间示例', treeLabel: '工作空间目录结构示例', worktreeComment: '# 工作树',
      treeNote: '每个仓库只克隆一次，作为 bare 仓库放进共享仓库池。yesman 按项目需要检出，每个 worker 一个工作树、一个分支。',
      leadTitle: 'lead 只动嘴，不动手', leadBody: '它没有 shell，从不亲自干活。它把你的目标拆成看板上的任务，逐个派给 worker，不会停下来等任何一个。',
      workersTitle: '它替你向上管理', workersBody: 'worker 的提问、失败和汇报都先到 lead，由 lead 自己处理。worker 从不打扰你。',
      hardCallsTitle: '只有难题才会找你', hardCallsBody: 'lead 能决定的都自己做完；拿不了主意的，等其他工作全部完成后，一次性交给你。',
      poolTitle: '面向项目，而不是仓库', poolBody: '一个 yesman 可以同时处理项目需要的多个仓库，它们都从同一个只由 yesman 写入的仓库池检出。',
      knowledgeTitle: '重启也能接着干', knowledgeBody: '共享知识、看板和每个智能体的会话记录，重启后都还在。',
      nextTitle: '即将推出：持续在线', nextBody: '把 yesman 接入一个沟通渠道，让它与用户和客户对话，把需求整理成任务，一版接一版地交付发布。',
      installTitle: '安装 yesman', requirements: '先安装 git 和 pi 1.5 或更新版本，并配置好模型。yesman 使用你现有的 pi 配置。',
      platformNote: '目前已在 macOS 运行。Linux 和 Windows 提供构建包，但尚未进行运行验证。macOS 下载使用临时签名，Windows 构建尚未签名。',
      scopeNote: 'PR 跟踪尚未实现。', downloadsTitle: '下载', releaseFallback: '在 GitHub 最新发布中选择适合的安装包。', releaseAvailable: '最新发布',
      allReleases: '最新发布与全部安装包', buildSource: '从源码构建', allProjects: '全部项目', reportIssue: '反馈问题',
    },
  });
  let version;
  const status = document.querySelector('#release-status');
  const renderStatus = () => {
    status.textContent = version ? `${site.t('releaseAvailable')} · ${version}` : site.t('releaseFallback');
  };
  document.addEventListener('idy:languagechange', renderStatus);
  const list = document.querySelector('#download-list');
  const packages = [
    ['macOS · Apple Silicon', /^yesman-[\d.]+-arm64\.dmg$/],
    ['macOS · Intel', /^yesman-[\d.]+-amd64\.dmg$/],
    ['Windows · x64', /^yesman-setup-[\d.]+-amd64\.exe$/],
    ['Windows · ARM64', /^yesman-setup-[\d.]+-arm64\.exe$/],
    ['Linux · x64', /^yesman_[\d.]+_amd64\.deb$/],
    ['Linux · ARM64', /^yesman_[\d.]+_arm64\.deb$/],
  ];
  site.loadRelease('idy/yesman').then(release => {
    version = release.tag_name;
    renderStatus();
    for (const [label, pattern] of packages) {
      const asset = site.asset(release, pattern);
      if (!asset) continue;
      const item = document.createElement('li');
      const link = document.createElement('a');
      link.href = asset.browser_download_url;
      const name = document.createElement('span');
      name.textContent = label;
      const format = document.createElement('span');
      format.textContent = asset.name.slice(asset.name.lastIndexOf('.'));
      link.append(name, format);
      item.append(link);
      list.append(item);
    }
    list.hidden = !list.children.length;
  }).catch(renderStatus);
})();
