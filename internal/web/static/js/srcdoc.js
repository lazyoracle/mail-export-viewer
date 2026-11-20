// Builds the srcdoc document for a sanitised HTML message body. The iframe
// is sandboxed without scripts or same-origin; this only adds a readable
// base style. The body always renders on a light "paper" ground because
// most HTML mail assumes one.

const BASE_STYLE = [
  'html{color-scheme:light;background:#fff}',
  'body{margin:0;padding:20px 24px 32px;color:#1c1f24;background:#fff;',
  'font:15px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;',
  'overflow-wrap:break-word;word-wrap:break-word}',
  'img{max-width:100%;height:auto}',
  'pre{white-space:pre-wrap}',
  'blockquote{margin:0 0 0 .4em;padding-left:1em;border-left:3px solid #d5d9e0;color:#4a505a}',
  'a{color:#1d5f8c}',
  'table{max-width:100%}',
].join('');

export function buildSrcdoc(html) {
  return '<!doctype html><html><head><meta charset="utf-8">' +
    '<meta name="color-scheme" content="light">' +
    `<style>${BASE_STYLE}</style></head><body>${html}</body></html>`;
}
