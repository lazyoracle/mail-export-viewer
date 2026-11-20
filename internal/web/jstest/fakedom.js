// A minimal DOM stand-in for rendering tests under node (no jsdom, no
// network). It models only what dom.js, list.js and reader.js use, and it
// deliberately has no HTML parser: innerHTML does not exist here.

class FakeNode {
  constructor() { this.childNodes = []; this.parentNode = null; }
  append(...nodes) {
    for (const n of nodes) {
      const node = typeof n === 'string' ? new FakeText(n) : n;
      if (node instanceof FakeFragment) { for (const c of [...node.childNodes]) this.append(c); node.childNodes = []; continue; }
      node.parentNode = this;
      this.childNodes.push(node);
    }
  }
  replaceChildren(...nodes) { this.childNodes = []; this.append(...nodes); }
  get children() { return this.childNodes.filter((c) => c instanceof FakeElement); }
  get textContent() { return this.childNodes.map((c) => c.textContent).join(''); }
  set textContent(v) { this.childNodes = []; if (v !== '') this.append(new FakeText(String(v))); }
}

class FakeText extends FakeNode {
  constructor(data) { super(); this.data = data; }
  get textContent() { return this.data; }
  set textContent(v) { this.data = String(v); }
}

class FakeFragment extends FakeNode {}

class FakeClassList {
  constructor(el) { this.el = el; }
  get list() { return (this.el.getAttribute('class') || '').split(/\s+/).filter(Boolean); }
  contains(c) { return this.list.includes(c); }
  add(...cs) { this.el.setAttribute('class', [...new Set([...this.list, ...cs])].join(' ')); }
  remove(...cs) { this.el.setAttribute('class', this.list.filter((c) => !cs.includes(c)).join(' ')); }
  toggle(c, on = !this.contains(c)) { if (on) this.add(c); else this.remove(c); return on; }
}

class FakeElement extends FakeNode {
  constructor(tag, ns = null) {
    super();
    this.tagName = tag.toUpperCase();
    this.namespaceURI = ns;
    this.attributes = new Map();
    this.dataset = {};
    this.listeners = {};
    this.classList = new FakeClassList(this);
    const props = new Map();
    this.style = { props, setProperty: (k, v) => props.set(k, v) };
  }
  setAttribute(k, v) { this.attributes.set(k, String(v)); }
  getAttribute(k) { return this.attributes.has(k) ? this.attributes.get(k) : null; }
  hasAttribute(k) { return this.attributes.has(k); }
  removeAttribute(k) { this.attributes.delete(k); }
  get className() { return this.getAttribute('class') || ''; }
  set className(v) { this.setAttribute('class', v); }
  get id() { return this.getAttribute('id') || ''; }
  set id(v) { this.setAttribute('id', v); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  dispatch(type, ev = {}) { for (const fn of this.listeners[type] || []) fn({ preventDefault() {}, target: this, ...ev }); }
  /** All descendant elements. */
  descendants() {
    const out = [];
    const walk = (n) => { for (const c of n.children) { out.push(c); walk(c); } };
    walk(this);
    return out;
  }
  // Supports "tag", ".class", "tag.class" and "[attr=value]" suffixes.
  matches(sel) {
    const m = /^([a-z0-9]*)((?:\.[\w-]+)*)((?:\[[\w-]+(?:="[^"]*")?\])*)$/i.exec(sel.replace(/:not\(\[disabled\]\)/, ''));
    if (!m) throw new Error(`fakedom: unsupported selector ${sel}`);
    const tag = m[1].toUpperCase();
    const classes = m[2].split('.').filter(Boolean);
    const attrs = [...m[3].matchAll(/\[([\w-]+)(?:="([^"]*)")?\]/g)];
    const notDisabled = sel.includes(':not([disabled])');
    return (!tag || this.tagName === tag) &&
      classes.every((c) => this.classList.contains(c)) &&
      attrs.every(([, k, v]) => (v === undefined ? this.hasAttribute(k) : this.getAttribute(k) === v)) &&
      (!notDisabled || !this.disabled);
  }
  querySelectorAll(sel) { return this.descendants().filter((e) => e.matches(sel)); }
  closest(sel) {
    for (let n = this; n instanceof FakeElement; n = n.parentNode) if (n.matches(sel)) return n;
    return null;
  }
  contains(n) { for (; n; n = n.parentNode) if (n === this) return true; return false; }
  focus() { globalThis.document.activeElement = this; }
  blur() { if (globalThis.document.activeElement === this) globalThis.document.activeElement = globalThis.document.body; }
  remove() { if (this.parentNode) { this.parentNode.childNodes = this.parentNode.childNodes.filter((c) => c !== this); this.parentNode = null; } }
  scrollIntoView() {}
  getBoundingClientRect() { return { top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0 }; }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}

export function installFakeDOM() {
  const body = new FakeElement('body');
  const documentElement = new FakeElement('html');
  documentElement.append(body);
  const document = {
    body,
    documentElement,
    title: '',
    activeElement: null,
    createElement: (t) => new FakeElement(t),
    createElementNS: (ns, t) => new FakeElement(t, ns),
    createTextNode: (d) => new FakeText(d),
    createDocumentFragment: () => new FakeFragment(),
    getElementById: (id) => documentElement.descendants().find((e) => e.id === id) || null,
  };
  globalThis.document = document;
  globalThis.Node = FakeNode;
  return document;
}
