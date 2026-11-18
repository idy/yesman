// Small pieces of the submission contract shared by the conversation's
// composer (app.js) and a topic's composer (topics.js). See
// internal/agent/delivery.go: a receipt identifies a submission, never which
// native message pi took, and an unchanged retry reuses its ID.

export const DELIVERY_LABELS = {
  sending: 'Sending…', accepted: 'Accepted · delivery unconfirmed',
  steering: 'Accepted for steering · delivery unconfirmed', queued: 'Accepted for follow-up · delivery unconfirmed',
  unconfirmed: 'Delivery unconfirmed', failed: 'Not sent', interrupted: 'Delivery interrupted',
  handled: 'Handled by an extension', dispatched: 'Taken by pi', cancelled: 'Recalled',
};

// A receipt's label: a message still in pi's native queue says which queue
// it waits in rather than its admission, or that pi's idle preparation has
// claimed it (being sent, not yet taken).
export const deliveryLabel = (d) => d.nativePending ? (d.preparing ? 'Preparing · being sent to pi' : d.state === 'queued' ? 'Waiting · follow-up' : 'Steering · waiting for input checkpoint') : DELIVERY_LABELS[d.state];

// A 12-hex millisecond timestamp, then 36 random hex digits.
export const newSubmissionID = () => Date.now().toString(16).padStart(12, '0') +
  [...crypto.getRandomValues(new Uint8Array(18))].map((b) => b.toString(16).padStart(2, '0')).join('');

export const sameSubmissionImages = (left, right) => left.length === right.length && left.every((image, i) =>
  image.type === right[i].type && image.mimeType === right[i].mimeType && image.data === right[i].data);

// The visible line a message sent from a topic begins with
// (yesmen.TopicMarker). It names the topic for the lead and the reader; it
// is shown, never hidden from the model, and is never how a message is tied
// to a topic: the app's record of the submission is.
// withoutLine is a topic message's text without the line the app recorded
// sending before it; text that does not begin with that line is kept whole.
export function withoutLine(text, line) {
  if (!line || typeof text !== 'string' || !text.startsWith(line)) return text;
  const rest = text.slice(line.length);
  if (rest === '') return '';
  return rest.startsWith('\n\n') ? rest.slice(2) : text;
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
