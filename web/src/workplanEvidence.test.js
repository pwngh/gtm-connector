import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer} from 'vite';
import {MessageSquare, Phone} from 'lucide-react';
import {approvalLetters, fixturePeople, initialState, readinessEvidenceFixture, readinessFixture, renewalEvidence, renewalSourceBoundaries} from './renewalFixtures.js';
import {walkthroughCommands, walkthroughFields} from './customerDecision.js';
import {derive, transitionWalkthrough} from './workflow.js';
import {advanceReadinessJournal, customerAuthorityLabel, evaluateReadiness, readinessHref} from './readiness.js';

let server, WorkPlan, Evidence, Dialogs, ReviewDecision, Readiness, FollowUpReport, renewalEvidenceModule;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  WorkPlan = (await server.ssrLoadModule('/src/WorkPlan.jsx')).default;
  Evidence = (await server.ssrLoadModule('/src/Evidence.jsx')).default;
  Dialogs = (await server.ssrLoadModule('/src/WorkflowDialogContent.jsx')).default;
  ReviewDecision = (await server.ssrLoadModule('/src/ReviewDecision.jsx')).default;
  Readiness = (await server.ssrLoadModule('/src/Readiness.jsx')).default;
  FollowUpReport = (await server.ssrLoadModule('/src/FollowUpReport.jsx')).default;
  renewalEvidenceModule = await server.ssrLoadModule('/src/renewalEvidence.js');
});
after(async () => {await server?.close();});

const evidence = renewalEvidence.map(record => ({...record, icon: record.binding === 'customer_statement' ? Phone : MessageSquare}));
const noop = () => {};
const text = html => html.replace(/<[^>]+>/g, ' ').replaceAll('&amp;', '&').replaceAll('&#x27;', "'").replaceAll('&quot;', '"').replace(/\s+/g, ' ');

