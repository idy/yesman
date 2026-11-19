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

export function messageActions({ text, loadText, copy, done, fail, recall, dismiss, busy = false, restoring = false, recovering = false }) {
  const available = typeof text === 'string' || typeof loadText === 'function';
  const hasText = !!loadText || !!text?.length;
  const label = !available ? t('Full message text is unavailable') : hasText ? t('Copy message text') : t('This message has no text to copy');
  let pending = false;
  const button = h('button', {
    class: 'message-copy', type: 'button', title: label, 'aria-label': label,
    disabled: !available || !hasText,
    onclick: async () => {
      if (pending || !available || !hasText) return;
      pending = true;
      button.setAttribute('aria-busy', 'true');
      try {
        const value = loadText ? await loadText() : text;
        if (typeof value !== 'string') throw new Error(t('Full message text is unavailable'));
        await copy(value);
        done();
      } catch (error) {
        fail(error);
      } finally {
        pending = false;
        button.removeAttribute('aria-busy');
      }
    },
  }, icon('copy'));
  const recallLabel = t(recovering ? 'Recover draft' : restoring ? 'Return recalled message to input' : 'Cancel and edit');
  const recallButton = recall ? h('button', {
    class: 'message-copy message-recall', type: 'button', title: recallLabel, 'aria-label': recallLabel,
    onclick: recall, disabled: busy, 'aria-busy': busy ? 'true' : null,
  }, icon('x')) : null;
  // Only stops showing a submission nothing waits on; it is never sent again.
  const dismissLabel = t('Hide');
  const dismissButton = dismiss ? h('button', {
    class: 'message-dismiss', type: 'button', title: t('Stop showing this. Nothing is sent again.'), 'aria-label': dismissLabel,
    onclick: dismiss,
  }, icon('check'), h('span', null, dismissLabel)) : null;
  return h('div', { class: 'message-actions' }, button, recallButton, dismissButton);
}
