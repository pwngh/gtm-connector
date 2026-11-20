import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {initialState, readinessFixture, readinessEvidenceFixture, renewalEvidence, walkthroughDecisions} from './renewalFixtures.js';
import {transitionWalkthrough} from './workflow.js';
import {advanceReadinessJournal, readinessHref, selectReadiness} from './readiness.js';

let server, Readiness;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  Readiness = (await server.ssrLoadModule('/src/Readiness.jsx')).default;
});
after(async () => {await server?.close();});
const journal = (state = initialState, fixture = readinessFixture) => advanceReadinessJournal(null, state, fixture);
const unescape = value => value.replaceAll('&amp;', '&').replaceAll('&#x27;', "'").replaceAll('&quot;', '"').replaceAll('&lt;', '<').replaceAll('&gt;', '>');
const links = html => [...html.matchAll(/<a ([^>]+)>([\s\S]*?)<\/a>/g)].map(match => ({attributes: match[1], href: unescape(match[1].match(/href="([^"]+)"/)[1]), content: match[2]}));
const ids = html => [...html.matchAll(/data-check-id="([^"]+)"/g)].map(match => match[1]);
function render(search = '', source = journal()) {
  return renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [readinessHref(search)]}, React.createElement(Readiness, {journal: source, onAction() {}})));
}
function complete() {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review', 'record_customer_approval', 'resolve_requirement']) state = transitionWalkthrough(state, {type, reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step});
  return state;
}

test('rendered check order and controls follow result filtering and stable blocker ordering', () => {
  for (const query of ['', 'order=blockers', 'checkState=failed&order=blockers', 'checkState=not_applicable']) {
    const source = journal(), model = selectReadiness(source, query), html = render(query, source);
    assert.deepEqual(ids(html), [...model.visibleChecks, ...model.evidenceChecks].map(check => check.id));
    assert.ok(html.includes(`<option value="${model.location.checkState}" selected="">`));
    assert.ok(html.includes(`<option value="${model.location.order}" selected="">`));
  }
});

test('each failed transition renders the complete set of named blockers for that check', () => {
  const source = journal(), html = render('', source);
  for (const check of source.current.checks.filter(check => check.result === 'failed')) {
    const chunk = html.slice(html.indexOf(`data-check-id="${check.id}"`)).split('</ul>')[0];
    for (const reason of check.blockedReasons) assert.ok(unescape(chunk).includes(reason.name));
    assert.ok(chunk.includes(`aria-label="All blockers for ${check.title}"`));
  }
});

test('passed, failed, inapplicable and unevaluated checks have distinct semantic status and no inferred zero', () => {
  let state = transitionWalkthrough(initialState, {type: 'request_review'});
  state = transitionWalkthrough(state, {type: 'approve_plan'});
  const fixture = structuredClone(readinessFixture);
  fixture.checks.at(-1).id = 'unsupported_rule';
  const html = render('', journal(state, fixture));
  for (const result of ['passed', 'failed', 'not_applicable', 'not_evaluated']) {
    assert.ok(html.includes(`class="rd-result ${result}" data-check-result="${result}"`));
  }
  const unknown = html.slice(html.indexOf('data-check-id="current_version_basis"')).split('data-check-id="customer_authority"')[0];
  assert.ok(!unknown.includes('rd-result passed'));
  assert.ok(!unknown.includes('0 blockers'));
  const unavailable = render('evaluation=not-evaluated');
  assert.ok(!unavailable.includes('rd-result passed'));
  assert.ok(!unavailable.includes('rd-button rd-primary'));
});

test('selected checks render only their own input versions, evaluation time and exact permitted destination', () => {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document']) state = transitionWalkthrough(state, {type});
  const source = journal(state), check = source.current.checks.find(item => item.id === 'verify_document');
  const html = render('check=verify_document', source);
  assert.ok(links(html).some(link => link.attributes.includes('data-readiness-target="check:verify_document"') && link.attributes.includes('data-readiness-entry="true"')));
  const details = html.split('class="rd-check-details rd-paper"')[1].split('</section>')[0];
  for (const input of check.inputVersions) if (input.id) assert.ok(details.includes(input.id));
  assert.ok(details.includes(check.evaluatedAt));
  const action = links(html).find(link => link.attributes.includes('data-readiness-focus="check:verify_document:action"'));
  const url = new URL(action.href, 'http://local.test');
  assert.equal(url.pathname, readinessFixture.paths.workplan);
  assert.equal(url.searchParams.get('workItem'), readinessFixture.worklist.workItems.find(item => item.kind === 'inspection').id);
  assert.equal(url.searchParams.get('readinessReturn'), readinessHref('check=verify_document'));
});

