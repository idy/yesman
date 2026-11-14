// Message actions read canonical text, never rendered Markdown or receipt previews.
import { h, icon } from './dom.js';
import { t } from './i18n.js';

export const messageText = (content) => typeof content === 'string' ? content :
  (Array.isArray(content) ? content : []).filter((block) => block?.type === 'text' && typeof block.text === 'string').map((block) => block.text).join('\n');

export function receiptText(receipt) {
  // A preview may end halfway through the original input. Copying it as the
  // message would silently lose content. Native transcript text stays copyable.
  return !receipt.preview && typeof receipt.text === 'string' ? receipt.text : null;
}

export function messageActions({ text, copy, done, fail }) {
  const available = typeof text === 'string';
  const label = !available ? t('Full message text is unavailable') : text.length ? t('Copy message text') : t('This message has no text to copy');
  let pending = false;
  const button = h('button', {
    class: 'message-copy', type: 'button', title: label, 'aria-label': label,
    disabled: !available || !text.length,
    onclick: async () => {
      if (pending || !available || !text.length) return;
      pending = true;
      button.setAttribute('aria-busy', 'true');
      try {
        await copy(text);
        done();
      } catch (error) {
        fail(error);
      } finally {
        pending = false;
        button.removeAttribute('aria-busy');
      }
    },
  }, icon('copy'));
  return h('div', { class: 'message-actions' }, button);
}
