// Runs before the page first paints, unlike the deferred app.js: a sidebar
// the user hid stays hidden from the first frame.
document.documentElement.dataset.sidebar = localStorage.getItem('yesman.sidebar') === 'hidden' ? 'hidden' : 'shown';