test('requirements render separate owners, awaited roles, authority unknowns, and one permitted corrective entry', () => {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review']) state = transitionWalkthrough(state, {type});
  const source = journal(state), item = source.current.requirements[0];
  const html = render('', source), facts = html.split('class="rd-responsibility"')[1].split('</dl>')[0];
  assert.ok(facts.match(/<dt>Next-step owner<\/dt><dd>([\s\S]*?)<\/dd>/)[1].includes(item.nextStepOwnerLabel));
  assert.ok(facts.match(/<dt>Waiting on<\/dt><dd>([\s\S]*?)<\/dd>/)[1].includes(unescape(item.waitingLabel)));
  assert.ok(facts.match(/<dt>Customer decision authority<\/dt><dd>([\s\S]*?)<\/dd>/)[1].includes(item.customerAuthorityLabel));
  const actions = links(html).filter(link => link.attributes.includes(`data-readiness-focus="requirement:${item.id}:action"`));
  assert.equal(actions.length, 1);
  assert.equal(new URL(actions[0].href, 'http://local.test').searchParams.get('check'), 'customer_authority');
});

test('the page keeps CRM, requirement readiness and the commercial outcome in separate facts even after resolution', () => {
  const source = journal(complete()), html = render('requirements=all', source);
  for (const fact of ['crm', 'readiness', 'commercial']) assert.equal((html.match(new RegExp(`data-fact="${fact}"`, 'g')) || []).length, 1);
  const crm = html.match(/data-fact="crm">([\s\S]*?)<\/dd>/)[1];
  const commercial = html.match(/data-fact="commercial">([\s\S]*?)<\/dd>/)[1];
  assert.ok(crm.includes(source.current.crm.stageLabel));
  assert.ok(crm.includes(source.current.crm.readAt));
  assert.ok(commercial.includes(source.current.commercial.label));
  assert.equal((html.match(/data-check-result="failed"/g) || []).length, 3);
  assert.ok(!html.includes('rd-requirement-state passed'));
});

test('citations render fixture text exactly, distinguish workflow notes, and link by card plus unit number', () => {
  const html = render(new URLSearchParams({requirement: initialState.requirement.id}));
  const quotations = [...html.matchAll(/<blockquote class="rd-quote">([\s\S]*?)<\/blockquote>/g)].map(match => unescape(match[1]));
  assert.deepEqual(quotations, renewalEvidence.map(item => item.quote));
  assert.equal((html.match(/class="rd-workflow-note"/g) || []).length, renewalEvidence.length);
  for (const unit of readinessFixture.card.units.filter(item => item.kind === 'sentence')) {
    assert.ok(unescape(html).includes(unit.speaker.basis));
    assert.ok(unescape(html).includes(unit.speaker.role));
    assert.equal(unit.partial, false);
    assert.ok(!html.includes('class="rd-label rd-cut"'));
  }
  const citations = links(html).filter(link => link.attributes.includes(`data-readiness-focus="requirement:${initialState.requirement.id}:unit:`));
  assert.equal(citations.length, 2);
  for (const citation of citations) {
    const url = new URL(citation.href, 'http://local.test');
    assert.equal(url.pathname, readinessFixture.paths.readiness);
    assert.equal(url.searchParams.get('card'), readinessFixture.card.id);
    assert.equal(url.searchParams.get('requirement'), initialState.requirement.id);
    assert.ok(readinessFixture.card.units.some(unit => unit.n === url.searchParams.get('unit')));
  }
});

test('record units render fields, sentence units render quotations, and hidden units never leak their text', () => {
  const record = readinessFixture.card.units.find(item => item.kind === 'record');
  const html = render(new URLSearchParams({card: record.cardId, unit: record.n}));
  const inspector = html.split('class="rd-unit-inspector rd-paper"')[1].split('</section>')[0];
  assert.ok(inspector.includes('rd-record-fields'));
  assert.ok(!inspector.includes('<blockquote'));
  for (const field of record.fields) assert.ok(inspector.includes(field.value));
  const fixture = structuredClone(readinessFixture);
  fixture.card.units[0].shown = false;
  fixture.card.units[0].notShownReason = 'cut';
  const hidden = render(new URLSearchParams({card: fixture.card.id, unit: '1'}), journal(initialState, fixture));
  assert.ok(!unescape(hidden).includes(renewalEvidence[0].quote));
  assert.ok(hidden.includes('rd-unit-inspector'));
  assert.ok(!hidden.includes('dangerouslySetInnerHTML'));
});

