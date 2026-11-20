import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {approvalLetters, historyFixture, historyTestRecords, initialState, readinessFixture, walkthroughActors, walkthroughDocumentExamples} from './renewalFixtures.js';
import {walkthroughCommands} from './customerDecision.js';
import {typedInputs, walkthroughInputs} from './testing/walkthroughInputs.js';
import {transitionWalkthrough} from './workflow.js';
import {historyHref, selectHistory} from './history.js';
import {hydrateHistoryRecord} from './historyRecording.js';

let server, History;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  History = (await server.ssrLoadModule('/src/History.jsx')).default;
});
after(async () => {await server?.close();});
const unescape = value => value.replaceAll('&amp;', '&').replaceAll('&#x27;', "'").replaceAll('&quot;', '"').replaceAll('&lt;', '<').replaceAll('&gt;', '>');
const links = html => [...html.matchAll(/<a ([^>]+)>([\s\S]*?)<\/a>/g)].map(match => ({attributes: match[1], href: unescape(match[1].match(/href="([^"]+)"/)[1]), content: match[2]}));
const ids = html => [...html.matchAll(/data-history-id="([^"]+)"/g)].map(match => unescape(match[1]));
// Document inspection needs the reason the operator types; tests supply it here.
const act = (state, type, extra = {}) => transitionWalkthrough(state, {type, ...(type === 'verify_document' ? typedInputs.verify_document : {}), ...extra});
const complete = () => walkthroughCommands.reduce((state, type) => act(state, type, walkthroughInputs(state, type)), initialState);
function render(search = '', state = initialState) {
  return renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [historyHref(search)]}, React.createElement(History, {state, onExport() {}})));
}
const example = historyFixture.examples.find(item => item.mode === 'example');
const exampleId = type => example.steps.find(step => step.action?.type === type || step.correction?.type === type).id;

test('rendered history order, filters and selected point correspond to the selector rather than local UI state', () => {
  const state = complete();
  for (const query of ['', 'order=newest', 'category=document&order=newest', 'category=correction', `as_of=${state.history[2].id}`]) {
    const view = selectHistory(state, query), html = render(query, state);
    assert.deepEqual(ids(html), view.visibleEntries.map(entry => entry.id));
    assert.ok(html.includes(`<option value="${view.location.category}" selected="">`));
    assert.ok(html.includes(`<option value="${view.location.order}" selected="">`));
    assert.ok(html.includes(`<option value="${view.location.asOf}" selected="">`));
    const entries = links(html).filter(link => link.attributes.includes('data-history-entry="true"'));
    assert.equal(entries.length, view.visibleEntries.length);
  }
});

