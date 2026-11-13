(() => {
  const site = window.IdySite.init({
    en: {
      tagline: 'the agent that says yes and gets things done.',
      workspaceNav: 'Workspace', installNav: 'Install', source: 'Source',
      intro: 'Give a project its own workspace, a lead to talk to, and workers to move it forward. yesman keeps the repositories, conversations, and shared notes together in a desktop tray app.',
      getYesman: 'Get yesman', readGuide: 'Read the guide', statusLabel: 'Status', prototype: 'Early prototype', runtimeLabel: 'Agent runtime', licenseLabel: 'License', platformLabel: 'Platforms',
      workspaceTitle: 'One project. Its own workspace.',
      workspaceIntro: 'Hire a yesman for a project, then talk to its lead. When the work splits up, the lead starts workers in the same workspace.',
      treeTitle: 'Example workspace', treeLabel: 'Example workspace directory layout', worktreeComment: '# worktree',
      treeNote: 'Repositories live in a shared pool. Each checkout gets a worktree and a branch inside the project workspace.',
      leadTitle: 'A lead you can talk to', leadBody: 'Discuss the goal in a dedicated conversation. The lead plans the work, keeps notes, and delegates tasks.',
      workersTitle: 'Workers for focused tasks', workersBody: 'The lead starts and messages workers. A worker’s final message returns to the lead when its run finishes.',
      knowledgeTitle: 'Knowledge that stays together', knowledgeBody: 'Agents read and write the same project notes. Their session logs let them resume when the app restarts.',
      filesTitle: 'Files beside the conversation', filesBody: 'Browse the workspace, edit code, read Markdown, and view images from the files pane.',
      installTitle: 'Install yesman', requirements: 'Install git and pi 1.5 or later, with a model configured. yesman uses your usual pi configuration.',
      platformNote: 'macOS has been run. Linux and Windows builds are available but have not been runtime-tested. macOS downloads are signed ad hoc; Windows builds are unsigned.',
      scopeNote: 'Pull-request watching and service-key management are not built yet.',
      downloadsTitle: 'Downloads', releaseFallback: 'Choose a package from the latest GitHub release.',
      releaseAvailable: 'Latest release', allReleases: 'Latest release and all packages', buildSource: 'Build from source', allProjects: 'All projects', reportIssue: 'Report an issue',
    },
    zh: {
      tagline: '说到做到，把事情办成。', workspaceNav: '工作空间', installNav: '安装', source: '源码',
      intro: '为项目配一个独立的工作空间、一个可以讨论目标的 lead，以及推动任务的 worker。yesman 是一款桌面托盘应用，把仓库、会话和共享笔记放在一起。',
      getYesman: '获取 yesman', readGuide: '阅读指南', statusLabel: '状态', prototype: '早期原型', runtimeLabel: '智能体运行时', licenseLabel: '许可证', platformLabel: '平台',
      workspaceTitle: '一个项目，一个工作空间。',
      workspaceIntro: '为项目雇一个 yesman，与它的 lead 交流。工作需要拆分时，lead 会在同一个工作空间中启动 worker。',
      treeTitle: '工作空间示例', treeLabel: '工作空间目录结构示例', worktreeComment: '# 工作树',
      treeNote: '仓库保存在共享仓库池中。每次检出，都会在项目工作空间内创建一个独立分支的工作树。',
      leadTitle: '随时沟通的 lead', leadBody: '在专属会话中讨论目标。lead 规划工作、记录笔记，并委派任务。',
      workersTitle: '专注任务的 worker', workersBody: 'lead 启动 worker 并向它发送消息。worker 完成本轮工作后，最终消息会返回给 lead。',
      knowledgeTitle: '留在项目里的知识', knowledgeBody: '智能体共同读写项目笔记。应用重启后，它们可以从各自的会话记录继续工作。',
      filesTitle: '会话旁的项目文件', filesBody: '在文件面板浏览工作空间、编辑代码、阅读 Markdown，并查看图片。',
      installTitle: '安装 yesman', requirements: '先安装 git 和 pi 1.5 或更新版本，并配置好模型。yesman 使用你现有的 pi 配置。',
      platformNote: '目前已在 macOS 运行。Linux 和 Windows 提供构建包，但尚未进行运行验证。macOS 下载使用临时签名，Windows 构建尚未签名。',
      scopeNote: 'PR 跟踪与服务密钥管理尚未实现。', downloadsTitle: '下载', releaseFallback: '在 GitHub 最新发布中选择适合的安装包。', releaseAvailable: '最新发布',
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
