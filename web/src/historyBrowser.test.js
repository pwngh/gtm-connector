import test from 'node:test';
import assert from 'node:assert/strict';
import {closeHistoryOnEscape, focusHistoryElement, historyFocusTarget, moveHistoryFocus, restoreHistoryFocus} from './historyBrowser.js';

const element = key => ({dataset: {historyFocus: key, historyTarget: key}, calls: [],
  focus(options) {this.calls.push(['focus', options]);}, scrollIntoView(options) {this.calls.push(['scroll', options]);}});
const keyEvent = (key, target, extra = {}) => ({key, target, preventDefault() {this.prevented = true;}, stopPropagation() {this.stopped = true;}, ...extra});

test('history arrows, Home and End follow the rendered order and clamp at the edges', () => {
  const entries = ['last', 'first', 'middle'].map(element), root = {querySelectorAll: () => entries};
  for (const [key, index, next] of [['ArrowDown', 0, 1], ['ArrowUp', 2, 1], ['ArrowUp', 0, 0], ['ArrowDown', 2, 2], ['Home', 2, 0], ['End', 0, 2]]) {
    entries.forEach(item => {item.calls = [];});
    const event = keyEvent(key, entries[index]);
    moveHistoryFocus(event, root);
    assert.equal(event.prevented, true);
    assert.deepEqual(entries[next].calls, [['focus', {preventScroll: true}], ['scroll', {block: 'nearest', behavior: 'instant'}]]);
    assert.ok(entries.filter((_, i) => i !== next).every(item => !item.calls.length));
  }
});

test('history list navigation does not intercept Enter, Tab, modified arrows or form controls', () => {
  const entries = [element('first'), element('last')], root = {querySelectorAll: () => entries};
  for (const event of [keyEvent('Enter', entries[0]), keyEvent('Tab', entries[0]), keyEvent('ArrowDown', element('select')),
    ...['altKey', 'ctrlKey', 'metaKey', 'shiftKey', 'defaultPrevented'].map(key => keyEvent('ArrowDown', entries[0], {[key]: true}))]) {
    moveHistoryFocus(event, root);
    assert.equal(event.prevented, undefined);
  }
  assert.ok(entries.every(item => !item.calls.length));
});

test('history selection focuses exact keys rather than treating URL text as a CSS selector', () => {
  const entries = [element('entry:first'), element('entry:second')], root = {querySelectorAll: () => entries};
  assert.equal(historyFocusTarget(root, 'entry:second'), entries[1]);
  assert.equal(historyFocusTarget(root, 'entry:missing["'), undefined);
  assert.equal(focusHistoryElement(undefined), false);
});

test('history focus restoration chooses the surviving opener, then entry, then heading', () => {
  const opener = element('interval'), entry = element('entry'), heading = element('heading');
  restoreHistoryFocus({querySelectorAll: () => [entry, opener]}, 'interval', 'entry', heading);
  assert.equal(opener.calls.length, 2);
  assert.equal(entry.calls.length, 0);
  restoreHistoryFocus({querySelectorAll: () => [entry]}, 'interval', 'entry', heading);
  assert.equal(entry.calls.length, 2);
  restoreHistoryFocus({querySelectorAll: () => []}, 'interval', 'entry', heading);
  assert.equal(heading.calls.length, 2);
});

test('history focus skips hidden, removed, disabled and closed-disclosure elements', () => {
  for (const hidden of [{isConnected: false}, {disabled: true}, {getClientRects: () => []},
    {closest: selector => selector.startsWith('[hidden]') ? {} : null},
    {closest: selector => selector.startsWith('details') ? {querySelector: () => ({contains: () => false})} : null}]) {
    const first = element('first'), skipped = Object.assign(element('hidden'), hidden), last = element('last');
    const root = {querySelectorAll: () => [first, skipped, last]};
    moveHistoryFocus(keyEvent('ArrowDown', first), root);
    assert.equal(skipped.calls.length, 0);
    assert.equal(last.calls[0][0], 'focus');
    assert.equal(historyFocusTarget(root, 'hidden'), undefined);
    assert.equal(focusHistoryElement(skipped), false);
  }
});

test('Escape closes history selection but leaves native form and handled events alone', () => {
  let closes = 0;
  const event = keyEvent('Escape', {closest: () => null});
  closeHistoryOnEscape(event, () => closes++);
  assert.equal(event.prevented, true);
  assert.equal(event.stopped, true);
  for (const other of [keyEvent('Escape', {closest: () => ({})}), keyEvent('Enter'), keyEvent('Escape', null, {defaultPrevented: true}), keyEvent('Escape', null, {ctrlKey: true})]) {
    closeHistoryOnEscape(other, () => closes++);
    assert.equal(other.prevented, undefined);
  }
  assert.equal(closes, 1);
});


test('restoring a nested history opener reveals all closed ancestor disclosures before focus', () => {
  const target = element('nested'), heading = element('heading');
  const outer = {open: false, querySelector: () => ({contains: () => false})};
  const inner = {open: false, parentElement: {closest: () => outer}, querySelector: () => ({contains: () => false})};
  target.closest = selector => selector === 'details' ? inner : selector === 'details:not([open])' ? !inner.open ? inner : !outer.open ? outer : null : null;
  target.getClientRects = () => inner.open && outer.open ? [{}] : [];
  restoreHistoryFocus({querySelectorAll: () => [target]}, 'nested', null, heading);
  assert.equal(inner.open, true);
  assert.equal(outer.open, true);
  assert.deepEqual(target.calls, [['focus', {preventScroll: true}], ['scroll', {block: 'nearest', behavior: 'instant'}]]);
  assert.equal(heading.calls.length, 0);
});

test('restoring an unavailable nested opener falls back without opening an inert disclosure', () => {
  const target = element('nested'), fallback = element('entry'), disclosure = {open: false};
  target.closest = selector => selector.startsWith('[hidden]') ? {} : disclosure;
  restoreHistoryFocus({querySelectorAll: () => [target, fallback]}, 'nested', 'entry', element('heading'));
  assert.equal(disclosure.open, false);
  assert.equal(target.calls.length, 0);
  assert.equal(fallback.calls[0][0], 'focus');
});
