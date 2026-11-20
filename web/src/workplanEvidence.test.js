import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {MessageSquare, Phone} from 'lucide-react';
import {initialState, readinessFixture, renewalEvidence, renewalSourceBoundaries, walkthroughDecisions} from './renewalFixtures.js';
import {derive, transitionWalkthrough} from './workflow.js';
import {advanceReadinessJournal, readinessHref} from './readiness.js';

let server, WorkPlan, Evidence, Dialogs, ReviewDecision, Readiness;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  WorkPlan = (await server.ssrLoadModule('/src/WorkPlan.jsx')).default;
  Evidence = (await server.ssrLoadModule('/src/Evidence.jsx')).default;
  Dialogs = (await server.ssrLoadModule('/src/WorkflowDialogContent.jsx')).default;
  ReviewDecision = (await server.ssrLoadModule('/src/ReviewDecision.jsx')).default;
  Readiness = (await server.ssrLoadModule('/src/Readiness.jsx')).default;
});
after(async () => {await server?.close();});

const evidence = renewalEvidence.map(record => ({...record, icon: record.binding === 'customer_statement' ? Phone : MessageSquare}));
const noop = () => {};
const text = html => html.replace(/<[^>]+>/g, ' ').replaceAll('&amp;', '&').replaceAll('&#x27;', "'").replaceAll('&quot;', '"').replace(/\s+/g, ' ');

function walk(steps) {
  let state = initialState;
  for (const type of steps) state = transitionWalkthrough(state, {type, reviewedBy: walkthroughDecisions.reviewedBy, evidenceText: walkthroughDecisions.step});
  return state;
}
const allSteps = ['request_review', 'approve_plan', 'obtain_document', 'verify_document', 'request_customer_review', 'record_customer_approval', 'resolve_requirement'];
const states = [initialState, walk(allSteps.slice(0, 3)), walk(allSteps.slice(0, 6)), walk(allSteps)];

function workPlan(state, selectedStep = 1, records = evidence) {
  return renderToStaticMarkup(React.createElement(WorkPlan, {
    state, derived: derive(state), evidence: records, selectedStep, setSelectedStep: noop, selectedWorkItem: null, openStep: noop,
    openWorklistAction: noop, openCustomerDecision: noop, openEdit: noop, setModal: noop, navigate: noop, source: noop,
    action: {label: 'Next step', fn: noop},
  }));
}
function evidencePage(state, records = evidence) {
  return renderToStaticMarkup(React.createElement(Evidence, {state, derived: derive(state), evidence: records, source: noop, setModal: noop, openStep: noop}));
}
// Dialog content renders inside the shared dialog, as it does in the app.
function dialog(modal, state = initialState) {
  return renderToStaticMarkup(React.createElement('section', {className: 'dialog'}, React.createElement(Dialogs, {
    modal, state, derived: derive(state), edit: {}, setEdit: noop, setModal: noop, openStep: noop, perform: noop, navigate: noop, evidence,
  })));
}

