// Appended only to a copy of the exact package's index.html via YESMAN_WEB_DIR.
// Uses the real native MyGo bridge. No bridge replacement, input mutation or
// devtools/automation/credential/Grant API. The page's normal reads stay intact.
(() => {
  const calls = [], errors = [];
  window.addEventListener('error', (e) => errors.push(e.message));
  window.addEventListener('unhandledrejection', (e) => errors.push(String(e.reason)));
  const read = (method) => {
    if (!['State', 'Settings', 'About', 'Update', 'Crew'].includes(method)) throw Error('Unexpected observer method');
    const wire = `Yesman.${method}`;
    calls.push(wire);
    return window.mygo.call(wire);
  };
  const until = async (fn) => {
    const end = Date.now() + 30000;
    while (!fn()) {
      if (Date.now() > end) throw Error('Models observer timed out');
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  };
  const send = (value) => fetch(SCORE_ENDPOINT, {
    method: 'POST', headers: { 'Content-Type': 'text/plain' }, body: JSON.stringify(value),
  });
  (async () => {
    await until(() => window.mygo && document.querySelector('#settings-button'));
    document.querySelector('#settings-button').click();
    await until(() => document.querySelector('#settings-nav [data-page="yesman"]'));
    document.querySelector('#settings-nav [data-page="yesman"]').click();
    await until(() => document.querySelectorAll('.crew-row').length === 2);
    const state = await read('State');
    const settings = await read('Settings');
    const about = await read('About');
    const update = await read('Update');
    const crew = await read('Crew');
    const rows = [...document.querySelectorAll('.crew-row')].map((row) => ({
      key: row.querySelector('.card-row-text strong').title,
      text: row.innerText,
      metrics: [...row.querySelectorAll('.crew-metrics dd')].map((cell) => cell.innerText),
    }));
    await send({ ok: true, state, settings, about, update, crew, rows, calls, errors,
      userAgent: navigator.userAgent, visibility: document.visibilityState, url: location.href });
  })().catch(async (error) => { await send({ ok: false, error: String(error), calls, errors }); });
})();
