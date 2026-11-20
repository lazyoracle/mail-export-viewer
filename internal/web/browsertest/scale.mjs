// Headless-browser check of the UI on a large archive: how long the list,
// sidebar, paging, search, a very long conversation and the statistics
// take, how big the DOM grows and which main-thread tasks block for over
// 50 ms (jank). It works on any running server (no fixture assumptions
// beyond a reply chain with the subject "Endless thread", which
// scripts/gencorpus -thread N generates):
//
//   CHROME=/path/to/chrome-headless-shell BASE=http://127.0.0.1:8080/ OUT=/tmp/ui \
//     node internal/web/browsertest/scale.mjs
//
// OUT receives screenshots and the browser profile (never inside the
// repository). Prints a JSON report; exits 1 on page errors.
import { launch, sleep } from './cdp.mjs';

const { CHROME, BASE, OUT } = process.env;
const WATCH = Number(process.env.WATCH || 0); // seconds to watch '#/' (e.g. while indexing)
if (!CHROME || !BASE || !OUT) {
  console.error('set CHROME, BASE and OUT');
  process.exit(2);
}
const b = await launch(CHROME, BASE, OUT);
const { send, evaluate, shot, viewport, go } = b;
const report = {};

await send('Page.addScriptToEvaluateOnNewDocument', {
  source: `window.__long = []; try { new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__long.push(Math.round(e.duration)); }).observe({ type: 'longtask', buffered: true }); } catch {}`,
});
const longTasks = async () => evaluate(`(() => { const l = window.__long || []; window.__long = []; return { count: l.length, max: Math.max(0, ...l), total: l.reduce((a, b) => a + b, 0) }; })()`);
const dom = async () => evaluate(`document.getElementsByTagName('*').length`);
async function waitFor(expr, ms = 30000) {
  const t = Date.now();
  while (Date.now() - t < ms) {
    if (await evaluate(expr)) return Date.now() - t;
    await sleep(25);
  }
  return -1;
}

try {
  await viewport(1440, 900);
  if (WATCH) {
    await go('#/', 200);
    const samples = [];
    for (let i = 0; i < WATCH; i++) {
      await sleep(1000);
      samples.push(await longTasks());
    }
    report.watch = { seconds: WATCH, longTasks: samples.filter((s) => s.count), dom: await dom() };
    await shot('scale-00-indexing');
  }
  await go('#/', 0);
  report.firstRowsMs = await waitFor(`document.querySelectorAll('#message-list .row').length > 0`);
  await sleep(1500);
  report.sidebar = await evaluate(`({ labels: document.querySelectorAll('#terms-label .side-term').length,
    categories: document.querySelectorAll('#terms-category .side-term').length,
    tags: document.querySelectorAll('#terms-tag .side-term').length,
    folders: document.querySelectorAll('#sidebar .tree-item, #sidebar .folder').length })`);
  report.initial = { rows: await evaluate(`document.querySelectorAll('#message-list .row').length`), dom: await dom(), longTasks: await longTasks() };
  await shot('scale-01-list');

  // Page through the list by scrolling to the bottom repeatedly.
  const t0 = Date.now();
  for (let i = 0; i < 40; i++) {
    await evaluate(`(() => { const s = document.getElementById('list-scroll'); s.scrollTop = s.scrollHeight; })()`);
    await sleep(120);
  }
  await sleep(800);
  report.scrolled = { ms: Date.now() - t0, rows: await evaluate(`document.querySelectorAll('#message-list .row').length`),
    dom: await dom(), longTasks: await longTasks() };
  await shot('scale-02-scrolled');

  // Filter the label list.
  if (report.sidebar.labels > 12) {
    await evaluate(`document.querySelector('#terms-label input')?.focus()`);
    const tf = Date.now();
    await send('Input.insertText', { text: '7' });
    await sleep(600);
    report.labelFilter = { ms: Date.now() - tf, shown: await evaluate(`document.querySelectorAll('#terms-label .side-term').length`), longTasks: await longTasks() };
    await shot('scale-03-label-filter');
  }

  // Search as you type.
  await go('#/', 1500);
  await longTasks();
  await evaluate(`document.getElementById('search').focus()`);
  const ts = Date.now();
  for (const ch of 'budg') { await send('Input.insertText', { text: ch }); await sleep(60); }
  await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
  await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter' });
  const tr = await waitFor(`location.hash.includes('budg') && document.querySelectorAll('#message-list .row').length > 0`);
  report.search = { ms: tr < 0 ? -1 : Date.now() - ts, rows: await evaluate(`document.querySelectorAll('#message-list .row').length`), longTasks: await longTasks() };
  await shot('scale-04-search');

  // The longest conversation.
  const l = await (await fetch(BASE + 'api/messages?q=' + encodeURIComponent('subject:"Endless thread"') + '&limit=1')).json();
  if (l.items && l.items.length) {
    const id = l.items[0].id;
    await go('#/', 1000);
    await longTasks();
    const tt = Date.now();
    await go('#/m/' + id, 0);
    await waitFor(`[...document.querySelectorAll('button')].some((b) => b.textContent.startsWith('Conversation'))`);
    await evaluate(`[...document.querySelectorAll('button')].find((b) => b.textContent.startsWith('Conversation')).click()`);
    let prev = -1;
    for (let i = 0; i < 40; i++) {
      const n = await evaluate(`document.querySelectorAll('.convo-item').length`);
      if (n === prev) break;
      prev = n;
      await sleep(250);
    }
    report.thread = { threadCount: l.items[0].threadCount, ms: Date.now() - tt, convoItems: prev, dom: await dom(), longTasks: await longTasks() };
    await shot('scale-05-thread');
  }

  // Statistics.
  const tst = Date.now();
  await go('#/stats', 0);
  await waitFor(`document.querySelectorAll('.bar').length > 0`);
  report.stats = { ms: Date.now() - tst, longTasks: await longTasks() };
  await shot('scale-06-stats');
} catch (e) {
  report.error = e.stack;
}
report.problems = [...new Set(b.problems)];
console.log(JSON.stringify(report, null, 2));
b.close();
process.exit(report.error || report.problems.length ? 1 : 0);