function walk(steps) {
  let state = initialState;
  for (const type of steps) state = transitionWalkthrough(state, {type, ...walkthroughFields(state, type)});
  return state;
}
const allSteps = walkthroughCommands;
const states = [initialState, walk(allSteps.slice(0, 3)), walk(allSteps.slice(0, -1)), walk(allSteps)];
const counselName = approvalLetters[0].signatory.name;

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
// Smoke checks only: one stylesheet, read in source order, without specificity or later
// stylesheets. The browser audit in scripts/audit-targets.mjs measures the rendered page.
const pageCss = readFileSync(new URL('./workplan.css', import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const baseCss = pageCss.slice(0, pageCss.indexOf('@media'));
const narrowCss = pageCss.slice(pageCss.indexOf('@media(max-width:650px)')).replace(/^[^{]*\{/, '').replace(/\}\s*$/, '');
const parseRules = css => [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(([, selectors, body]) => ({
  selectors: selectors.split(/,(?![^()]*\))/).flatMap(expand),
  declarations: Object.fromEntries(body.split(';').filter(part => part.includes(':')).map(part => [part.slice(0, part.indexOf(':')).trim(), part.slice(part.indexOf(':') + 1).trim()])),
}));
const rules = parseRules(baseCss), narrowRules = [...rules, ...parseRules(narrowCss)];
const declaredIn = (list, element, property) => list.filter(rule => rule.declarations[property] && rule.selectors.some(selector => matches(element, selector))).map(rule => rule.declarations[property]).at(-1);
const declared = (element, property) => declaredIn(rules, element, property);
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

test('smoke: every Work plan and Evidence control has a 44px target and 16px text in each walkthrough state', () => {
  let count = 0;
  for (const [index, state] of states.entries()) {
    for (const step of [1, 2, 3, 4]) count += checkControls(workPlan(state, step), `Work plan state ${index} step ${step}`);
    count += checkControls(evidencePage(state), `Evidence state ${index}`);
  }
  for (const modal of [{type: 'source', e: evidence[0]}, {type: 'source', e: evidence[1]}, {type: 'coverage'}, {type: 'rider'}]) count += checkControls(dialog(modal), `${modal.type} dialog`);
  for (const modal of [{type: 'decision'}, {type: 'documentDetail'}]) count += checkControls(dialog(modal, states[3]), `${modal.type} dialog`);
  assert.ok(count > 100);
});

test('smoke: text links keep their row height on desktop by reaching into the space around them', () => {
  const html = workPlan(initialState);
  // A 44px target that takes a 24px line: its block margins add up to -20px.
  for (const name of ['version-help', 'help-link']) {
    const control = elements(html).find(element => element.classes.includes(name));
    const [start, end = start] = String(declared(control, 'margin-block')).split(/\s+/).map(parseFloat);
    assert.equal(start + end, -20, name);
  }
  const icon = elements(html).find(element => element.classes.includes('icon-button') && element.parent.classes.includes('quote-meta'));
  assert.equal(declared(icon, 'width'), '44px');
  assert.equal(declared(icon, 'height'), '44px');
});

test('smoke: labels and captions are at least 14px and the page loads after the legacy layers', () => {
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
    const notes = [...html.matchAll(/<div class="workflow-note"><span class="note-label">Workflow note · applied for this walkthrough<\/span>([\s\S]*?)<\/div>/g)].map(match => text(match[1]));
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
  // Work plan previews carry the role, its basis and authority beside each quotation.
  const plan = text(workPlan(initialState));
  assert.ok(plan.includes('Role: Customer · Operations director (basis: synthetic contact match). Customer decision authority: unidentified.'));
  assert.ok(plan.includes('Role: Internal · Account owner (basis: synthetic principal match). Customer decision authority: none (internal speaker).'));
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
  // Work plan shows both quotations; Evidence shows both in the library and the seller quote in the map.
  for (const [html, count] of [[workPlan(initialState, 1, cut), 2], [evidencePage(initialState, cut), 3]]) {
    assert.equal([...html.matchAll(/class="evidence-cut">Partial excerpt · cut at a change of speaker</g)].length, count);
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

test('the recorded decision cites its receipt and names the contact from the authority record wherever it appears', () => {
  const resolved = states[3], approved = states[2], receiptId = approved.customerReview.evidenceRef.id;
  const decision = text(dialog({type: 'decision'}, resolved));
  assert.ok(decision.includes(`Cited evidence Receipt ${receiptId}, inspected signed letter`));
  assert.ok(decision.includes(`Approving contact ${counselName}`));
  assert.ok(decision.includes(`Customer decision authority ${counselName}`));
  assert.ok(!decision.includes('reviewer input'));
  assert.ok(!dialog({type: 'decision'}, resolved).includes('class="full-quote"'));
  const resolve = text(renderToStaticMarkup(React.createElement(ReviewDecision, {type: 'resolve', state: approved, derived: derive(approved), onPerform: noop})));
  assert.ok(resolve.includes(`Approving contact ${counselName}`));
  assert.ok(resolve.includes(`Cites receipt ${receiptId}`));
  assert.ok(!resolve.includes('Decision text'));
  const receipt = text(workPlan(resolved, 4));
  assert.ok(receipt.includes(`Approving contact ${counselName}`));
  assert.ok(receipt.includes(`Customer decision authority ${counselName}`));
  // The flow names the awaited party as unidentified, then the recorded contact with the letter it cites.
  assert.ok(text(evidencePage(initialState)).includes('Customer counsel · unidentified'));
  assert.ok(text(evidencePage(approved)).includes(`${counselName} · cites the inspected signed letter`));
  const operations = text(renderToStaticMarkup(React.createElement(MemoryRouter, {initialEntries: ['/operations?reason=condition&view=all']},
    React.createElement(FollowUpReport, {state: approved, derived: derive(approved), evidence, onTask: noop, onHistory: noop, onSource: noop, onDocument: noop, onDecision: noop, onScope: noop}))));
  assert.ok(operations.includes(`Approving contact (authority record): ${counselName} · Cites receipt ${receiptId}`));
});

test('the customer counsel stays unidentified until the authority record names them from the letter', () => {
  const counsel = walk(allSteps.slice(0, 4)), inspected = walk(allSteps.slice(0, allSteps.indexOf('record_customer_authority')));
  const request = renderToStaticMarkup(React.createElement(ReviewDecision, {type: 'counselRequest', state: counsel, derived: derive(counsel), onPerform: noop}));
  assert.ok(text(request).includes('For Customer counsel · unidentified Unidentified until a reviewer records who decides, with evidence'));
  for (const html of [request, dialog({type: 'context'}), dialog({type: 'context'}, counsel), dialog({type: 'context'}, inspected)]) {
    assert.ok(!html.includes(fixturePeople.customerCounsel));
    assert.ok(text(html).toLowerCase().includes('unidentified until a reviewer records who decides, with evidence'));
  }
  // The letter is received and accepted, yet nothing before the authority record shows its signatory.
  for (const html of [workPlan(inspected, 3), evidencePage(inspected), dialog({type: 'letterDetail'}, inspected), dialog({type: 'authority'}, inspected), dialog({type: 'letterInspect'}, walk(allSteps.slice(0, 6)))]) {
    assert.ok(!html.includes(counselName));
  }
  assert.ok(text(dialog({type: 'authority'}, inspected)).includes('The letter’s signatory'));
  const named = walk(allSteps.slice(0, allSteps.indexOf('record_customer_authority') + 1));
  assert.ok(text(dialog({type: 'context'}, named)).includes(`${counselName}, general counsel, decides for Agreement A-17. The authority record cites the inspected signed letter.`));
  assert.ok(text(workPlan(named, 3)).replaceAll(' ,', ',').includes(`Signed by ${counselName}, general counsel. Recorded as a contact from this letter’s receipt.`));
});

test('a resolution records the cited decision, not a verified outcome', () => {
  const resolved = states[3];
  const plan = text(workPlan(resolved, 4)), library = text(evidencePage(resolved));
  assert.ok(plan.includes('Condition resolved from the cited decision'));
  assert.doesNotMatch(plan, /resolved with (reviewed )?evidence/i);
  assert.ok(library.includes('RESOLUTION RECORDED Customer condition Recorded from the cited decision'));
  assert.ok(!library.includes('VERIFIED OUTCOME'));
  assert.ok(library.includes(`${counselName} · cites the inspected signed letter`));
  assert.ok(library.includes(`${counselName} is recorded as the contact who decides, from this receipt.`));
  // The toast for the resolve action.
  assert.ok(readFileSync(new URL('./ReviewDecision.jsx', import.meta.url), 'utf8').includes("message: 'Condition resolved from the cited decision'"));
});

test('the Evidence map shows the records the page shows, with their labels', () => {
  const shown = evidence.map(record => record.binding === 'comparison_statement' ? {...record, quote: 'We expect a signature soon.', partial: true, partialReason: 'cut at a change of speaker'} : record);
  const map = text(evidencePage(initialState, shown).match(/<section class="evidence-flow"[\s\S]*?<\/section>/)[0]);
  assert.ok(map.includes('“We expect a signature soon.” Partial excerpt · cut at a change of speaker'));
  assert.ok(!map.includes(renewalEvidence[1].quote));
  assert.ok(map.includes('Role: Internal · Account owner (basis: synthetic principal match). Customer decision authority: none (internal speaker).'));
  // The authored title on the customer node is labelled as a workflow note, not the unit's content.
  assert.ok(map.includes(`CUSTOMER · ${renewalEvidence[0].id} Workflow note Counsel approval required`));
});

test('the source dialog quotes the cited unit and labels the text around it as context', () => {
  for (const record of evidence) {
    const html = dialog({type: 'source', e: record});
    const quotes = [...html.matchAll(/<blockquote[^>]*>([\s\S]*?)<\/blockquote>/g)].map(match => text(match[1]).trim());
    assert.deepEqual(quotes, [`“${record.quote}”`]);
    const [before, after] = record.context.split(record.quote).map(part => part.trim());
    const excerpt = text(html.match(/<div class="source-excerpt">([\s\S]*?)<\/div>/)[1]);
    assert.ok(excerpt.startsWith(' Neighbouring text in the preserved excerpt'));
    assert.ok(!excerpt.includes(record.quote));
    if (before) assert.ok(excerpt.includes(`Before ${before}`));
    if (after) assert.ok(excerpt.includes(`After ${after}`));
  }
});

test('decision authority comes from the Readiness projection', () => {
  assert.equal(renewalEvidenceModule.decisionAuthorityLabel(), customerAuthorityLabel(readinessFixture));
  assert.equal(customerAuthorityLabel(readinessFixture), 'Unidentified');
  // With accepted authority evidence the same projection names the supported contact.
  const supported = customerAuthorityLabel(readinessEvidenceFixture);
  assert.equal(supported, readinessEvidenceFixture.contacts[0].displayName);
  const requirement = evaluateReadiness(initialState, readinessEvidenceFixture).requirements.find(item => item.binding === 'workflow_requirement');
  assert.equal(requirement.customerAuthorityLabel, supported);
  // The walkthrough's own authority record names the letter's signatory through the same projection.
  assert.equal(renewalEvidenceModule.decisionAuthorityLabel(states[2]), counselName);
  assert.equal(renewalEvidenceModule.decisionAuthorityLabel(states[1]), 'Unidentified');
});

const px = value => String(value).split(/\s+/).map(part => parseFloat(part) || 0);
test('smoke: the document gap buttons keep a positive gap between hit areas when they wrap or stack', () => {
  const all = elements(workPlan(initialState));
  const row = all.find(element => element.classes.includes('gap-actions'));
  const buttons = all.filter(element => element.tag === 'button' && element.parent === row);
  assert.equal(buttons.length, 2);
  for (const [list, where] of [[rules, 'wrapped'], [narrowRules, 'stacked at 650px and below']]) {
    const margins = button => {const [start, end = start] = px(declaredIn(list, button, 'margin-block') || '0'); return {start, end};};
    const space = px(declaredIn(list, row, 'gap'))[0] + margins(buttons[0]).end + margins(buttons[1]).start;
    assert.ok(space > 0, `${where}: the 44px hit areas are ${-space}px into each other`);
    for (const button of buttons) assert.equal(declaredIn(list, button, 'min-height'), '44px');
  }
  assert.equal(declaredIn(narrowRules, row, 'flex-direction'), 'column');
});

test('smoke: Task instructions keeps its disclosure marker and a 44px target', () => {
  const review = walk(['request_review']);
  const html = renderToStaticMarkup(React.createElement('section', {className: 'dialog'}, React.createElement(ReviewDecision, {type: 'review', state: review, derived: derive(review), onPerform: noop})));
  const summary = elements(html).find(element => element.tag === 'summary');
  assert.equal(declared(summary, 'display'), 'list-item');
  assert.equal(declared(summary, 'min-height'), '44px');
  assert.equal(declared(summary, 'list-style'), undefined);
});

test('smoke: the seller quotation in the Evidence map is 16px with a 14px eyebrow at every breakpoint', () => {
  const flowCss = readFileSync(new URL('./evidence-flow.css', import.meta.url), 'utf8');
  const quote = [...flowCss.matchAll(/\.flow-context-copy>strong\{[^}]*?font-size:([^;}]+)/g)].map(match => match[1]);
  const eyebrow = [...flowCss.matchAll(/\.flow-context-copy \.flow-eyebrow\{[^}]*?font-size:([^;}]+)/g)].map(match => match[1]);
  assert.ok(quote.length >= 3);
  assert.deepEqual([...new Set(quote)], ['1rem']);
  assert.deepEqual([...new Set(eyebrow)], ['.875rem']);
});
