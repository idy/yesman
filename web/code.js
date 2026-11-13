// The language of a file, by its name, as the editor's languages call it
// (tools/codemirror/entry.js), or plaintext.

const BY_EXT = {
  go: 'go', js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript',
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  json: 'json', jsonc: 'json', webmanifest: 'json',
  md: 'markdown', markdown: 'markdown', mdx: 'markdown',
  py: 'python', pyi: 'python', rb: 'ruby', rs: 'rust', java: 'java', kt: 'kotlin', kts: 'kotlin', swift: 'swift',
  c: 'cpp', h: 'cpp', cc: 'cpp', cpp: 'cpp', cxx: 'cpp', hpp: 'cpp', hh: 'cpp', m: 'objectivec', mm: 'objectivec',
  cs: 'csharp', php: 'php', pl: 'perl', pm: 'perl', lua: 'lua', r: 'r', sql: 'sql',
  sh: 'shell', bash: 'shell', zsh: 'shell', fish: 'shell', ps1: 'powershell', psm1: 'powershell',
  yml: 'yaml', yaml: 'yaml', toml: 'toml', ini: 'ini', cfg: 'ini', conf: 'ini', properties: 'ini',
  html: 'html', htm: 'html', vue: 'html', svelte: 'html', xml: 'xml', svg: 'xml', plist: 'xml', xsd: 'xml',
  css: 'css', scss: 'css', less: 'css', diff: 'diff', patch: 'diff', proto: 'protobuf',
};
const BY_NAME = {
  dockerfile: 'dockerfile', containerfile: 'dockerfile', 'nginx.conf': 'nginx',
  '.bashrc': 'shell', '.zshrc': 'shell', '.profile': 'shell', '.env': 'shell', '.envrc': 'shell',
  '.gitignore': 'shell', '.dockerignore': 'shell', '.gitattributes': 'shell', '.editorconfig': 'ini',
  'go.mod': 'plaintext', 'go.sum': 'plaintext', license: 'plaintext',
};

export function languageOf(path) {
  const name = path.split('/').pop().toLowerCase();
  if (BY_NAME[name]) return BY_NAME[name];
  if (name.startsWith('dockerfile.') || name.endsWith('.dockerfile')) return 'dockerfile';
  if (name.startsWith('.env.')) return 'shell';
  const ext = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : '';
  return BY_EXT[ext] ?? 'plaintext';
}
