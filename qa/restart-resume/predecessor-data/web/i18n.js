// The page's language. The page is written in English, and each message's
// English is its key: a locale's dictionary (web/locales/) maps it to that
// language, and a message it lacks stays English. The app chooses the
// language (MyGo's App.Locale): mygo.locale has it as the page starts, and
// the mygo:locale event tells the page when it changes.
//
//   t('Hire {name}', { name })                     a message, its {names} filled in
//   tn(n, '{n} repository', '{n} repositories')    a message by count, {n} the count
//   translatePage()                                index.html's data-i18n texts

import zhHans from './locales/zh-Hans.js';

const dictionaries = { 'zh-Hans': zhHans };

let current = 'en';
let dictionary = null;

// LANGUAGES are those the language menu offers, each named in itself.
export const LANGUAGES = [['en', 'English'], ['zh-Hans', '简体中文']];

// setLocale shows the page in a language the app is written in, English
// for any other.
export function setLocale(tag) {
  current = dictionaries[tag] ? tag : 'en';
  dictionary = dictionaries[current] ?? null;
  document.documentElement.lang = current;
}

export const locale = () => current;

const fill = (text, params) =>
  params ? text.replace(/\{(\w+)\}/g, (m, key) => (key in params ? String(params[key]) : m)) : text;

export function t(text, params) {
  return fill(dictionary?.[text] ?? text, params);
}

// tn picks one or other by n, as English does; a language without plural
// forms translates other and may leave one out.
export function tn(n, one, other, params) {
  const key = n === 1 ? one : other;
  const text = dictionary?.[key] ?? (n === 1 ? dictionary?.[other] : undefined) ?? key;
  return fill(text, { n: formatNumber(n), ...params });
}

export const formatNumber = (n, options) => new Intl.NumberFormat(current, options).format(n);

// tNodes is t for a message with elements in it: it returns the message's
// pieces, strings and the nodes params give, for h to append.
export function tNodes(text, params) {
  return (dictionary?.[text] ?? text).split(/(\{\w+\})/).filter(Boolean).map((piece) => {
    const key = /^\{(\w+)\}$/.exec(piece)?.[1];
    return key && key in params ? params[key] : piece;
  });
}

// translatePage translates the static text of index.html: data-i18n for an
// element's text, data-i18n-placeholder for its placeholder. The English
// is kept in the attribute the first time, so the page can change again.
export function translatePage(root = document) {
  for (const el of root.querySelectorAll('[data-i18n]')) {
    if (!el.dataset.i18n) el.dataset.i18n = el.textContent.trim();
    el.textContent = t(el.dataset.i18n);
  }
  for (const el of root.querySelectorAll('[data-i18n-placeholder]')) {
    if (!el.dataset.i18nPlaceholder) el.dataset.i18nPlaceholder = el.getAttribute('placeholder') ?? '';
    el.setAttribute('placeholder', t(el.dataset.i18nPlaceholder));
  }
}
