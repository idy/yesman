// A small Markdown renderer for agent replies: headings, paragraphs, lists,
// block quotes, fenced code, tables, rules, and inline code, emphasis and
// links. Text is escaped before any markup is added, so model output can
// never inject HTML.

const escapeHTML = (s) =>
  s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

function inline(text) {
  const codes = [];
  let s = escapeHTML(text).replace(/`([^`\n]+)`/g, (_, code) => {
    codes.push(code);
    return `\u0000${codes.length - 1}\u0000`;
  });
  s = s
    .replace(/\[([^\]\n]+)\]\(((?:https?:\/\/|mailto:)[^\s)]+)\)/g, '<a href="$2" data-external>$1</a>')
    .replace(/\[([^\]\n]+)\]\((?:file:\/\/)?((?:\/|~\/|[A-Za-z]:[\\/])[^\s)]*)\)/g, '<a href="#" data-path="$2" title="$2">$1</a>')
    .replace(/(^|[\s(])((?:https?:\/\/)[^\s<)]+[^\s<).,;:!?])/g, '$1<a href="$2" data-external>$2</a>')
    // In a document, a link to another file of it, relative to it.
    .replace(/\[([^\]\n]+)\]\(((?![a-z][a-z0-9+.-]*:|[\/~#])[^\s)]+)\)/gi, (m, label, href) => (relativeLinks ? `<a href="${href}">${label}</a>` : m))
    .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
    .replace(/__([^_\n]+)__/g, '<strong>$1</strong>')
    .replace(/(^|[^*\w])\*([^*\n]+)\*(?!\w)/g, '$1<em>$2</em>')
    .replace(/(^|[^_\w])_([^_\n]+)_(?!\w)/g, '$1<em>$2</em>')
    .replace(/~~([^~\n]+)~~/g, '<del>$1</del>');
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${codes[i]}</code>`);
}

const isTableRow = (line) => /^\s*\|.*\|\s*$/.test(line);
const isTableRule = (line) => /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(line);
const cells = (line) => line.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());

// relativeLinks says the text is a document of the workspace, whose links
// to other files of it are relative, rather than an agent's reply.
let relativeLinks = false;

// renderDocument renders a Markdown file of the workspace: as a reply, but
// with links to other files of it, which the files pane opens.
export function renderDocument(src) {
  relativeLinks = true;
  try {
    return renderMarkdown(src);
  } finally {
    relativeLinks = false;
  }
}

export function renderMarkdown(src) {
  const lines = (src ?? '').replace(/\r\n?/g, '\n').split('\n');
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];

    const fence = line.match(/^\s*(```+|~~~+)\s*([\w+-]*)/);
    if (fence) {
      const close = fence[1];
      const body = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(close)) body.push(lines[i++]);
      i++;
      const lang = fence[2] ? ` data-lang="${escapeHTML(fence[2])}"` : '';
      out.push(`<pre class="md-code"${lang}><code>${escapeHTML(body.join('\n'))}</code></pre>`);
      continue;
    }

    if (!line.trim()) {
      i++;
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      const level = Math.min(heading[1].length + 2, 6); // keep replies' headings small
      out.push(`<h${level}>${inline(heading[2])}</h${level}>`);
      i++;
      continue;
    }

    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      out.push('<hr>');
      i++;
      continue;
    }

    if (isTableRow(line) && i + 1 < lines.length && isTableRule(lines[i + 1])) {
      const head = cells(line);
      i += 2;
      const rows = [];
      while (i < lines.length && isTableRow(lines[i])) rows.push(cells(lines[i++]));
      out.push(
        `<div class="md-table"><table><thead><tr>${head.map((c) => `<th>${inline(c)}</th>`).join('')}</tr></thead>` +
          `<tbody>${rows.map((r) => `<tr>${r.map((c) => `<td>${inline(c)}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`,
      );
      continue;
    }

    if (/^\s*>/.test(line)) {
      const body = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) body.push(lines[i++].replace(/^\s*>\s?/, ''));
      out.push(`<blockquote>${renderMarkdown(body.join('\n'))}</blockquote>`);
      continue;
    }

    const item = line.match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
    if (item) {
      const ordered = /\d/.test(item[2]);
      const indent = item[1].length;
      const items = [];
      while (i < lines.length) {
        const m = lines[i].match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
        if (m && m[1].length <= indent + 1 && /\d/.test(m[2]) === ordered) {
          items.push([m[3]]);
          i++;
        } else if (items.length && lines[i].trim() && /^\s+/.test(lines[i])) {
          items[items.length - 1].push(lines[i].replace(new RegExp(`^\\s{0,${indent + 4}}`), ''));
          i++;
        } else break;
      }
      const tag = ordered ? 'ol' : 'ul';
      const start = ordered && parseInt(item[2], 10) !== 1 ? ` start="${parseInt(item[2], 10)}"` : '';
      out.push(
        `<${tag}${start}>${items
          .map((body) => {
            const [first, ...rest] = body;
            const task = first.match(/^\[([ xX])\]\s+(.*)$/);
            const head = task ? `<input type="checkbox" disabled${task[1] !== ' ' ? ' checked' : ''}> ${inline(task[2])}` : inline(first);
            return `<li>${head}${rest.length ? renderMarkdown(rest.join('\n')) : ''}</li>`;
          })
          .join('')}</${tag}>`,
      );
      continue;
    }

    const para = [];
    while (
      i < lines.length &&
      lines[i].trim() &&
      !/^\s*(```|~~~|#{1,6}\s|>|([-*+]|\d+[.)])\s)/.test(lines[i]) &&
      !(isTableRow(lines[i]) && i + 1 < lines.length && isTableRule(lines[i + 1]))
    ) {
      para.push(lines[i++]);
    }
    if (!para.length) para.push(lines[i++]); // an indented marker no block took
    out.push(`<p>${para.map(inline).join('<br>')}</p>`);
  }
  return out.join('');
}

export { escapeHTML };
