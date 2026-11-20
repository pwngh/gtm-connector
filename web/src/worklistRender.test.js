import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {MotionConfig} from 'motion/react';
import {createServer} from 'vite';
import {derive, initialState, transition} from './workflow.js';
import {renewalFixture, worklistFixture, fixturePeople} from './renewalFixtures.js';
import {buildWorklist, worklistHref} from './worklist.js';

let server, Worklists, WorklistStepEntry, ReviewDecision, App;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  const module = await server.ssrLoadModule('/src/Worklists.jsx');
  Worklists = module.default; WorklistStepEntry = module.WorklistStepEntry;
  ReviewDecision = (await server.ssrLoadModule('/src/ReviewDecision.jsx')).default;
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
function renderApp(path, state = initialState) {
  const windowBefore = Object.getOwnPropertyDescriptor(globalThis, 'window');
  const storageBefore = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
  globalThis.localStorage = {getItem: key => key === 'gtm-workflow-demo-v1' ? JSON.stringify(state) : null};
  globalThis.window = {matchMedia: () => ({matches: false})};
  try {return renderToStaticMarkup(React.createElement(MotionConfig, {isStatic: true}, React.createElement(MemoryRouter, {initialEntries: [path]}, React.createElement(App))));}
  finally {
    if (windowBefore) Object.defineProperty(globalThis, 'window', windowBefore); else delete globalThis.window;
    if (storageBefore) Object.defineProperty(globalThis, 'localStorage', storageBefore); else delete globalThis.localStorage;
  }
}

test('rendered row order and selected form values follow literal sort orders in both directions', () => {
  const expected = {
    renewals: {asc: ['thornfield-a17', 'calder-renewal'], desc: ['calder-renewal', 'thornfield-a17']},
    queue: {
      asc: ['work-plan-review-ai-terms', 'work-inspect-ai-terms', 'work-review-ai-terms', 'task-obtain-ai-terms', 'work-calder-exception'],
      desc: ['work-calder-exception', 'work-plan-review-ai-terms', 'work-inspect-ai-terms', 'work-review-ai-terms', 'task-obtain-ai-terms'],
    },
  };
  for (const view of ['renewals', 'queue']) {
    for (const direction of ['asc', 'desc']) {
      const html = render(view, `scope=all&stage=negotiation&sort=blockers&direction=${direction}`);
      assert.deepEqual(recordIds(html), expected[view][direction]);
      assert.match(html, /<option value="negotiation" selected="">/);
      assert.match(html, /<option value="blockers" selected="">/);
      if (view === 'renewals') assert.ok(html.includes(`aria-sort="${direction === 'asc' ? 'ascending' : 'descending'}"`));
    }
  }
});

