// Small pieces of the submission contract shared by the conversation's
// composer (app.js) and a topic's composer (topics.js). See
// internal/agent/delivery.go: a receipt identifies a submission, never which
// native message pi took, and an unchanged retry reuses its ID.

export const DELIVERY_LABELS = {
  sending: 'Sending…', accepted: 'Accepted · delivery unconfirmed',
  steering: 'Accepted for steering · delivery unconfirmed', queued: 'Accepted for follow-up · delivery unconfirmed',
  unconfirmed: 'Delivery unconfirmed', failed: 'Not sent', interrupted: 'Delivery interrupted',
  handled: 'Handled by an extension',
};

// A 12-hex millisecond timestamp, then 36 random hex digits.
export const newSubmissionID = () => Date.now().toString(16).padStart(12, '0') +
  [...crypto.getRandomValues(new Uint8Array(18))].map((b) => b.toString(16).padStart(2, '0')).join('');

export const sameSubmissionImages = (left, right) => left.length === right.length && left.every((image, i) =>
  image.type === right[i].type && image.mimeType === right[i].mimeType && image.data === right[i].data);

// The visible line a message sent from a topic begins with
// (yesmen.TopicMarker). It names the topic for the lead and the reader; it
// is shown, never hidden, and is not how receipts are matched.
const MARKER = /^\[Topic (tp[1-9][0-9]{0,5}): ([^\n]*)\](?:\n\n|$)/;

export function parseTopicMarker(text) {
  const m = typeof text === 'string' ? MARKER.exec(text) : null;
  return m ? { id: m[1], title: m[2], rest: text.slice(m[0].length) } : null;
}

export function topicMarker(topic) {
  let title = (topic.title ?? '').split(/\s+/).filter(Boolean).join(' ');
  const chars = Array.from(title);
  if (chars.length > 120) title = chars.slice(0, 120).join('') + '…';
  return `[Topic ${topic.id}: ${title}]`;
}

// Text beginning with "/" is a pi command, skill or template: it is sent as
// typed, without the topic line (yesmen.TopicCommand).
export const isCommand = (text) => /^[ \t\n]*\//.test(text ?? '');