test('coverage renders each source read time and each gap independently, with no percentage or claim of completeness', () => {
  const html = render(), coverage = html.split('class="rd-coverage rd-paper"')[1].split('class="rd-changes"')[0];
  assert.deepEqual([...coverage.matchAll(/data-gap-code="([^"]+)"/g)].map(match => match[1]), readinessFixture.card.gaps.map(gap => gap.code));
  for (const source of readinessFixture.card.sources) {
    const row = coverage.split(`data-source-id="${source.id}"`)[1].split('</li>')[0];
    assert.ok(row.includes(source.readAt));
  }
  assert.ok(!coverage.includes('%'));
  const unknown = render('', journal(initialState, {...readinessFixture, card: null}));
  assert.ok(!unknown.includes('data-gap-code='));
  assert.ok(!unknown.includes('data-source-id='));
  assert.ok(unknown.includes('id="readiness-coverage" tabindex="-1"'));
});

test('stale results remain visibly retained and expose no current action links or green result classes', () => {
  const first = journal(), next = advanceReadinessJournal(first, transitionWalkthrough(initialState, {type: 'request_review'}));
  const html = render('evaluation=stale&check=submit_plan', next);
  assert.ok(html.includes('role="alert"'));
  assert.ok(html.includes('class="rd-result retained" data-check-result="passed"'));
  assert.ok(!html.includes('rd-result passed'));
  assert.ok(!html.includes('rd-button rd-primary'));
  assert.ok(html.includes(first.current.evaluationId));
  assert.ok(html.includes(next.current.evaluationId));
});

test('empty requirement views, unknown selections and filter-hidden selections have different rendered structures', () => {
  const empty = render('', journal(complete(), readinessEvidenceFixture));
  assert.ok(empty.includes('class="rd-empty rd-paper"'));
  assert.ok(!empty.includes('data-requirement-id='));
  const missing = render('check=missing');
  assert.ok(missing.includes('rd-selection-error'));
  assert.ok(missing.includes('data-readiness-target="check:missing"'));
  assert.ok(!missing.includes('rd-check-details rd-paper'));
  const hidden = render('check=verify_document&checkState=passed');
  assert.ok(hidden.includes('rd-check-details rd-paper'));
  assert.equal((hidden.match(/data-check-id="verify_document"/g) || []).length, 1);
});

test('every local focus key is unique even where the same check or source is cited from several places', () => {
  const html = render('check=verify_document');
  const keys = [...html.matchAll(/data-readiness-focus="([^"]+)"/g)].map(match => match[1]);
  assert.equal(new Set(keys).size, keys.length);
  const entries = links(html).filter(link => link.attributes.includes('data-readiness-entry="true"'));
  assert.equal(entries.length, readinessFixture.requirements.length + readinessFixture.checks.length);
  assert.ok(entries.every(link => new URL(link.href, 'http://local.test').pathname === readinessFixture.paths.readiness));
});

test('comparison rendering retains removed check rows and changed blocker explanations', () => {
  let state = initialState;
  for (const type of ['request_review', 'approve_plan', 'obtain_document']) state = transitionWalkthrough(state, {type});
  const before = journal(state), fixture = structuredClone(readinessFixture);
  fixture.checks = fixture.checks.filter(check => check.id !== 'verify_document');
  const removed = advanceReadinessJournal(before, state, fixture);
  const removedHtml = render('', removed);
  assert.ok(!ids(removedHtml).includes('verify_document'));
  const row = removedHtml.split('data-comparison-id="verify_document"')[1].split('</li>')[0];
  assert.ok(row);
  assert.ok(unescape(row).includes(removed.changes.checks.find(check => check.id === 'verify_document').beforeLabel));
  assert.ok(unescape(row).includes(removed.changes.checks.find(check => check.id === 'verify_document').afterLabel));

  const earlier = {...state, document: {...state.document, version: 'outdated'}};
  const later = {...earlier, document: {...earlier.document, currentVersion: 'replacement'}};
  const changed = advanceReadinessJournal(journal(earlier), later);
  const html = render('', changed), comparison = html.split('data-comparison-id="verify_document"')[1].split('</li>')[0];
  const difference = changed.changes.checks.find(check => check.id === 'verify_document').updated[0];
  assert.ok(unescape(comparison).includes(difference.before.reason));
  assert.ok(unescape(comparison).includes(difference.after.reason));
  assert.equal(changed.current.evaluatedAt, null);
});

test('failed and recorded checks render different icon shapes, with recorded state separate from evidence failure', () => {
  const state = transitionWalkthrough(initialState, {type: 'request_review'});
  const source = journal(state), html = render('', source);
  const badge = result => html.match(new RegExp(`<span class="rd-result ${result}"[^>]*>([\\s\\S]*?)<\\/span>`))[1];
  const paths = value => [...value.matchAll(/<path d="([^"]+)"/g)].map(match => match[1]);
  assert.notDeepEqual(paths(badge('failed')), paths(badge('not_applicable')));
  const recorded = source.current.checks.find(check => check.recorded);
  assert.ok(unescape(badge('not_applicable')).includes(recorded.resultLabel));
  assert.equal(recorded.allowedActions.length, 0);
  assert.equal(source.current.checks.find(check => check.id === 'customer_authority').result, 'failed');
});