test('selected entries expose separately labelled clocks with the original and outcome recording times', () => {
  const query = new URLSearchParams({example: example.id, entry: exampleId('record_customer_approval')}), view = selectHistory(initialState, query), html = render(query);
  const entry = view.selection.resource;
  const clocks = html.split('class="hs-clocks"')[1].split('</dl>')[0];
  assert.deepEqual([...clocks.matchAll(/data-clock="([^"]+)"/g)].map(match => match[1]), ['event', 'recorded', 'outcome']);
  const event = clocks.split('data-clock="event"')[1].split('</div>')[0];
  const recorded = clocks.split('data-clock="recorded"')[1].split('</div>')[0];
  const outcome = clocks.split('data-clock="outcome"')[1].split('</div>')[0];
  assert.ok(event.includes(`dateTime="${entry.eventAt}"`));
  assert.ok(recorded.includes(`dateTime="${entry.recordedAt}"`));
  assert.ok(outcome.includes(`dateTime="${entry.outcomes[0].recordedAt}"`));
  assert.ok(!event.includes(entry.outcomes[0].recordedAt));
});

test('a known-by cutoff withholds future details and their outcomes even when a future entry is selected', () => {
  const state = complete(), first = state.history[0], last = state.history.at(-1);
  const html = render(new URLSearchParams({as_of: first.id, entry: last.id}), state);
  assert.ok(html.includes('hs-selection-error'));
  assert.ok(!html.includes('class="hs-proof"'));
  assert.ok(!unescape(html).includes(last.title));
  assert.ok(!unescape(html).includes(state.customerReview.evidenceText));
  assert.ok(!html.includes('data-clock="outcome"'));
  const request = render(new URLSearchParams({as_of: first.id, entry: first.id}), state);
  const outcomes = request.split('data-clock="outcome"')[1].split('</div>')[0];
  assert.ok(!outcomes.includes('<time'));
  assert.ok(!outcomes.includes('<a'));
});

test('the recorded decision renders as attributed scoped values, never as a source quotation or typed text', () => {
  const state = complete(), id = state.history.find(row => row.type === 'record_customer_approval').id, html = render(new URLSearchParams({entry: id}), state);
  const quotes = [...html.matchAll(/<blockquote class="hs-source-text">([\s\S]*?)<\/blockquote>/g)].map(match => unescape(match[1]));
  assert.deepEqual(quotes, readinessFixture.requirements[0].citations.map(citation => readinessFixture.card.units.find(unit => unit.n === citation.n).text));
  assert.ok(!html.includes('data-history-field="decision.text"'));
  const decision = html.split('data-history-field="decision.reviewer"')[1].split('</dd>')[0];
  assert.ok(unescape(decision).includes(state.customerReview.reviewedBy));
  assert.ok(unescape(decision).includes('Evidence record'));
  const recorder = html.split('data-history-field="decision.recordedBy"')[1].split('</dd>')[0];
  assert.ok(unescape(recorder).includes(state.customerReview.recordedBy));
  assert.ok(unescape(decision).includes(state.customerReview.reviewedBy));
  assert.ok(decision.includes(state.customerReview.documentVersion));
  assert.ok(decision.includes('hs-attribution'));
  assert.ok(decision.includes('hs-value-scope'));
});

test('corrections render supersedes links and typed attribution changes while the effective view retains the original values', () => {
  const correctionId = exampleId('correction'), decisionId = exampleId('record_customer_approval');
  const correction = render(new URLSearchParams({example: example.id, entry: correctionId}));
  const supersedes = links(correction).find(link => link.attributes.includes(`data-history-focus="supersedes:${correctionId}"`));
  assert.equal(new URL(supersedes.href, 'http://local.test').searchParams.get('entry'), decisionId);
  assert.ok(correction.includes('data-diff-field="decision.reviewer"'));
  assert.ok(!correction.includes('data-diff-field="decision.text"'));
  assert.ok(!correction.includes('data-diff-field="decision.authority"'));
  assert.ok(!correction.includes('data-diff-field="decision.citation"'));
  assert.ok(!correction.includes('data-diff-field="source.eventAt"'));
  const effective = render(new URLSearchParams({example: example.id, entry: decisionId, view: 'effective'}));
  const original = effective.split('class="hs-disclosure hs-original"')[1].split('</details>')[0];
  assert.ok(unescape(original).includes(approvalLetters[0].signatory.name));
  assert.ok(unescape(original).includes('receipt-approval-letter-v4-1'));
  assert.ok(links(effective).some(link => link.attributes.includes(`data-history-focus="applied:${decisionId}:${correctionId}"`)));
});

test('derived outputs, requirement resolutions and the commercial result have independent rendered rows and links', () => {
  const state = complete(), view = selectHistory(state), html = render('', state);
  assert.equal((html.match(/data-history-type="work_item_output"/g) || []).length, view.derivedCount);
  assert.equal((html.match(/data-history-type="resolve_requirement"/g) || []).length, 1);
  const outcomes = [...html.matchAll(/data-history-outcome="([^"]+)"/g)].map(match => match[1]);
  assert.deepEqual(outcomes, ['requirement', 'work', 'commercial']);
  const commercial = html.split('data-history-outcome="commercial"')[1].split('</div>')[0];
  assert.ok(unescape(commercial).includes(initialState.commercial.label));
  assert.ok(!commercial.includes('<time'));
  assert.ok(!commercial.includes('<a'));
  const outputId = view.entries.find(row => row.type === 'work_item_output').id;
  const selected = render(new URLSearchParams({entry: outputId}), state);
  const cause = links(selected).find(link => link.attributes.includes(`data-history-focus="cause:${outputId}"`));
  assert.equal(new URL(cause.href, 'http://local.test').searchParams.get('entry'), view.entries.find(row => row.id === outputId).parentId);
});

test('document and work-item inspection links keep historical version values and use the exact current destination', () => {
  const original = complete(), receipt = original.history.find(event => event.type === 'obtain_document');
  const state = act(original, 'obtain_document', {document: walkthroughDocumentExamples.replacement, actorId: walkthroughActors.acquisitionId});
  const query = new URLSearchParams({entry: receipt.id, as_of: receipt.id, category: 'document'}), html = render(query, state);
  const version = html.split('data-history-field="document.version"')[1].split('</dd>')[0];
  assert.ok(version.includes(original.document.version));
  assert.ok(!version.includes(state.document.version));
  const destination = links(html).find(link => link.attributes.includes(`data-history-focus="inspect:${receipt.id}:workplan"`));
  const url = new URL(destination.href, 'http://local.test');
  const work = readinessFixture.worklist.workItems.find(item => item.kind === 'collection');
  assert.equal(url.searchParams.get('workItem'), work.id);
  assert.equal(url.hash, `#work-item-${work.id}`);
  assert.equal(url.searchParams.get('historyReturn'), historyHref(query));
});

test('unknown entries, invalid points and empty category results expose different recovery structures', () => {
  const unknown = render('entry=missing');
  assert.ok(unknown.includes('hs-selection-error'));
  assert.ok(unknown.includes('data-history-target="entry:missing"'));
  assert.ok(!unknown.includes('class="hs-proof"'));
  const point = render('as_of=missing&entry=missing');
  assert.ok(point.includes('class="hs-error hs-paper" role="alert"'));
  assert.equal(ids(point).length, 0);
  assert.ok(point.includes('disabled=""'));
  const empty = render('category=correction');
  assert.ok(empty.includes('class="hs-empty hs-paper"'));
  assert.equal(ids(empty).length, 0);
  assert.ok(!empty.includes('role="alert"'));
});

test('open and closed intervals render measured endpoints and exact transition links without making up an opening time', () => {
  const state = act(complete(), 'reopen_requirement', {reason: historyTestRecords.editReason, actorId: walkthroughActors.reviewerId}), view = selectHistory(state), html = render('', state);
  const intervals = view.intervals[0].intervals;
  assert.deepEqual([...html.matchAll(/data-interval-status="([^"]+)"/g)].map(match => match[1]), ['closed', 'open']);
  assert.equal((html.match(/data-interval-id=/g) || []).length, intervals.length);
  for (const interval of intervals) {
    const section = html.split(`data-interval-id="${interval.id}"`)[1].split('</li>')[0];
    assert.ok(unescape(section).includes(interval.durationLabel));
    if (interval.openedAt) assert.ok(section.includes(`dateTime="${interval.openedAt}"`));
    if (interval.resolvedAt) assert.ok(section.includes(`dateTime="${interval.resolvedAt}"`));
    if (interval.startEntryId) {
      const link = links(section).find(link => link.attributes.includes(`data-history-focus="interval:${interval.id}:start"`));
      assert.equal(new URL(link.href, 'http://local.test').searchParams.get('entry'), interval.startEntryId);
    }
  }
});

test('every history focus key stays unique across repeated references, corrected projections and hidden selections', () => {
  const state = complete();
  for (const [query, source] of [[new URLSearchParams({entry: state.history.at(-1).id, category: 'document'}), state],
    [new URLSearchParams({example: example.id, entry: exampleId('record_customer_approval'), view: 'effective'}), initialState]]) {
    const html = render(query, source), keys = [...html.matchAll(/data-history-focus="([^"]+)"/g)].map(match => match[1]);
    assert.equal(new Set(keys).size, keys.length);
    assert.equal((html.match(/class="hs-proof"/g) || []).length, 1);
    const selectedId = query.get('entry');
    assert.equal(ids(html).filter(id => id === selectedId).length, 1);
    const target = links(html).find(link => link.attributes.includes(`data-history-target="entry:${selectedId}"`));
    assert.ok(target.attributes.includes('data-history-entry="true"'));
    assert.ok(target.attributes.includes('aria-current="true"'));
  }
});

test('readiness effects render the selector’s actual invalidated checks, not success styling from resolution', () => {
  const state = act(complete(), 'save_plan', {specifications: historyTestRecords.revisedSpecifications, actorId: walkthroughActors.acquisitionId}), id = state.history.at(-1).id;
  const view = selectHistory(state, new URLSearchParams({entry: id})), html = render(new URLSearchParams({entry: id}), state);
  assert.deepEqual([...html.matchAll(/data-effect="([^"]+)" data-effect-status="invalidated"/g)].map(match => match[1]), view.selection.resource.impacts.effects.filter(effect => effect.status === 'invalidated').map(effect => effect.id));
  // The authority record and the agreement record's version basis survive a specifications edit; the decision marked for re-review does not.
  assert.equal((html.match(/data-evidence-result="failed"/g) || []).length, 1);
  assert.equal((html.match(/data-evidence-result="passed"/g) || []).length, 2);
});


test('malformed nested saved inputs render partial details safely instead of evaluating the broken snapshot', () => {
  for (const mutate of [record => {record.after.requirements = {};}, record => {record.contextAfter.contacts = [null];},
    record => {record.contextAfter.card.units[2].fields = [null];}]) {
    const state = complete(), event = state.history.at(-1);
    event.historyRecord = structuredClone(hydrateHistoryRecord(event.historyRecord, state.historyContexts));
    mutate(event.historyRecord);
    const query = new URLSearchParams({entry: event.id}), view = selectHistory(state, query), html = render(query, state);
    assert.equal(view.selection.resource.complete, false);
    assert.ok(html.includes('<section class="hs-proof"'));
    assert.equal((html.match(/data-evidence-result=/g) || []).length, 0);
    assert.equal((html.match(/data-effect-status=/g) || []).length, 0);
  }
});

test('required reasons are present in collapsed decision rows without opening an entry', () => {
  const state = act(act(initialState, 'request_review'), 'request_changes', {note: historyTestRecords.editReason});
  const html = render('', state), row = html.split(`data-history-id="${state.history.at(-1).id}"`)[1].split('</li>')[0];
  assert.ok(!row.includes('class="hs-proof"'));
  assert.ok(row.includes('class="hs-row-reason"'));
  assert.ok(unescape(row).includes(state.history.at(-1).reason));
  const correction = render(new URLSearchParams({example: example.id}));
  const collapsed = correction.split(`data-history-id="${exampleId('correction')}"`)[1].split('</li>')[0];
  assert.ok(collapsed.includes('class="hs-row-reason"'));
  assert.ok(!collapsed.includes('class="hs-proof"'));
});

test('collapsed effective entries distinguish corrected projections and their dependent resolution', () => {
  const recorded = render(new URLSearchParams({example: example.id}));
  const effective = render(new URLSearchParams({example: example.id, view: 'effective'}));
  for (const id of [exampleId('record_customer_approval'), exampleId('resolve_requirement')]) {
    const originalRow = recorded.split(`data-history-id="${id}"`)[1].split('</li>')[0];
    const effectiveRow = effective.split(`data-history-id="${id}"`)[1].split('</li>')[0];
    assert.ok(!originalRow.includes('data-corrected-projection="true"'));
    assert.ok(effectiveRow.includes('data-corrected-projection="true"'));
    assert.ok(!effectiveRow.includes('class="hs-proof"'));
  }
});
