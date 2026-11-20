import test from 'node:test';
import assert from 'node:assert/strict';
import {closeReadinessOnEscape, focusReadinessElement, moveReadinessFocus, readinessFocusTarget, restoreReadinessFocus} from './readinessBrowser.js';

function element(key) {
  return {dataset: {readinessFocus: key, readinessTarget: key}, calls: [],
    focus(options) {this.calls.push(['focus', options]);}, scrollIntoView(options) {this.calls.push(['scroll', options]);}};
}
function keyboard(key, target, extra = {}) {
  return {key, target, prevented: false, stopped: false, preventDefault() {this.prevented = true;}, stopPropagation() {this.stopped = true;}, ...extra};
}

test('readiness keyboard navigation follows the rendered order, clamps at edges, and supports Home and End', () => {
  const entries = ['third', 'first', 'second'].map(element), root = {querySelectorAll: () => entries};
  for (const [key, index, next] of [['ArrowDown', 0, 1], ['ArrowUp', 2, 1], ['ArrowUp', 0, 0], ['ArrowDown', 2, 2], ['Home', 2, 0], ['End', 0, 2]]) {
    entries.forEach(item => {item.calls = [];});
    const event = keyboard(key, entries[index]);
    moveReadinessFocus(event, root);
    assert.equal(event.prevented, true);
    assert.deepEqual(entries[next].calls, [['focus', {preventScroll: true}], ['scroll', {block: 'nearest', behavior: 'instant'}]]);
    assert.ok(entries.filter((_, i) => i !== next).every(item => item.calls.length === 0));
  }
});

test('readiness navigation leaves native Enter, Tab, modified keys, and nested controls alone', () => {
  const entries = [element('first'), element('second')], root = {querySelectorAll: () => entries};
  for (const event of [keyboard('Enter', entries[0]), keyboard('Tab', entries[0]), keyboard('ArrowDown', element('control')),
    ...['altKey', 'ctrlKey', 'metaKey', 'shiftKey', 'defaultPrevented'].map(modifier => keyboard('ArrowDown', entries[0], {[modifier]: true}))]) {
    moveReadinessFocus(event, root);
    assert.equal(event.prevented, false);
  }
  assert.ok(entries.every(item => item.calls.length === 0));
});

test('focus restoration chooses the exact opener, a surviving item, then the page heading', () => {
  const opener = element('citation'), row = element('row'), heading = element('heading');
  restoreReadinessFocus({querySelectorAll: () => [row, opener]}, 'citation', 'row', heading);
  assert.equal(opener.calls.length, 2);
  assert.equal(row.calls.length, 0);
  restoreReadinessFocus({querySelectorAll: () => [row]}, 'citation', 'row', heading);
  assert.equal(row.calls.length, 2);
  restoreReadinessFocus({querySelectorAll: () => []}, 'citation', 'row', heading);
  assert.equal(heading.calls.length, 2);
});

test('selection focus resolves exact data keys without constructing selectors from URL text', () => {
  const entries = [element('check:one'), element('check:two')], root = {querySelectorAll: () => entries};
  assert.equal(readinessFocusTarget(root, 'check:two'), entries[1]);
  assert.equal(readinessFocusTarget(root, 'check:missing["'), undefined);
  focusReadinessElement(undefined);
  assert.ok(entries.every(item => item.calls.length === 0));
});

test('Escape closes an inspection but does not hijack form controls or handled key events', () => {
  let closes = 0;
  const ordinary = keyboard('Escape', {closest: () => null});
  closeReadinessOnEscape(ordinary, () => closes++);
  assert.equal(closes, 1);
  assert.equal(ordinary.prevented, true);
  assert.equal(ordinary.stopped, true);
  for (const event of [keyboard('Escape', {closest: () => ({})}), keyboard('Enter'), keyboard('Escape', null, {defaultPrevented: true}), keyboard('Escape', null, {metaKey: true})]) {
    closeReadinessOnEscape(event, () => closes++);
    assert.equal(event.prevented, false);
  }
  assert.equal(closes, 1);
});

test('focus recovery rejects hidden, disconnected, and closed-disclosure targets before falling back', () => {
  for (const hidden of [
    {isConnected: false}, {getClientRects: () => []}, {disabled: true},
    {closest: selector => selector.startsWith('[hidden]') ? {} : null},
    {closest: selector => selector.startsWith('details') ? {querySelector: () => ({contains: () => false})} : null},
  ]) {
    const opener = Object.assign(element('opener'), hidden), fallback = Object.assign(element('fallback'), hidden), heading = element('heading');
    assert.equal(focusReadinessElement(opener), false);
    restoreReadinessFocus({querySelectorAll: () => [opener, fallback]}, 'opener', 'fallback', heading);
    assert.equal(opener.calls.length, 0);
    assert.equal(fallback.calls.length, 0);
    assert.equal(heading.calls[0][0], 'focus');
  }
  const summary = element('summary');
  summary.closest = selector => selector.startsWith('details') ? {querySelector: () => ({contains: item => item === summary})} : null;
  assert.equal(focusReadinessElement(summary), true);
});

test('keyboard navigation skips entries hidden by a disclosure or removed from the document', () => {
  const entries = [element('first'), Object.assign(element('hidden'), {getClientRects: () => []}), element('last')];
  const event = keyboard('ArrowDown', entries[0]);
  moveReadinessFocus(event, {querySelectorAll: () => entries});
  assert.equal(event.prevented, true);
  assert.equal(entries[1].calls.length, 0);
  assert.equal(entries[2].calls[0][0], 'focus');
});
