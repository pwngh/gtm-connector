import test from 'node:test';
import assert from 'node:assert/strict';
import {activateWorklistRow, focusWorklistElement, moveWorklistFocus, restoreWorklistFocus} from './worklistBrowser.js';

function element(key) {
  const calls = [];
  return {dataset: {worklistFocus: key}, calls, focus: options => calls.push(['focus', options]), scrollIntoView: options => calls.push(['scroll', options])};
}
const event = (key, target, extra = {}) => ({key, target, prevented: false, preventDefault() {this.prevented = true;}, ...extra});

test('entry keyboard navigation follows DOM order, clamps at edges and supports Home and End', () => {
  const entries = [element('first'), element('second'), element('third')];
  const root = {querySelectorAll: () => entries};
  for (const [key, from, to] of [['ArrowDown', 0, 1], ['ArrowUp', 2, 1], ['Home', 2, 0], ['End', 0, 2], ['ArrowUp', 0, 0], ['ArrowDown', 2, 2]]) {
    entries.forEach(entry => entry.calls.length = 0);
    const input = event(key, entries[from]);
    moveWorklistFocus(input, root);
    assert.equal(input.prevented, true);
    assert.equal(entries[to].calls[0][0], 'focus');
    assert.equal(entries[to].calls[1][1].block, 'nearest');
    assert.equal(entries[to].calls[1][1].behavior, 'instant');
    assert.equal(entries.flatMap(entry => entry.calls).length, 2);
  }
});

test('entry keyboard navigation leaves native Enter, Tab, modifiers and nested controls alone', () => {
  const entry = element('entry'), root = {querySelectorAll: () => [entry]};
  for (const input of [event('Enter', entry), event('Tab', entry), event('ArrowDown', entry, {ctrlKey: true}), event('ArrowDown', entry, {metaKey: true}), event('ArrowDown', entry, {shiftKey: true}), event('ArrowDown', {} )]) {
    moveWorklistFocus(input, root);
    assert.equal(input.prevented, false);
  }
  assert.equal(entry.calls.length, 0);
});

test('closing details restores the exact opener, then a surviving row, then the search field', () => {
  const opener = element('opener'), row = element('row'), search = element('search');
  let entries = [opener, row];
  const root = {querySelectorAll: () => entries};
  restoreWorklistFocus(root, 'opener', 'row', search);
  assert.equal(opener.calls.length, 2);
  entries = [row];
  restoreWorklistFocus(root, 'opener', 'row', search);
  assert.equal(row.calls.length, 2);
  entries = [];
  restoreWorklistFocus(root, 'opener', 'row', search);
  assert.equal(search.calls.length, 2);
  focusWorklistElement(null);
});

test('pointer row entry activates its primary exact-item link but never intercepts another control or modified click', () => {
  let clicks = 0;
  const currentTarget = {querySelector: selector => {assert.equal(selector, '[data-row-entry]'); return {click: () => clicks++};}};
  const input = {button: 0, currentTarget, target: {closest: () => null}};
  activateWorklistRow(input);
  assert.equal(clicks, 1);
  for (const extra of [{button: 1}, {ctrlKey: true}, {metaKey: true}, {altKey: true}, {shiftKey: true}, {defaultPrevented: true}, {target: {closest: () => ({})}}]) activateWorklistRow({...input, ...extra});
  assert.equal(clicks, 1);
});
