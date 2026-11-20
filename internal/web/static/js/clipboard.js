// Copy text to the clipboard: the async Clipboard API when the browser
// allows it, else select the text in an off-screen field and copy that.

export async function copyText(text, { nav = globalThis.navigator, doc = globalThis.document } = {}) {
  try {
    if (nav && nav.clipboard && nav.clipboard.writeText) {
      await nav.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Permission denied or insecure context: try the selection fallback.
  }
  const prev = doc.activeElement;
  const ta = doc.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.setAttribute('aria-hidden', 'true');
  ta.className = 'clip-buffer';
  doc.body.append(ta);
  let ok = false;
  try {
    ta.select();
    ok = doc.execCommand('copy') === true;
  } catch {
    ok = false;
  }
  ta.remove();
  if (prev && prev.focus) prev.focus();
  return ok;
}
