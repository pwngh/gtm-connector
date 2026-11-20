import assert from 'node:assert/strict';

export class ChromeHooks {
  constructor(path = '/renewals') {
    this.slots = []; this.entries = [path]; this.index = 0; this.nextKey = 1;
    this.metadata = [{key: 'initial', state: null}];
    this.navigate = (path, options = {}) => {
      if (typeof path === 'number') {this.index += path; return;}
      const metadata = {key: String(this.nextKey++), state: options.state || null};
      if (options.replace) {this.entries[this.index] = path; this.metadata[this.index] = metadata;}
      else {this.entries = this.entries.slice(0, this.index + 1); this.metadata = this.metadata.slice(0, this.index + 1); this.entries.push(path); this.metadata.push(metadata); this.index++;}
    };
  }
  slot(kind, create) {const index = this.cursor++; this.slots[index] ||= {kind, ...create()}; assert.equal(this.slots[index].kind, kind); return this.slots[index];}
  useState(initial) {
    const slot = this.slot('state', () => ({value: typeof initial === 'function' ? initial() : initial}));
    slot.set ||= value => {slot.value = typeof value === 'function' ? value(slot.value) : value;};
    return [slot.value, slot.set];
  }
  useReducer(reducer, value, init) {const slot = this.slot('reducer', () => ({value: init ? init(value) : value})); return [slot.value, action => {slot.value = reducer(slot.value, action);}];}
  useRef(value) {return this.slot('ref', () => ({value: {current: value}})).value;}
  useMemo(create, deps) {const slot = this.slot('memo', () => ({})); if (!slot.deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {slot.value = create(); slot.deps = deps;} return slot.value;}
  useId() {return this.slot('id', () => ({value: 'chrome-' + this.cursor})).value;}
  effect(kind, callback, deps) {const slot = this.slot(kind, () => ({})); if (!slot.deps || !deps || deps.some((value, index) => !Object.is(value, slot.deps[index]))) {slot.deps = deps; this.pending[kind].push(() => {slot.cleanup?.(); slot.cleanup = callback();});}}
  useEffect(callback, deps) {this.effect('effect', callback, deps);}
  useLayoutEffect(callback, deps) {this.effect('layout', callback, deps);}
  useLocation() {const url = new URL(this.entries[this.index], 'http://local.test'); return {pathname: url.pathname, search: url.search, hash: url.hash, ...this.metadata[this.index]};}
  useNavigate() {return this.navigate;}
  render(Component, props) {this.cursor = 0; this.pending = {layout: [], effect: []}; globalThis.__chromeHooks = this; return Component(props);}
  flush() {for (const kind of ['layout', 'effect']) for (const callback of this.pending[kind].splice(0)) callback();}
  dispose() {this.slots.forEach(slot => slot.cleanup?.());}
}

export function* nodes(node) {
  if (Array.isArray(node)) for (const child of node) yield* nodes(child);
  else if (node?.props) {yield node; yield* nodes(node.props.children);}
}
export const find = (tree, predicate) => [...nodes(tree)].find(predicate);
export const named = (tree, name) => find(tree, node => node.type?.name === name);
export const byClass = (tree, name) => find(tree, node => typeof node.props.className === 'string' && node.props.className.split(' ').includes(name));
export const keyboard = (key, props = {}) => Object.assign(new Event('keydown', {cancelable: true}), {key, ...props});

export function element(key) {
  return {key, tabIndex: 0, isConnected: true, visible: true, inert: false, calls: [],
    getClientRects() {return this.visible ? [{}] : [];},
    closest(selector) {return this.inert && selector.includes('[inert]') ? this : null;},
    focus(options) {if (!this.inert && this.visible) {this.calls.push(['focus', options]); document.activeElement = this; document.dispatchEvent?.(new Event('focusin'));}},
    scrollIntoView(options) {this.calls.push(['scroll', options]);},
  };
}

export function chromeDOM(media) {
  const elements = new Map(), frames = new Map(), storage = new Map(), targets = new Map();
  const get = key => {if (!elements.has(key)) elements.set(key, element(key)); return elements.get(key);};
  const dom = {elements, frames, storage, targets, get, navigation: [], dialog: null, mainHeading: get('heading')};
  globalThis.window = Object.assign(new EventTarget(), {location: new URL('http://local.test'), matchMedia: () => media, scrollTo() {}});
  globalThis.document = Object.assign(new EventTarget(), {
    documentElement: {dataset: {}}, body: {style: {overflow: ''}}, activeElement: null,
    getElementById: id => targets.get(id) || (id === 'workspace-navigation' ? get('navigation') : id === 'main' ? get('main') : null),
    querySelector: selector => selector === '.mobile-nav-close' ? get('close') : selector === 'main h1' ? dom.mainHeading : selector.includes('dialog') ? dom.dialog : null,
    querySelectorAll: () => dom.navigation,
  });
  globalThis.localStorage = {getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value)};
  globalThis.requestAnimationFrame = callback => {const key = {}; frames.set(key, callback); return key;};
  globalThis.cancelAnimationFrame = key => frames.delete(key);
  globalThis.getComputedStyle = node => ({visibility: node.visible ? 'visible' : 'hidden'});
  dom.flushFrames = () => {for (const [key, callback] of [...frames]) {frames.delete(key); callback();}};
  dom.attach = (tree, path) => {
    const aside = find(tree, node => node.props.id === 'workspace-navigation');
    const mainShell = byClass(tree, 'main-shell');
    const menu = byClass(tree, 'mobile-menu');
    menu.props.ref.current = get('menu');
    for (const key of ['menu', 'theme', 'search', 'profile', 'main', 'heading']) get(key).inert = mainShell.props.inert;
    get('menu').visible = media.matches;
    get('main').tabIndex = find(tree, node => node.props.id === 'main').props.tabIndex;
    dom.navigation = [...nodes(aside)].filter(node => node.type === 'button' || node.props.to).map(node => {
      const key = node.props.to ? (node.props.className === 'brand' ? 'brand' : node.props.to)
        : node.props.className.includes('mobile-nav-close') ? 'close' : node.props.className;
      const target = get(key); target.inert = aside.props.inert;
      target.visible = !aside.props['aria-hidden'] && (key !== 'close' || media.matches);
      target.to = node.props.to;
      return target;
    });
    get('navigation').contains = node => dom.navigation.includes(node);
    get('navigation').querySelector = () => dom.navigation.find(node => node.to && path.startsWith(node.to) && node.key !== 'brand');
    get('skip').inert = byClass(tree, 'skip').props.inert;
    dom.dialog = named(tree, 'WorkspaceDialog') ? get('dialog') : null;
  };
  return dom;
}
