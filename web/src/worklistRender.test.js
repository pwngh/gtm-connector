import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {MotionConfig} from 'motion/react';
import {createServer} from 'vite';
import {initialState, transition} from './workflow.js';
import {renewalFixture, worklistFixture, fixturePeople} from './renewalFixtures.js';
import {buildWorklist, selectWorklist, worklistHref} from './worklist.js';

let server, Worklists, App;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  Worklists = (await server.ssrLoadModule('/src/Worklists.jsx')).default;
  App = (await server.ssrLoadModule('/src/main.jsx')).App;
});
after(async () => {await server?.close();});
const data = () => buildWorklist(initialState);
const unescape = value => value.replaceAll('&amp;', '&').replaceAll('&#x27;', "'").replaceAll('&quot;', '"');
const recordIds = html => [...html.matchAll(/data-record-id="([^"]+)"/g)].map(match => match[1]);
const links = html => [...html.matchAll(/<a ([^>]+)>([\s\S]*?)<\/a>/g)].map(match => ({attributes: match[1], href: unescape(match[1].match(/href="([^"]+)"/)[1]), content: match[2]}));
function render(view = 'renewals', search = '', source = data()) {
  return renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [worklistHref(view, search)]}, React.createElement(Worklists, {data: source, view})));
}
function renderApp(path) {
  const windowBefore = Object.getOwnPropertyDescriptor(globalThis, 'window');
  globalThis.window = {matchMedia: () => ({matches: false})};
  try {return renderToStaticMarkup(React.createElement(MotionConfig, {isStatic: true}, React.createElement(MemoryRouter, {initialEntries: [path]}, React.createElement(App))));}
  finally {if (windowBefore) Object.defineProperty(globalThis, 'window', windowBefore); else delete globalThis.window;}
}

test('rendered row order and selected form values follow the query, including opposite sort directions', () => {
  for (const view of ['renewals', 'queue']) {
    for (const direction of ['asc', 'desc']) {
      const search = `stage=negotiation&sort=blockers&direction=${direction}`;
      const html = render(view, search);
      assert.deepEqual(recordIds(html), selectWorklist(data(), view, search).rows.map(row => row.id));
      assert.match(html, /<option value="negotiation" selected="">/);
      assert.match(html, /<option value="blockers" selected="">/);
      if (view === 'renewals') assert.ok(html.includes(`aria-sort="${direction === 'asc' ? 'ascending' : 'descending'}"`));
    }
  }
});

test('every primary row entry is a native link to exactly its resource and keeps the list query', () => {
  for (const view of ['renewals', 'queue']) {
    const search = 'sort=changed&direction=asc';
    const rendered = links(render(view, search)).filter(link => link.attributes.includes('data-row-entry="true"'));
    const expected = selectWorklist(data(), view, search).rows;
    assert.equal(rendered.length, expected.length);
    for (const [index, link] of rendered.entries()) {
      const url = new URL(link.href, 'http://local.test');
      assert.equal(url.pathname, `/${view}`);
      assert.equal(url.searchParams.get('selection'), expected[index].selectionKey);
      assert.equal(url.searchParams.get('sort'), 'changed');
      assert.equal(url.searchParams.get('direction'), 'asc');
      assert.ok(link.attributes.includes('data-worklist-focus='));
    }
  }
});

test('requirement and next-step links identify different resources instead of opening the account page', () => {
  const result = data();
  const rendered = links(render());
  const renewal = result.renewals.find(row => row.id === renewalFixture.id);
  const keys = rendered.map(link => new URL(link.href, 'http://local.test').searchParams.get('selection'));
  assert.ok(keys.includes(renewal.selectionKey));
  assert.ok(keys.includes(renewal.urgentRequirement.selectionKey));
  assert.ok(keys.includes(renewal.nextStep.selectionKey));
  assert.equal(new Set([renewal.selectionKey, renewal.urgentRequirement.selectionKey, renewal.nextStep.selectionKey]).size, 3);
});

