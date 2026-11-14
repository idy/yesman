// Composer-local recall. The server is the authority for native membership;
// no message text, queue count or history entry is used to infer eligibility.
import { IMAGE_TYPES, MAX_IMAGES, MAX_IMAGE_BYTES, MAX_TOTAL_BYTES } from './images.js';

export function supportsRecall(capability) { return capability === 1 || capability === 2 || capability === 3; }
export function supportsPromotion(capability) { return capability === 2 || capability === 3; }

export function restoredImages(images) {
  if (!Array.isArray(images) || images.length > MAX_IMAGES) throw new Error('The original image payload is unavailable');
  let total = 0;
  return images.map((image, index) => {
    if (image?.type !== 'image' || !IMAGE_TYPES.includes(image.mimeType) || typeof image.data !== 'string' || !image.data.length)
      throw new Error('The original image payload is unavailable');
    // Validate without re-encoding, resizing or replacing original bytes.
    const size = atob(image.data).length;
    total += size;
    if (size > MAX_IMAGE_BYTES || total > MAX_TOTAL_BYTES) throw new Error('The original image payload is unavailable');
    return { type: image.type, mimeType: image.mimeType, data: image.data, size, name: `Image ${index + 1}` };
  });
}

export function mayRecallInComposer(textarea, attachments) {
  if (!textarea || !attachments) return false;
  if (textarea.value !== '' || textarea.selectionStart !== 0 || textarea.selectionEnd !== 0 || textarea.readOnly || attachments.items.length || attachments.reading || attachments.sending || attachments.recalling) return false;
  // Native composer only. Editors and overlay consumers keep their own keys.
  for (const el of document.querySelectorAll('dialog[open], [role="dialog"], [role="menu"], [role="listbox"], [data-shortcut-recording="true"]')) {
    if (el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden') return false;
  }
  return true;
}

// Kept for standalone/native composer callers; the desktop registers its
// context action with shortcuts.js so overrides and recording have one owner.
export function mayRecallWithArrow(event, textarea, attachments, composing = false) {
  if (event.defaultPrevented || event.key !== 'ArrowUp' || event.repeat || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.isComposing || event.keyCode === 229 || composing) return false;
  return mayRecallInComposer(textarea, attachments);
}
