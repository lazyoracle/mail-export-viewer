// Timing helpers for type-ahead: a debouncer and a "latest request wins"
// guard that aborts superseded fetches and drops their late results.

/** fn runs once, ms after the last call, with that call's arguments. */
export function debounce(fn, ms) {
  let timer = null;
  let args = null;
  const run = () => {
    timer = null;
    const a = args;
    args = null;
    fn(...a);
  };
  const d = (...a) => {
    args = a;
    clearTimeout(timer);
    timer = setTimeout(run, ms);
  };
  d.cancel = () => { clearTimeout(timer); timer = null; args = null; };
  d.flush = () => { if (timer !== null) { clearTimeout(timer); run(); } };
  d.pending = () => timer !== null;
  return d;
}

/**
 * latest() tracks one in-flight request at a time. next() aborts the
 * previous one and returns {signal, current()}; run(task) calls
 * task(signal) and resolves {stale: false, value} or {stale: true} when a
 * newer request started meanwhile (errors of stale requests are swallowed).
 */
export function latest() {
  let gen = 0;
  let ctrl = null;
  const next = () => {
    if (ctrl) ctrl.abort();
    const mine = ++gen;
    const c = new AbortController();
    ctrl = c;
    return { signal: c.signal, current: () => mine === gen && !c.signal.aborted };
  };
  return {
    next,
    cancel() { if (ctrl) ctrl.abort(); ctrl = null; gen++; },
    async run(task) {
      const t = next();
      try {
        const value = await task(t.signal);
        return t.current() ? { stale: false, value } : { stale: true };
      } catch (e) {
        if (!t.current()) return { stale: true };
        throw e;
      }
    },
  };
}