test('direct selection renders the exact record ID, revision, and only its allowed workflow entry', () => {
  const result = data();
  for (const row of [...result.renewals, ...result.requirements, ...result.workItems]) {
    const view = row.selectionKey.startsWith('work-item:') ? 'queue' : 'renewals';
    const search = new URLSearchParams({selection: row.selectionKey, sort: 'changed'});
    const html = render(view, search, result);
    assert.ok(html.includes(`class="wl-record-id">${row.id}</span> · Revision ${row.revision}`));
    assert.match(html, /id="worklist-selection-title"[^>]*tabindex="-1"/);
    const actions = links(html).filter(link => link.attributes.includes('wl-primary'));
    assert.equal(actions.length, row.allowedActions.length);
    for (const link of actions) {
      const url = new URL(link.href, 'http://local.test');
      assert.equal(url.searchParams.get('returnTo'), worklistHref(view, search));
      if (row.selectionKey.startsWith('work-item:')) assert.equal(url.searchParams.get('workItem'), row.id);
      if (row.selectionKey.startsWith('requirement:')) assert.equal(url.searchParams.get('requirement'), row.id);
    }
  }
});

test('denied access, loading and errors have no table, search controls, list counts or selected-record details', () => {
  for (const response of ['denied', 'loading', 'error']) {
    for (const view of ['renewals', 'queue']) {
      const html = render(view, new URLSearchParams({response, selection: data().renewals[0].selectionKey}));
      assert.equal(recordIds(html).length, 0);
      assert.doesNotMatch(html, /<table|role="search"|class="wl-facets"|class="wl-results-heading"|class="wl-record-id"/);
      assert.ok(!html.includes(renewalFixture.account.name));
      assert.equal((html.match(/name="response"/g) || []).length, 1);
      if (response === 'loading') assert.match(html, /aria-busy="true"/);
      if (response === 'error') assert.match(html, /role="alert"/);
    }
  }
  assert.doesNotMatch(renderApp('/queue?response=denied'), /class="nav-count"/);
});

test('empty results, truncated results and unknown selection render different structures', () => {
  const empty = render('renewals', 'q=missing-record');
  assert.doesNotMatch(empty, /<table/);
  assert.match(empty, /class="wl-state wl-empty"/);
  assert.match(empty, /role="search"/);
  const truncated = render('renewals', 'response=truncated');
  assert.equal(recordIds(truncated).length, 2);
  assert.match(truncated, /class="wl-truncated"/);
  assert.equal(recordIds(render('renewals', 'response=truncated&limit=all')).length, 4);
  assert.doesNotMatch(render('renewals', 'response=truncated&limit=all'), /class="wl-truncated"/);
  const invalid = render('renewals', 'selection=work-item%3Amissing-record');
  assert.match(invalid, /class="wl-inspector wl-state"/);
  assert.doesNotMatch(invalid, /class="wl-record-id"/);
});

test('queue cells keep action owner, waiting party and due time in separate columns', () => {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review']) state = transition(state, {type});
  const result = buildWorklist(state);
  const row = result.workItems.find(item => item.kind === 'customer_review');
  const html = render('queue', '', result);
  const markup = html.match(new RegExp(`data-record-id="${row.id}"([\\s\\S]*?)</tr>`))[1];
  assert.ok(markup.includes(`data-label="Action owner">${fixturePeople.internalReviewer}</td>`));
  assert.ok(markup.includes(`data-label="Waiting on">${row.waitingLabel}</td>`));
  assert.ok(!markup.includes(`data-label="Waiting on">${fixturePeople.internalReviewer}`));
  assert.match(markup, /data-label="Due date"><time>/);
  assert.ok(!markup.includes('dateTime="null"'));
});

test('readiness selection shows independent CRM, requirement and outcome fields with recorded read time', () => {
  const row = data().renewals[0];
  const html = render('renewals', new URLSearchParams({selection: row.selectionKey}));
  assert.ok(html.includes(`<time dateTime="${row.crm.readAt}">`));
  const facts = html.match(/<dl class="wl-facts">([\s\S]*?)<\/dl>/)[1];
  assert.match(facts, /<dt>CRM stage<\/dt>/);
  assert.match(facts, /<dt>Readiness<\/dt>/);
  assert.match(facts, /<dt>Commercial outcome<\/dt>/);
  assert.equal(worklistFixture.renewals[0].crm.stage, 'negotiation');
});

test('main uses selector work-item counts rather than the old account count and routes both views to the shared worklist', () => {
  for (const path of ['/renewals', '/queue']) {
    const html = renderApp(path);
    assert.match(html, /class="wl-page"/);
    assert.ok(html.includes(`aria-label="${data().openWorkItemCount} open work items"`));
    assert.equal(recordIds(html).length, path === '/renewals' ? data().renewals.length : data().openWorkItemCount);
  }
});
