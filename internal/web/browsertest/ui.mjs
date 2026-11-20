// Headless-browser check of the UI: drives Chromium over the DevTools
// protocol (Node 22 built-ins only, no npm packages) against a running
// server with the demo archive, checks key behaviour and saves screenshots.
// Run it through the Go test, which starts the server:
//
//   MEV_CHROME=/path/to/chromium go test ./internal/web -run TestBrowser -v
//
// Environment: CHROME (browser binary), BASE (server URL ending in /),
// OUT (directory for screenshots and the browser profile; never inside
// the repository). Exits 1 when a check fails.
import { launch, sleep } from './cdp.mjs';

const { CHROME, BASE, OUT } = process.env;
if (!CHROME || !BASE || !OUT) {
  console.error('set CHROME, BASE and OUT');
  process.exit(2);
}
let b;
try {
  b = await launch(CHROME, BASE, OUT);
} catch (e) {
  console.error(e.message);
  process.exit(2);
}
const { send, problems, requests, viewport, scheme, go, evaluate, shot, key, type } = b;

const checks = [];
let failed = false;
const check = (name, ok, detail = '') => { if (!ok) failed = true; checks.push(`${ok ? 'PASS' : 'FAIL'} ${name} ${detail}`); };

try {
  await viewport(1440, 900);
  await scheme(false);
  await go('#/');
  await sleep(500);
  check('list has rows', await evaluate(`document.querySelectorAll('#message-list .row').length > 20`));
  check('sidebar sources', await evaluate(`document.querySelectorAll('.source').length`) >= 3, await evaluate(`[...document.querySelectorAll('.source-name')].map(e=>e.textContent).join('|')`));
  check('labels section', await evaluate(`!document.getElementById('terms-label').hidden`));
  check('categories section', await evaluate(`!document.getElementById('terms-category').hidden`));
  check('tags section', await evaluate(`!document.getElementById('terms-tag').hidden`), await evaluate(`[...document.querySelectorAll('#terms-tag .side-name')].map(e=>e.textContent).join('|')`));
  await shot('01-list-light');

  // keyboard: focus list, j, then open
  await evaluate(`document.getElementById('message-list').focus()`);
  await key('j'); await key('j'); await key('o');
  await sleep(700);
  check('j/o opens a message', await evaluate(`location.hash.startsWith('#/m/')`), await evaluate('location.hash'));
  await shot('02-message-light');

  // search newsletter and open it
  await go('#/?q=newsletter');
  await sleep(600);
  await evaluate(`document.querySelector('#message-list .row').click()`);
  await sleep(900);
  check('html iframe sandboxed', await evaluate(`document.querySelector('iframe.html-frame')?.getAttribute('sandbox')`) === 'allow-popups allow-popups-to-escape-sandbox');
  check('remote notice', await evaluate(`!!document.querySelector('.notice-remote')`));
  check('attachments', await evaluate(`document.querySelectorAll('.att').length`) >= 2);
  await shot('03-html-message');
  await evaluate(`document.getElementById('tab-props').click()`);
  await sleep(300);
  await shot('04-properties');
  await evaluate(`document.getElementById('tab-headers').click()`);
  await sleep(300);
  await shot('05-headers');

  // OLM message with categories
  await go('#/?q=subject%3AOutlook');
  await sleep(600);
  await evaluate(`document.querySelector('#message-list .row').click()`);
  await sleep(900);
  check('category pills', await evaluate(`document.querySelectorAll('.msg-tags .pill-category').length`) >= 1);
  await shot('06-olm-message');

  // conversation
  await go('#/?q=offsite');
  await sleep(500);
  await evaluate(`document.getElementById('thread-toggle').getAttribute('aria-pressed')==='false' && document.getElementById('thread-toggle').click()`);
  await sleep(500);
  await evaluate(`document.querySelector('#message-list .row').click()`);
  await sleep(900);
  check('conversation view', await evaluate(`document.querySelectorAll('.convo-item').length`) >= 2, String(await evaluate(`document.querySelectorAll('.convo-item').length`)));
  await shot('07-conversation');
  await evaluate(`document.getElementById('thread-toggle').click()`);

  // search suggestions
  await go('#/');
  await evaluate(`document.getElementById('search').focus()`);
  await type('tag:');
  await sleep(400);
  check('suggestions', await evaluate(`!document.getElementById('search-suggest').hidden`), await evaluate(`[...document.querySelectorAll('.suggest-insert')].map(e=>e.textContent).join('|')`));
  await shot('08-suggest');
  await key('ArrowDown'); await key('Enter');
  await sleep(800);
  check('chip after pick', await evaluate(`document.querySelectorAll('.query-chip').length`) >= 1, await evaluate(`document.getElementById('search').value`));
  await shot('09-chips');
  await evaluate(`document.getElementById('search-help-toggle').click()`);
  await sleep(300);
  await shot('10-help');
  await key('Escape');

  // flag filter + deleted only
  await go('#/');
  await evaluate(`document.querySelector('.flag-btn[data-flag="starred"]')?.click()`);
  await sleep(700);
  check('starred filter in q', (await evaluate(`document.getElementById('search').value`)).includes('is:starred'));
  await go('#/?deleted=only');
  await sleep(700);
  check('deleted only lists', await evaluate(`document.querySelectorAll('#message-list .row.is-deleted').length`) >= 1);
  await shot('11-deleted-only');

  // stats
  await go('#/stats');
  await sleep(900);
  check('stats bars', await evaluate(`document.querySelectorAll('.bar').length`) >= 3);
  await shot('12-stats-light');

  // dialogs
  await go('#/');
  await evaluate(`document.body.focus()`);
  await key('?', 'Slash');
  await sleep(300);
  check('help dialog open', await evaluate(`document.getElementById('shortcuts').open`));
  await shot('13-shortcuts');
  await key('Escape');
  await evaluate(`document.getElementById('status-open').click()`);
  await sleep(300);
  await shot('14-status');
  await key('Escape');

  // dark
  await scheme(true);
  await go('#/?q=newsletter');
  await sleep(500);
  await evaluate(`document.querySelector('#message-list .row').click()`);
  await sleep(900);
  await shot('15-message-dark');
  await go('#/stats');
  await sleep(800);
  await shot('16-stats-dark');
  await go('#/');
  await shot('17-list-dark');
  await scheme(false);

  // medium width, drawer
  await viewport(960, 800);
  await go('#/');
  await shot('18-medium');
  await evaluate(`document.getElementById('sidebar-open').click()`);
  await sleep(400);
  await shot('19-medium-drawer');
  await key('Escape');
  check('Esc closes drawer', await evaluate(`!document.getElementById('app').classList.contains('drawer-open')`));

  // phone
  await viewport(390, 844, true);
  await go('#/');
  await shot('20-phone-list');
  await evaluate(`document.querySelector('#message-list .row').click()`);
  await sleep(900);
  await shot('21-phone-message');
  await go('#/stats');
  await sleep(800);
  await shot('22-phone-stats');

  // stale deep link
  await viewport(1440, 900);
  await go('#/m/999999');
  await sleep(700);
  check('stale link explained', (await evaluate(`document.querySelector('.reader-state-title')?.textContent || ''`)).includes('no longer'));
  await shot('23-stale-link');

  const external = requests.filter((u) => !u.startsWith(BASE) && !u.startsWith('data:') && !u.startsWith('about:'));
  check('no external requests', external.length === 0, external.join(' '));
} catch (e) {
  console.log('ERROR', e.stack);
  failed = true;
}
console.log(checks.join('\n'));
// The stale-link check requests a missing message on purpose.
const real = [...new Set(problems)].filter((p) => !p.includes('/api/messages/999999'));
console.log('problems:\n' + real.join('\n'));
b.close();
process.exit(failed || real.length ? 1 : 0);
