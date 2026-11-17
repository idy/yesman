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

export function messageActions({ text, loadText, copy, done, fail, recall, promote, busy = false, restoring = false }) {
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
  const recallLabel = restoring ? t('Return recalled message to input') : t('Cancel and edit');
  const recallButton = recall ? h('button', {
    class: 'message-copy message-recall', type: 'button', title: recallLabel, 'aria-label': recallLabel,
    onclick: recall, disabled: busy, 'aria-busy': busy ? 'true' : null,
  }, icon('x')) : null;
  const promoteLabel = t('Send now');
  const promoteButton = promote ? h('button', {
    class: 'message-copy message-promote', type: 'button', title: t('Move to steering for the next input checkpoint'), 'aria-label': promoteLabel,
    onclick: promote, disabled: busy, 'aria-busy': busy ? 'true' : null,
  }, icon('send'), h('span', null, promoteLabel)) : null;
  return h('div', { class: 'message-actions' }, button, promoteButton, recallButton);
}
