// Shared DevTools-protocol plumbing for the headless-browser scripts
// (Node 22 built-ins only, no npm packages): launch starts Chromium with a
// profile below out, opens a page and returns helpers.
import { spawn } from 'node:child_process';
import { writeFileSync, mkdirSync, readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export async function launch(chrome, base, out) {
  mkdirSync(out, { recursive: true });
  const profile = join(out, 'profile');
  const proc = spawn(chrome, ['--headless', '--no-sandbox', '--remote-debugging-port=0', '--disable-gpu',
    `--user-data-dir=${profile}`, 'about:blank'], { stdio: 'ignore' });
  let PORT = 0;
  for (let i = 0; i < 100 && !PORT; i++) {
    const f = join(profile, 'DevToolsActivePort');
    if (existsSync(f)) PORT = Number(readFileSync(f, 'utf8').split('\n')[0]);
    else await sleep(100);
  }
  if (!PORT) { proc.kill(); throw new Error('browser did not start'); }
  for (let i = 0; i < 50; i++) {
    try { await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json(); break; } catch { await sleep(100); }
  }
  const tgt = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json();
  const ws = new WebSocket(tgt.webSocketDebuggerUrl);
  await new Promise((r) => ws.addEventListener('open', r));
  let nextId = 1;
  const pending = new Map();
  const problems = [];
  const requests = [];
  ws.addEventListener('message', (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result); return; }
    if (m.method === 'Runtime.exceptionThrown') problems.push('exception: ' + JSON.stringify(m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
    if (m.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(m.params.type)) problems.push('console: ' + m.params.args.map((a) => a.value || a.description).join(' '));
    if (m.method === 'Log.entryAdded' && m.params.entry.level !== 'verbose') problems.push('log: ' + m.params.entry.text + ' ' + (m.params.entry.url || ''));
    if (m.method === 'Network.requestWillBeSent') requests.push(m.params.request.url);
  });
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = nextId++;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
  await send('Runtime.enable');
  await send('Log.enable');
  await send('Network.enable');
  await send('Page.enable');
  const b = {
    send, problems, requests, sleep,
    async viewport(width, height, mobile = false) {
      await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile });
    },
    async scheme(dark) {
      await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: dark ? 'dark' : 'light' }] });
    },
    async go(hash, wait = 900) {
      await send('Page.navigate', { url: base + hash });
      await sleep(wait);
    },
    async evaluate(expr) {
      const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error('eval: ' + JSON.stringify(r.exceptionDetails));
      return r.result.value;
    },
    async shot(name) {
      await sleep(350);
      const r = await send('Page.captureScreenshot', { format: 'png' });
      writeFileSync(`${out}/${name}.png`, Buffer.from(r.data, 'base64'));
      console.log('shot', name);
    },
    async key(k, code = k, text = k.length === 1 ? k : undefined) {
      await send('Input.dispatchKeyEvent', { type: 'keyDown', key: k, code, text, windowsVirtualKeyCode: k === 'Enter' ? 13 : k === 'Escape' ? 27 : k === 'ArrowDown' ? 40 : 0 });
      await send('Input.dispatchKeyEvent', { type: 'keyUp', key: k, code });
      await sleep(150);
    },
    async type(s) {
      for (const ch of s) { await send('Input.insertText', { text: ch }); await sleep(30); }
    },
    close() { ws.close(); proc.kill(); },
  };
  return b;
}