test('every primary row entry is a native link in literal change order and keeps the list query', () => {
  const expected = {
    renewals: ['renewal:thornfield-a17', 'renewal:ashbourne-renewal', 'renewal:calder-renewal', 'renewal:mercer-renewal'],
    queue: ['work-item:work-plan-review-ai-terms', 'work-item:work-inspect-ai-terms', 'work-item:work-review-ai-terms', 'work-item:task-obtain-ai-terms', 'work-item:work-ashbourne-package', 'work-item:work-calder-exception', 'work-item:work-mercer-owner'],
  };
  for (const view of ['renewals', 'queue']) {
    const search = 'scope=all&sort=changed&direction=asc';
    const rendered = links(render(view, search)).filter(link => link.attributes.includes('data-row-entry="true"'));
    assert.equal(rendered.length, expected[view].length);
    for (const [index, link] of rendered.entries()) {
      const url = new URL(link.href, 'http://local.test');
      assert.equal(url.pathname, `/${view}`);
      assert.equal(url.searchParams.get('selection'), expected[view][index]);
      assert.equal(url.searchParams.get('sort'), 'changed');
      assert.equal(url.searchParams.get('direction'), 'asc');
      assert.equal(url.searchParams.get('scope'), 'all');
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
    assert.ok(html.includes(`class="wl-record-id">${row.id}</span>`));
    assert.ok(html.includes(`revision ${row.revision}`) || html.includes(`Revision ${row.revision}`));
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
  const truncated = render('renewals', 'scope=all&response=truncated');
  assert.equal(recordIds(truncated).length, 2);
  assert.match(truncated, /class="wl-truncated"/);
  assert.equal(recordIds(render('renewals', 'scope=all&response=truncated&limit=all')).length, 4);
  assert.doesNotMatch(render('renewals', 'scope=all&response=truncated&limit=all'), /class="wl-truncated"/);
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
  assert.ok(markup.includes(`data-label="Waiting on">${row.waitingLabel}<span`));
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
    assert.match(html, /aria-label="4 open walkthrough work items"/);
    assert.equal(recordIds(html).length, path === '/renewals' ? 1 : 4);
  }
});

test('the queue opens with the actionable plan review, followed by blocked and planned work', () => {
  assert.deepEqual(recordIds(render('queue')), ['work-plan-review-ai-terms', 'work-inspect-ai-terms', 'work-review-ai-terms', 'task-obtain-ai-terms']);
  assert.deepEqual(recordIds(render('renewals')), ['thornfield-a17']);
  const html = render('queue');
  assert.match(html, /name="scope"[^>]*checked=""[^>]*value="walkthrough"|name="scope"[^>]*value="walkthrough"[^>]*checked=""/);
  assert.match(html, /<option value="due" selected="">/);
});

test('renewal cells show the current action owner separately from the renewal owner and awaited party', () => {
  const submitted = buildWorklist(transition(initialState, {type: 'request_review'}));
  const html = render('renewals', '', submitted);
  const nextCell = html.match(/data-label="Next step">([\s\S]*?)<\/td>/)[1];
  assert.ok(nextCell.includes(fixturePeople.internalReviewer));
  assert.ok(!nextCell.includes(fixturePeople.accountOwner));
  const ownerCell = html.match(/data-label="Renewal owner">([\s\S]*?)<\/td>/)[1];
  assert.equal(ownerCell, fixturePeople.accountOwner);
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review']) state = transition(state, {type});
  const waiting = render('renewals', '', buildWorklist(state)).match(/data-label="Next step">([\s\S]*?)<\/td>/)[1];
  assert.ok(waiting.includes(worklistFixture.workItems[3].awaitedParty.label));
});

test('direction and urgent link accessible names include their full visible labels', () => {
  for (const direction of ['asc', 'desc']) {
    const html = render('renewals', `direction=${direction}`);
    const match = html.match(/<button[^>]*class="wl-button wl-direction"[^>]*aria-label="([^"]+)"[^>]*>([\s\S]*?)<\/button>/);
    const visible = match[2].replace(/<[^>]*>/g, '').trim();
    assert.ok(match[1].includes(visible));
    const urgent = links(html).find(link => link.attributes.includes('|urgent-requirement'));
    const label = urgent.attributes.match(/aria-label="([^"]+)"/)[1];
    assert.ok(label.includes(urgent.content.replace(/<[^>]*>/g, '').trim()));
  }
});

test('counts use singular nouns for one renewal, work item, and blocking requirement', () => {
  for (const [view, query] of [['renewals', 'selection=renewal%3Athornfield-a17'], ['queue', 'attention=actionable']]) {
    const html = render(view, query);
    assert.doesNotMatch(html, /\b1 renewals\b|\b1 work items\b|\b1 open blocking requirements\b/);
    assert.match(html, view === 'renewals' ? /1 of 1 matching renewal</ : /1 of 1 matching work item</);
  }
});

test('waiting basis appears only with the awaited party, not as a blocking prerequisite', () => {
  const row = data().workItems.find(item => item.status === 'waiting');
  const html = render('queue', new URLSearchParams({scope: 'illustrative', selection: row.selectionKey}));
  const markup = html.match(new RegExp(`data-record-id="${row.id}"([\\s\\S]*?)</tr>`))[1];
  assert.ok(markup.match(/data-label="Waiting on">([\s\S]*?)<\/td>/)[1].includes(row.waitingReason));
  assert.ok(!markup.match(/data-label="Blocked, because">([\s\S]*?)<\/td>/)[1].includes(row.waitingReason));
  const inspector = html.slice(html.indexOf('<section class="wl-inspector"'));
  assert.doesNotMatch(inspector, /class="wl-blocked"/);
  assert.ok(inspector.includes(row.waitingReason));
});