// A small HTML and selector reader: enough for the static markup and the pass stylesheet.
const voidTags = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr']);
function elements(html) {
  const root = {tag: 'root', classes: [], parent: null}, all = [];
  let current = root;
  for (const [, close, tag, attrs, selfClosing] of html.matchAll(/<(\/?)([a-zA-Z0-9]+)([^>]*?)(\/?)>/g)) {
    if (close) {while (current !== root && current.tag !== tag) current = current.parent; current = current.parent || root; continue;}
    const element = {tag, attrs, parent: current, classes: (attrs.match(/class="([^"]*)"/)?.[1] || '').split(/\s+/).filter(Boolean)};
    all.push(element);
    if (!selfClosing && !voidTags.has(tag)) current = element;
  }
  return all;
}
function expand(selector) {
  const group = selector.match(/:is\(([^()]*)\)/);
  return group ? group[1].split(',').flatMap(part => expand(selector.replace(group[0], part.trim()))) : [selector.trim()];
}
function compoundMatches(element, compound) {
  const tag = compound.match(/^[a-z]+/)?.[0];
  return (!tag || element.tag === tag) && [...compound.matchAll(/\.([\w-]+)/g)].every(([, name]) => element.classes.includes(name));
}
function matches(element, selector) {
  const parts = selector.replace(/\s*>\s*/g, ' > ').split(/\s+/);
  const match = (node, index) => {
    if (!compoundMatches(node, parts[index])) return false;
    if (index === 0) return true;
    if (parts[index - 1] === '>') return Boolean(node.parent) && match(node.parent, index - 2);
    for (let ancestor = node.parent; ancestor; ancestor = ancestor.parent) if (match(ancestor, index - 1)) return true;
    return false;
  };
  return match(element, parts.length - 1);
}
const pageCss = readFileSync(new URL('./workplan.css', import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const baseCss = pageCss.slice(0, pageCss.indexOf('@media'));
const rules = [...baseCss.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(([, selectors, body]) => ({
  selectors: selectors.split(/,(?![^()]*\))/).flatMap(expand),
  declarations: Object.fromEntries(body.split(';').filter(part => part.includes(':')).map(part => [part.slice(0, part.indexOf(':')).trim(), part.slice(part.indexOf(':') + 1).trim()])),
}));
const declared = (element, property) => rules.filter(rule => rule.declarations[property] && rule.selectors.some(selector => matches(element, selector))).map(rule => rule.declarations[property]).at(-1);
// Whole cards that are buttons: their size comes from the card, and the browser audit measures them.
const cardControls = ['step-header', 'progress-node', 'evidence-flow-node', 'flow-context'];

function checkControls(html, where) {
  const controls = elements(html).filter(element => ['button', 'a', 'summary'].includes(element.tag) && !cardControls.some(name => element.classes.includes(name)));
  assert.ok(controls.length > 0, where);
  for (const control of controls) {
    const name = control.classes.join('.') || control.parent.classes.join('.');
    const target = declared(control, 'min-height') || declared(control, 'height');
    assert.equal(target, '44px', `${where}: ${control.tag} ${name} has no 44px target`);
    const iconOnly = /aria-label=/.test(control.attrs);
    if (!iconOnly) assert.equal(declared(control, 'font-size'), '1rem', `${where}: ${control.tag} ${name} text is not 16px`);
  }
  return controls.length;
}

test('every Work plan and Evidence control has a 44px target and 16px text in each walkthrough state', () => {
  let count = 0;
  for (const [index, state] of states.entries()) {
    for (const step of [1, 2, 3, 4]) count += checkControls(workPlan(state, step), `Work plan state ${index} step ${step}`);
    count += checkControls(evidencePage(state), `Evidence state ${index}`);
  }
  for (const modal of [{type: 'source', e: evidence[0]}, {type: 'source', e: evidence[1]}, {type: 'coverage'}, {type: 'rider'}]) count += checkControls(dialog(modal), `${modal.type} dialog`);
  for (const modal of [{type: 'decision'}, {type: 'documentDetail'}]) count += checkControls(dialog(modal, states[3]), `${modal.type} dialog`);
  assert.ok(count > 100);
});

test('text links keep their row height on desktop by reaching into the space around them', () => {
  const html = workPlan(initialState);
  for (const name of ['version-help', 'help-link']) {
    const control = elements(html).find(element => element.classes.includes(name));
    assert.equal(declared(control, 'margin-block'), '-10px', name);
  }
  const icon = elements(html).find(element => element.classes.includes('icon-button') && element.parent.classes.includes('quote-meta'));
  assert.equal(declared(icon, 'width'), '44px');
  assert.equal(declared(icon, 'height'), '44px');
});

test('labels and captions are at least 14px and the page loads after the legacy layers', () => {
  const html = workPlan(states[1], 2) + evidencePage(states[1]);
  for (const name of ['step-status', 'mini-label', 'progress-sub', 'flow-eyebrow', 'flow-relation', 'section-kicker']) {
    const element = elements(html).find(node => node.classes.includes(name));
    assert.ok(element, name);
    assert.equal(declared(element, 'font-size'), '.875rem', name);
  }
  const main = readFileSync(new URL('./main.jsx', import.meta.url), 'utf8');
  assert.ok(main.indexOf("import './atelier.css';") < main.indexOf("import './workplan.css';"));
  assert.ok(main.indexOf("import './workplan.css';") < main.indexOf("import './chrome.css';"));
  assert.doesNotMatch(pageCss, /\.(sidebar|topbar|main-content|main-shell|page-heading|detail-nav|nav-item|brand)(?=[\s.:>{,#)])/);
});

test('authored meaning sits in a labelled workflow note, apart from the quotation', () => {
  const plan = workPlan(initialState), library = evidencePage(initialState), source = dialog({type: 'source', e: evidence[0]});
  for (const [html, where] of [[plan, 'Work plan'], [library, 'Evidence'], [source, 'source dialog']]) {
    const notes = [...html.matchAll(/<div class="workflow-note"><span class="note-label">Workflow note<\/span>([\s\S]*?)<\/div>/g)].map(match => text(match[1]));
    assert.ok(notes.some(note => note.includes(renewalEvidence[0].meaning)), where);
    // The meaning never sits inside a quotation or the old caption style.
    for (const quote of html.matchAll(/<blockquote[^>]*>([\s\S]*?)<\/blockquote>/g)) assert.ok(!text(quote[1]).includes(renewalEvidence[0].meaning), where);
    assert.ok(!html.includes(`>${renewalEvidence[0].meaning.toUpperCase()}<`), where);
  }
  assert.ok(text(library).includes(renewalEvidence[0].detail));
  assert.ok(!source.includes('HOW TO READ THIS'));
  assert.ok(source.includes('WORKFLOW CONTEXT'));
  assert.ok(!plan.includes('Seller statement only'));
});

test('a speaker role shows how it was established, and a role is not authority', () => {
  for (const html of [evidencePage(initialState), dialog({type: 'source', e: evidence[0]}), dialog({type: 'source', e: evidence[1]})]) {
    const visible = text(html);
    for (const unit of readinessFixture.card.units.filter(unit => unit.speaker)) {
      if (!visible.includes(unit.speaker.name)) continue;
      assert.ok(visible.includes(`Role ${unit.speaker.role}`));
      assert.ok(visible.includes(`Role basis ${unit.speaker.basis}`));
    }
    assert.ok(!html.includes(`<dt>Role basis</dt><dd>${renewalEvidence[0].role}</dd>`));
  }
  const library = text(evidencePage(initialState));
  assert.ok(library.includes('Customer decision authority Unidentified'));
  assert.ok(library.includes('Customer decision authority None (internal speaker)'));
  assert.ok(text(workPlan(initialState)).includes('Customer decision authority: unidentified.'));
  // A record without a card unit says its basis is not recorded, rather than repeating the label.
  const unknown = {...evidence[0], id: 'E-999'};
  assert.ok(text(evidencePage(initialState, [unknown])).includes('Role basis Not recorded.'));
});

test('a cut unit is labelled as cut and a whole sentence is not labelled partial', () => {
  for (const html of [workPlan(initialState), evidencePage(initialState), dialog({type: 'source', e: evidence[0]})]) {
    assert.ok(!html.includes('Partial excerpt'));
    assert.ok(!html.includes('evidence-cut'));
  }
  assert.ok(text(dialog({type: 'source', e: evidence[0]})).includes('The cited unit is a whole sentence'));
  const cut = evidence.map(record => ({...record, partial: true, partialReason: 'cut at a change of speaker'}));
  for (const html of [workPlan(initialState, 1, cut), evidencePage(initialState, cut)]) {
    assert.equal([...html.matchAll(/class="evidence-cut">Partial excerpt · cut at a change of speaker</g)].length, 2);
  }
  assert.ok(text(dialog({type: 'source', e: cut[0]})).includes('The cited unit is cut (Partial excerpt · cut at a change of speaker)'));
  assert.ok(evidencePage(initialState, [{...cut[0], partialReason: undefined}]).includes('Partial excerpt · cut boundary not recorded'));
});

test('Evidence, Sources checked and Readiness use the same source and gap wording', () => {
  const readiness = text(renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: [readinessHref('')]},
    React.createElement(Readiness, {journal: advanceReadinessJournal(null, initialState), onAction: noop}))));
  const coverage = dialog({type: 'coverage'}), library = evidencePage(initialState);
  for (const gap of renewalSourceBoundaries) {
    for (const html of [coverage, library]) {
      assert.ok(html.includes(`data-gap-code="${gap.code}"`));
      assert.ok(text(html).includes(`${gap.title} ${gap.text}`));
    }
    assert.ok(readiness.includes(gap.text));
  }
  for (const source of readinessFixture.card.sources) {
    assert.ok(coverage.includes(`data-source-id="${source.id}"`));
    assert.ok(text(coverage).includes(`${source.label} ${source.scope} Bounded local read`));
    assert.ok(readiness.includes(source.scope));
  }
  assert.ok(readiness.includes('Bounded local read'));
  assert.ok(!text(coverage).includes('Relevant excerpt'));
  assert.ok(text(coverage).includes('What was not read or not shown'));
  assert.ok(text(library).includes('What was not read or not shown'));
});

test('typed reviewer input is labelled wherever it appears and unknown authority is unidentified', () => {
  const resolved = states[3], approved = states[2];
  const decision = dialog({type: 'decision'}, resolved);
  assert.ok(decision.includes(`<span class="note-label">Decision text (reviewer input)</span><p>${walkthroughDecisions.step}</p>`));
  assert.ok(!decision.includes('class="full-quote"'));
  assert.ok(text(decision).includes('Customer decision authority Unidentified'));
  const resolve = renderToStaticMarkup(React.createElement(ReviewDecision, {type: 'resolve', state: approved, derived: derive(approved), onPerform: noop}));
  assert.ok(resolve.includes(`Decision text (reviewer input)</span><p>${walkthroughDecisions.step}</p>`));
  assert.ok(!resolve.includes('decision-evidence-quote'));
  assert.ok(text(resolve).includes('Customer decision authority: unidentified'));
  const receipt = text(workPlan(resolved, 4));
  assert.ok(receipt.includes(`Customer reviewer (reviewer input) ${walkthroughDecisions.reviewedBy}`));
  assert.ok(receipt.includes('Customer decision authority Unidentified'));
  // The flow names the awaited party as unidentified, then the typed reviewer as input.
  assert.ok(text(evidencePage(initialState)).includes('Customer counsel · unidentified'));
  assert.ok(text(evidencePage(approved)).includes(`${walkthroughDecisions.reviewedBy} · reviewer input`));
  const counsel = walk(allSteps.slice(0, 4));
  const request = renderToStaticMarkup(React.createElement(ReviewDecision, {type: 'counselRequest', state: counsel, derived: derive(counsel), onPerform: noop}));
  assert.ok(text(request).includes('decision authority unidentified'));
});