test('a requirement without related work has an empty state instead of an empty list', () => {
  const html = render('renewals', 'scope=illustrative&selection=requirement%3Areq-calder-decision-owner');
  const section = html.match(/<section class="wl-related-section">([\s\S]*?)<\/section>/)[1];
  assert.doesNotMatch(section, /<ul/);
  assert.match(section, /<p>/);
});

test('readiness and supporting evidence action URLs carry the complete originating worklist', () => {
  for (const selection of ['renewal:thornfield-a17', 'requirement:req-ai-counsel']) {
    const search = new URLSearchParams({scope: 'all', sort: 'blockers', direction: 'desc', selection});
    const rendered = links(render('renewals', search));
    const destinations = rendered.filter(link => link.href.startsWith(`${renewalFixture.paths.root}/`));
    assert.ok(destinations.length);
    for (const link of destinations) assert.equal(new URL(link.href, 'http://local.test').searchParams.get('returnTo'), worklistHref('renewals', search));
  }
});

test('table rows and cells retain explicit semantics when the responsive layout changes their display', () => {
  for (const view of ['renewals', 'queue']) {
    const html = render(view, 'scope=all');
    for (const [tag, role] of [['table', 'table'], ['thead', 'rowgroup'], ['tbody', 'rowgroup'], ['tr', 'row'], ['th', 'columnheader'], ['td', 'cell']]) {
      const elements = [...html.matchAll(new RegExp(`<${tag}(?=[ >])[^>]*>`, 'g'))].map(match => match[0]);
      assert.ok(elements.length);
      assert.ok(elements.every(element => element.includes(`role="${role}"`)));
    }
  }
});

test('the exact work destination renders only its permitted action, prerequisite, or recorded output', () => {
  let state = initialState;
  const states = [state];
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document']) {state = transition(state, {type}); states.push(state);}
  for (const state of states) {
    for (const item of buildWorklist(state).workItems.filter(row => row.mode === 'walkthrough')) {
      const html = renderToStaticMarkup(React.createElement(WorklistStepEntry, {item, onAction() {}}));
      assert.ok(html.includes(`id="${item.entryTargetId}" tabindex="-1"`));
      assert.equal((html.match(/<button /g) || []).length, item.workflowAction ? 1 : 0);
      assert.equal(html.includes('class="wl-step-output"'), Boolean(item.output));
      assert.equal(html.includes('class="wl-step-prerequisites"'), item.blockedReasons.length > 0);
    }
  }
});

test('the Work plan review form uses shared eligibility and cannot approve a submitted self-owned plan', () => {
  const submitted = transition(initialState, {type: 'request_review'});
  for (const owner of [fixturePeople.internalReviewer, fixturePeople.accountOwner]) {
    const state = {...submitted, plan: {...submitted.plan, owner}}, derived = derive(state);
    const html = renderToStaticMarkup(React.createElement(ReviewDecision, {type: 'review', state, derived, onPerform() {}}));
    const approve = html.match(/<button[^>]*class="button primary"([^>]*)>/)[1];
    assert.equal(approve.includes('disabled'), owner === fixturePeople.internalReviewer);
    assert.equal(buildWorklist(state).workItems[0].canAct, owner !== fixturePeople.internalReviewer);
  }
});

test('the main badge follows walkthrough outputs without counting illustrative work as open tasks', () => {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document']) state = transition(state, {type});
  const html = renderApp('/queue', state);
  assert.match(html, /aria-label="1 open walkthrough work item">1<\/span>/);
  assert.deepEqual(recordIds(html), ['work-review-ai-terms']);
  assert.equal(recordIds(renderApp('/queue?scope=illustrative', state)).length, 3);
});
