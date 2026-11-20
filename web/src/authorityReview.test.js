import test, {before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync, readdirSync} from 'node:fs';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {derive, initialState, restoreWorkflowState, transition, workflowPolicyVersion} from './workflow.js';
import {customerDecisionSteps, decisionCitationOptions, walkthroughCommands} from './customerDecision.js';
import {walkthroughAction} from './testing/walkthroughInputs.js';
import {approvalLetters, historyFixture, readinessEvidenceExample, readinessFixture, walkthroughActors, walkthroughDocumentExamples} from './renewalFixtures.js';
import {evaluateEvidenceCheck, workflowEvidence} from './evidenceChecks.js';
import {buildOperations} from './operations.js';
import {buildWorklist} from './worklist.js';
import {evaluateReadiness} from './readiness.js';
import {buildPlaybookContext} from './playbookDrafts.js';
import {selectHistory} from './history.js';
import {ChromeHooks, find, nodes} from './testing/chromeHarness.js';

// Each test reproduces a reviewer's finding on f9ea0d6. They fail on that commit and pass here.

const {acquisitionId, reviewerId} = walkthroughActors;
const counsel = approvalLetters[0].signatory.name;
const run = (state, type, extra = {}) => transition(state, walkthroughAction(state, type, extra));
const through = last => {
  let state = initialState;
  for (const type of walkthroughCommands) {
    if (type === last) break;
    state = run(state, type);
    assert.equal(state.error, null, type);
  }
  return state;
};
const resolved = () => run(through('resolve_requirement'), 'resolve_requirement');
const letterId = state => derive(state).approvalLetter.id;
const decide = (state, extra) => run(state, 'record_customer_approval', extra);
const legacySaved = () => JSON.parse(readFileSync(new URL('./testing/legacyResolvedState.json', import.meta.url), 'utf8'));
const views = state => {
  const d = derive(state), ops = buildOperations(state).rows.find(row => row.id === 'condition');
  const queue = buildWorklist(state).requirements.find(item => item.id === state.requirement.id);
  const readiness = evaluateReadiness(state).requirements[0];
  return {derived: d.requirementStatus, operations: ops.status, queue: queue.status, readiness: readiness.status, waiting: ops.waitingOn};
};

test('blocker 1: none of the three source-card units can stand as the decision, in the reducer, the evaluator or the form', () => {
  const state = through('record_customer_approval'), contactId = derive(state).authorityContact.id;
  for (const n of ['1', '2', '3']) {
    const refused = decide(state, {evidenceRef: {kind: 'unit', cardId: readinessFixture.card.id, n}, contactId});
    assert.match(refused.error, /source unit cannot establish an approval/, `unit ${n}`);
    assert.equal(refused.customerReview.status, 'pending');
    // The evaluator refuses the same citation, even if a unit citation were recorded.
    const forged = {...state, customerReview: {...decide(state).customerReview, evidenceRef: {kind: 'unit', cardId: readinessFixture.card.id, n}}};
    assert.equal(derive(forged).evidenceChecks.decision_citation.result, 'failed');
    assert.equal(derive(forged).hasCustomerApproval, true, 'the record exists but its citation fails the check');
    assert.equal(derive(forged).canResolveRequirement, false);
  }
  assert.deepEqual(decisionCitationOptions(state).map(item => item.id), [letterId(state)]);
});

test('blocker 1: an unrelated inspected receipt, the document receipt and a source are refused as the decision', () => {
  const state = through('record_customer_approval'), contactId = derive(state).authorityContact.id;
  const unrelated = {id: 'receipt-call-notes-1', artifactId: 'artifact-call-notes', kind: 'note', status: 'received', receivedAt: state.document.receivedAt,
    checksum: 'synthetic:notes', inspections: [{status: 'accepted', at: state.document.receivedAt, reason: 'Legible.'}]};
  const withNote = {...state, evidence: {...state.evidence, artifactReceipts: [...state.evidence.artifactReceipts, unrelated]}};
  assert.match(decide(withNote, {evidenceRef: {kind: 'receipt', id: unrelated.id}, contactId}).error, /not a signed approval letter/);
  assert.match(decide(state, {evidenceRef: {kind: 'receipt', id: state.document.receiptId}, contactId}).error, /document receipt cannot record/);
  assert.match(decide(state, {evidenceRef: {kind: 'source', id: 'source-internal-slack'}, contactId}).error, /must cite the accepted signed letter/);
  assert.equal(decide(state).error, null);
});

test('blocker 2: a real pre-change resolved save reopens with a system reason and every view shows one open status', () => {
  const saved = legacySaved();
  assert.equal(saved.requirement.status, 'resolved');
  assert.equal(saved.customerReview.evidenceText, 'Counsel approved version 4 by email.');
  const state = restoreWorkflowState(saved);
  assert.equal(state.policyVersion, workflowPolicyVersion);
  assert.equal(state.requirement.status, 'open');
  assert.match(state.requirement.reopenReason, /saved under an earlier policy/);
  assert.equal(state.requirement.priorResolutions.length, 1);
  assert.equal(state.customerReview.status, 'needs_re_review');
  assert.equal(state.customerReview.reviewedBy, undefined, 'a typed name is not carried as the approver');
  const [retained] = state.customerReview.priorDecisions;
  assert.equal(retained.legacy, true);
  assert.equal(retained.current, false);
  assert.equal(retained.evidenceText, saved.customerReview.evidenceText);
  const event = state.history.at(-1);
  assert.equal(event.type, 'restore_policy');
  assert.equal(event.actor, 'Walkthrough rules (on restore)');
  assert.match(event.reason, /walkthrough-3/);
  assert.equal(state.history.length, saved.history.length + 1);
  const shown = views(state);
  assert.deepEqual([shown.derived, shown.queue, shown.readiness], ['open', 'open', 'open']);
  assert.notEqual(shown.operations, 'Resolved');
  assert.equal(shown.waiting, null);
  assert.ok(derive(state).nextCommand, 'the operator has a next step');
  // The operator can continue from the restored state to a supported resolution.
  let next = state;
  for (let i = 0; i < 12 && derive(next).nextCommand; i++) {
    const type = derive(next).nextCommand;
    next = run(next, type);
    assert.equal(next.error, null, type);
  }
  assert.equal(derive(next).requirementStatus, 'resolved');
  assert.deepEqual(Object.values(views(next)).slice(0, 4), ['resolved', 'Resolved', 'resolved', 'resolved']);
  assert.equal(next.customerReview.priorDecisions[0].legacy, true, 'the retained record survives the new decision');
});

test('blocker 2: a restored resolution whose recorder is its approver reopens instead of passing', () => {
  const complete = resolved();
  const {policyVersion, ...older} = JSON.parse(JSON.stringify(complete));
  const {authorityRef, recordedById, ...decision} = older.customerReview;
  older.customerReview = {...decision, recordedBy: counsel};
  older.evidence.artifactReceipts = older.evidence.artifactReceipts.map(({inspections, uploadedById, ...receipt}) => ({...receipt, inspection: inspections.at(-1) || null}));
  const state = restoreWorkflowState(older);
  assert.equal(state.requirement.status, 'open');
  assert.equal(state.customerReview.status, 'needs_re_review');
  assert.equal(derive(state).hasCustomerApproval, false);
  assert.equal(views(state).queue, 'open');
});

test('major 3: title and owner edits keep the decision and the requirement, before and after resolution', () => {
  for (const base of [through('resolve_requirement'), resolved()]) {
    for (const edit of [{title: 'Obtain the applicable AI terms'}, {ownerId: 'principal-portfolio-2'}]) {
      const edited = run(base, 'save_plan', edit);
      assert.equal(edited.error, null);
      assert.deepEqual(edited.customerReview, base.customerReview);
      assert.deepEqual(edited.requirement, base.requirement);
      assert.equal(edited.plan.status, 'draft');
      assert.equal(derive(edited).hasCustomerApproval, true);
      const reapproved = run(run(edited, 'request_review'), 'approve_plan');
      assert.equal(derive(reapproved).hasCustomerApproval, true);
      assert.equal(derive(reapproved).nextCommand, base.requirement.status === 'resolved' ? null : 'resolve_requirement');
    }
  }
});

test('major 3: a specifications edit marks the decision for re-review, keeps it, and reopens with a reason and an actor', () => {
  const base = resolved();
  assert.match(transition(base, {type: 'save_plan', specifications: 'Changed specifications.'}).error, /Name the principal editing the plan/);
  const edited = run(base, 'save_plan', {specifications: 'Changed specifications.'});
  assert.equal(edited.customerReview.status, 'needs_re_review');
  assert.equal(edited.customerReview.id, base.customerReview.id);
  assert.equal(edited.customerReview.reReview.actorId, acquisitionId);
  assert.match(edited.customerReview.reReview.reason, /specifications changed/);
  assert.equal(edited.requirement.status, 'open');
  assert.equal(edited.requirement.reopenedById, acquisitionId);
  const event = edited.history.at(-1);
  assert.equal(event.actor, 'Morgan Reed');
  assert.match(event.reason, /specifications changed/);
});

test('major 4: replacing the document after resolution keeps the decision as not current and reopens with a reason and an actor', () => {
  const base = resolved();
  const replaced = run(base, 'obtain_document', {document: walkthroughDocumentExamples.rescan});
  assert.equal(replaced.error, null);
  assert.equal(replaced.customerReview.status, 'needs_re_review');
  assert.equal(replaced.customerReview.evidenceRef.id, base.customerReview.evidenceRef.id);
  assert.match(replaced.customerReview.reReview.reason, /replaced the inspected document/);
  assert.equal(replaced.requirement.status, 'open');
  assert.equal(replaced.requirement.reopenedBy, 'Morgan Reed');
  assert.match(replaced.history.at(-1).reason, /replaced the inspected document/);
  assert.match(transition(base, {type: 'obtain_document', document: walkthroughDocumentExamples.rescan}).error, /Name the principal attaching the replacement/);
});

test('major 5: authority for v4 does not carry over to v5, and no view names the counsel for it', () => {
  const v5 = run(resolved(), 'obtain_document', {document: walkthroughDocumentExamples.replacement});
  const d = derive(v5);
  assert.equal(d.authorityContact, null);
  assert.match(d.evidenceChecks.customer_authority.explanation, /covers rider-v4 v4, not rider-v5 v5/);
  assert.match(run(v5, 'verify_document').error, /Agreement A-17 names version v4/);
  const rejected = run(v5, 'reject_document', {reason: 'Agreement A-17 names v4.'});
  assert.equal(rejected.error, null);
  for (const state of [v5, rejected]) {
    // The retained v4 decision keeps its approver; nothing names her as the authority or the awaited party for v5.
    const requirement = evaluateReadiness(state).requirements[0];
    const named = [buildOperations(state).rows.map(row => row.waitingOn), buildWorklist(state).workItems.map(item => item.waitingLabel),
      requirement.customerAuthorityLabel, requirement.waitingLabel, derive(state).customerApprovalStatus];
    assert.ok(!JSON.stringify(named).includes(counsel), 'the counsel is not named for a version the letter does not cover');
    assert.equal(requirement.customerAuthorityLabel, 'Unidentified');
    assert.equal(requirement.recordedDecision.current, false);
  }
});

test('major 6: the correction example marks the effective decision and resolution for review and shows the approver by contact', () => {
  const recorded = selectHistory(initialState, 'example=correction');
  const effective = selectHistory(initialState, 'example=correction&view=effective');
  const correction = effective.entries.find(entry => entry.id === 'example-correction');
  assert.match(correction.correctionNeedsReview, /contact-operations-director, which is not a recorded contact/);
  const resolution = effective.entries.find(entry => entry.id === 'example-resolution');
  assert.ok(resolution.correctionNeedsReview);
  const original = recorded.entries.find(entry => entry.id === 'example-customer-decision');
  assert.equal(original.values.find(value => value.field === 'decision.reviewer').display, `${counsel} · General counsel`);
  assert.equal(original.correctionNeedsReview, null);
  const approver = effective.entries.find(entry => entry.id === 'example-customer-decision').values.find(value => value.field === 'decision.reviewer');
  assert.equal(approver.display, 'contact-operations-director · not a recorded contact');
  const evidence = effective.entries.find(entry => entry.id === 'example-correction').impacts.evidenceChecks;
  assert.equal(evidence.find(check => check.id === 'decision_citation').result, 'failed');
  assert.match(historyFixture.examples[1].label, /not your walkthrough/);
});

test('major 6: withdrawing authority is a reducer command that marks the decision and reopens with a reason and an actor', () => {
  const base = resolved();
  assert.match(run(base, 'withdraw_customer_authority').error, /reason/);
  const withdrawn = run(base, 'withdraw_customer_authority', {reason: 'The signatory left the customer.'});
  const d = derive(withdrawn);
  assert.equal(d.evidenceChecks.customer_authority.result, 'failed');
  assert.equal(withdrawn.customerReview.status, 'needs_re_review');
  assert.equal(withdrawn.requirement.status, 'open');
  assert.equal(withdrawn.requirement.reopenedById, reviewerId);
  assert.match(withdrawn.requirement.reopenReason, /signatory left/);
  assert.equal(d.canRecordCustomerApproval, false);
  assert.match(decide(withdrawn, {evidenceRef: {kind: 'receipt', id: base.customerReview.evidenceRef.id}, contactId: base.customerReview.contactId}).error, /No usable signed approval letter/);
  // The withdrawn basis is never reused; a distinct letter restores the path.
  assert.equal(d.nextCommand, 'upload_approval_letter');
  let next = withdrawn;
  for (const type of customerDecisionSteps) next = run(next, type);
  assert.equal(next.error, null);
  assert.equal(next.evidence.customerAuthority.revision, '3');
  assert.equal(next.customerReview.evidenceRef.id, 'receipt-approval-letter-v4-resigned-1');
});

test('major 7: inspection and authority wait for plan approval like the upload, and refusals name the gate that failed', () => {
  const uploaded = through('inspect_approval_letter');
  const draft = run(uploaded, 'save_plan', {title: 'A new title'});
  assert.match(run(draft, 'inspect_approval_letter').error, /plan revision needs internal approval/);
  const accepted = run(uploaded, 'inspect_approval_letter');
  assert.match(run(accepted, 'inspect_approval_letter').error, /already recorded for this letter/);
  assert.match(run(run(accepted, 'save_plan', {title: 'Another title'}), 'record_customer_authority').error, /plan revision needs internal approval/);
});

test('major 7: a restored accepted receipt with an unknown artifact is refused by derive and transition alike', () => {
  const accepted = run(through('inspect_approval_letter'), 'inspect_approval_letter');
  const forged = {...accepted, evidence: {...accepted.evidence, artifactReceipts: accepted.evidence.artifactReceipts.map(receipt => receipt.kind === 'approval_letter' ? {...receipt, artifactId: 'artifact-unknown'} : receipt)}};
  const d = derive(forged);
  assert.equal(d.canRecordCustomerAuthority, false);
  assert.match(d.transitions.record_customer_authority.refusal, /artifact this walkthrough does not have/);
  assert.doesNotThrow(() => run(forged, 'record_customer_authority'));
  assert.match(run(forged, 'record_customer_authority').error, /artifact this walkthrough does not have/);
});

test('major 7: an outdated document can be rejected from the Work queue, not only replaced', () => {
  const outdated = run(through('obtain_document'), 'obtain_document', {document: walkthroughDocumentExamples.outdated});
  const d = derive(outdated);
  assert.equal(d.canVerifyDocument, false);
  assert.equal(d.canRejectDocument, true);
  const inspection = buildWorklist(outdated).workItems.find(item => item.kind === 'inspection');
  assert.equal(inspection.status, 'ready');
  assert.equal(inspection.workflowAction.command, 'reject_document');
});

test('major 8: separation of duties is compared by principal ID for the inspector, authority recorder and decision recorder', () => {
  const uploaded = through('inspect_approval_letter');
  assert.match(run(uploaded, 'inspect_approval_letter', {actorId: acquisitionId}).error, /plan owner cannot be the principal who inspects/);
  const otherUploader = run(through('upload_approval_letter'), 'upload_approval_letter', {actorId: reviewerId});
  assert.match(run(otherUploader, 'inspect_approval_letter', {actorId: reviewerId}).error, /uploaded receipt .* cannot be the principal who inspects/);
  assert.match(transition(uploaded, {type: 'inspect_approval_letter', outcome: 'accepted', reason: 'Signed.', actor: ' helen okafor '}).error, /Name the principal/);
  assert.match(transition(uploaded, {type: 'inspect_approval_letter', outcome: 'accepted', reason: 'Signed.', actor: 'Alex Chen'}).error, /Name the principal/);
  const accepted = run(uploaded, 'inspect_approval_letter');
  assert.match(run(accepted, 'record_customer_authority', {actorId: acquisitionId}).error, /plan owner cannot/);
  assert.match(run(accepted, 'record_customer_authority', {actorId: 'contact-general-counsel'}).error, /Name the principal/);
  const recorded = run(accepted, 'record_customer_authority');
  assert.match(decide(recorded, {actorId: acquisitionId}).error, /plan owner cannot be the principal who records the decision/);
  assert.match(decide(recorded, {actorId: undefined, actor: counsel}).error, /Name the principal/);
  const chosen = run(recorded, 'record_customer_approval');
  assert.equal(chosen.customerReview.recordedById, reviewerId);
  assert.notEqual(chosen.customerReview.recordedById, chosen.customerReview.contactId);
  assert.deepEqual(chosen.customerReview.authorityRef, {id: recorded.evidence.customerAuthority.id, revision: recorded.evidence.customerAuthority.revision});
});

test('inspections are append-only with typed reasons, and a rejected file never returns as its own replacement', () => {
  const uploaded = through('inspect_approval_letter');
  assert.match(run(uploaded, 'inspect_approval_letter', {reason: ' '}).error, /typed reason for accepting/);
  const rejected = run(uploaded, 'inspect_approval_letter', {outcome: 'rejected', reason: 'Unsigned.'});
  assert.match(run(rejected, 'upload_approval_letter', {artifactId: approvalLetters[0].artifactId}).error, /rejected at inspection \(same checksum\)/);
  const replacement = run(rejected, 'upload_approval_letter');
  assert.equal(replacement.evidence.artifactReceipts.at(-1).artifactId, approvalLetters[1].artifactId);
  const letters = replacement.evidence.artifactReceipts.filter(receipt => receipt.kind === 'approval_letter');
  assert.deepEqual(letters.map(receipt => receipt.inspections.map(item => item.reason)), [['Unsigned.'], []]);
  // The same artifact and version with a different checksum gets its own receipt, never a silent drop.
  const doc = through('verify_document');
  const second = run(doc, 'obtain_document', {document: {...walkthroughDocumentExamples.rescan, id: doc.document.id}});
  assert.equal(second.evidence.artifactReceipts.filter(receipt => receipt.kind === 'document').length, 2);
  assert.match(run(doc, 'obtain_document', {document: {id: doc.document.id, version: doc.document.version, checksum: doc.document.checksum}}).error, /already received as receipt/);
  // A specifications edit then re-inspection appends a second inspection to the same receipt.
  const reinspected = run(run(run(run(resolved(), 'save_plan', {specifications: 'Changed.'}), 'request_review'), 'approve_plan'), 'verify_document');
  const receipt = reinspected.evidence.artifactReceipts.find(item => item.id === reinspected.document.receiptId);
  assert.equal(receipt.inspections.length, 2);
});

test('the version basis comes from the agreement record, never from the letter or the document', () => {
  const accepted = run(through('inspect_approval_letter'), 'inspect_approval_letter');
  assert.equal(accepted.evidence.documentVersionBasis, undefined);
  const check = derive(accepted).evidenceChecks.current_version_basis;
  assert.equal(check.result, 'passed');
  const letterBasis = {...readinessFixture, documentVersionBasis: {...readinessFixture.documentVersionBasis, source: {kind: 'receipt', id: letterId(accepted)}}};
  assert.match(evaluateEvidenceCheck('current_version_basis', accepted, workflowEvidence(accepted, letterBasis)).explanation, /receipt cannot establish the applicable version/);
  assert.equal(derive(accepted).documentStatus, 'Accepted at inspection');
});

test('the walkthrough’s own records win over a fixture or retained context with the same kind of record', () => {
  const state = resolved();
  const conflicting = {...readinessFixture, customerAuthority: readinessEvidenceExample.customerAuthority, decisionReceipt: readinessEvidenceExample.decisionReceipt,
    contacts: [{...readinessEvidenceExample.contact, id: 'contact-general-counsel', displayName: 'Someone else'}]};
  const context = workflowEvidence(state, conflicting);
  assert.equal(context.customerAuthority, state.evidence.customerAuthority);
  assert.equal(context.decisionReceipt.id, state.customerReview.id);
  assert.equal(context.contacts.find(item => item.id === 'contact-general-counsel').displayName, counsel);
});

test('major 9: no second counsel name is offered, and the example contact is labelled as an invented example', () => {
  const sources = readdirSync(new URL('.', import.meta.url)).filter(name => /\.(js|jsx)$/.test(name) && !name.endsWith('.test.js'));
  for (const name of sources) assert.ok(!readFileSync(new URL(name, import.meta.url), 'utf8').includes('Nina Patel'), name);
  assert.deepEqual(buildPlaybookContext(initialState).roleOptions.customerAuthorityOptions, []);
  assert.deepEqual(buildPlaybookContext(resolved()).roleOptions.customerAuthorityOptions, [counsel]);
  assert.match(readinessEvidenceExample.contact.exampleLabel, /Invented example contact.*Not the walkthrough’s counsel/);
  assert.notEqual(readinessEvidenceExample.contact.displayName, counsel);
});

let server, CustomerEvidence, HookedEvidence, ResolutionReceipt, WorkPlan, ContextPanels;
const hookNames = ['useState', 'useId', 'useRef', 'useEffect', 'useMemo'];
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error', plugins: [{
    name: 'inspection-hooks', enforce: 'pre',
    resolveId(id) {if (id === 'virtual:inspection-react') return '\0' + id;},
    load(id) {if (id === '\0virtual:inspection-react') return 'export * from \'react\'; import React from \'react\'; export default React; ' + hookNames.map(name => `export const ${name}=(...args)=>globalThis.__chromeHooks.${name}(...args);`).join('\n');},
    // A second copy of the dialog module runs its hooks on the test harness; the plain copy renders with React.
    transform(code, id) {if (id.endsWith('/src/CustomerEvidence.jsx?hooks')) return code.replace("from 'react';", "from 'virtual:inspection-react';");},
  }]});
  CustomerEvidence = await server.ssrLoadModule('/src/CustomerEvidence.jsx');
  HookedEvidence = await server.ssrLoadModule('/src/CustomerEvidence.jsx?hooks');
  ResolutionReceipt = (await server.ssrLoadModule('/src/ResolutionReceipt.jsx')).default;
  WorkPlan = (await server.ssrLoadModule('/src/WorkPlan.jsx')).default;
  ContextPanels = await server.ssrLoadModule('/src/ContextPanels.jsx');
});
after(async () => {await server?.close();});
const textOf = html => html.replace(/<[^>]+>/g, ' ').replaceAll('&amp;', '&').replaceAll('&#x27;', '\'').replace(/\s+/g, ' ');

test('minor 10: the resolution receipt shows the citation and a UTC time; a unit citation is shown, not hidden', () => {
  const state = resolved();
  const html = textOf(renderToStaticMarkup(React.createElement(ResolutionReceipt, {state, onDecision() {}, onHistory() {}})));
  assert.match(html, new RegExp(`Cited evidence Receipt ${state.customerReview.evidenceRef.id}, accepted signed letter`));
  assert.match(html, /Resolved 19 Nov 2026, 16:\d\d UTC/);
  assert.match(html, new RegExp(`Approving contact ${counsel} · General counsel`));
  const unit = {...state, customerReview: {...state.customerReview, status: 'needs_re_review', evidenceRef: {kind: 'unit', cardId: readinessFixture.card.id, n: '1'}}};
  const panel = textOf(renderToStaticMarkup(React.createElement(ContextPanels.DecisionEvidence, {state: unit, onHistory() {}})));
  assert.match(panel, /Cited evidence Card card-thornfield-walkthrough · Unit 1/);
  assert.match(panel, /NEEDS RE-REVIEW/);
});

test('minor 11: step 3 says the letter can be added while the upload is allowed', () => {
  const state = through('upload_approval_letter');
  const html = textOf(renderToStaticMarkup(React.createElement(WorkPlan, {state, derived: derive(state), evidence: [{...readinessFixture.card.units[0], id: 'E-017', type: 'Customer call', speaker: 'Rachel Ellis', time: 'Nov 18 · 04:12', quote: 'q', meaning: 'm'}, {id: 'E-042', type: 'Internal Slack', speaker: 'Morgan Reed', time: 'Nov 19 · 09:24', quote: 'q', meaning: 'm'}],
    selectedStep: 3, setSelectedStep() {}, openStep() {}, openWorklistAction() {}, openCustomerDecision() {}, openEdit() {}, setModal() {}, navigate() {}, source() {}, action: {label: 'Add the signed letter', fn() {}}})));
  assert.match(html, /Ready to add the signed letter/);
  assert.doesNotMatch(html, /Awaiting the signed letter/);
});

test('minor 12: the inspection dialog shows the letter text by keyboard and needs a typed reason for either outcome', () => {
  const state = through('inspect_approval_letter'), d = derive(state);
  const html = renderToStaticMarkup(React.createElement(CustomerEvidence.LetterInspection, {state, derived: d, onCancel() {}, onPerform() {}}));
  assert.match(html, /<section class="letter-text" tabindex="0" aria-labelledby="[^"]+"><span class="mini-label" id="[^"]+">LETTER TEXT · SYNTHETIC EXCERPT/);
  assert.match(textOf(html), new RegExp(`Signed: ${counsel}, General counsel`));
  assert.match(html, /<dl class="receipt-meta"><div><dt>Receipt<\/dt><dd>receipt-approval-letter-v4-1<\/dd><\/div>/);
  const order = [...html.matchAll(/<(textarea|button)[^>]*>([^<]*)/g)].map(match => match[1] === 'textarea' ? 'reason' : match[2]);
  assert.deepEqual(order, ['reason', 'Cancel', 'Reject the letter', 'Accept the letter']);
  assert.match(html, /<button type="button" class="button" disabled="">Reject the letter/);
  assert.match(html, /<button type="submit" class="button primary" disabled="">Accept the letter/);
});

test('minor 12: typing a reason enables reject and accept, Cancel leaves without recording, and nothing is filled in', () => {
  const state = through('inspect_approval_letter'), d = derive(state), calls = [];
  const hooks = new ChromeHooks('/renewals/thornfield/workplan');
  const props = {state, derived: d, onCancel: () => calls.push(['cancel']), onPerform: (...args) => calls.push(args)};
  const render = () => {const tree = hooks.render(HookedEvidence.LetterInspection, props); hooks.flush(); return tree;};
  const button = (tree, label) => [...nodes(tree)].find(node => node.type === 'button' && [].concat(node.props.children).join('') === label);
  let tree = render();
  assert.equal(button(tree, 'Reject the letter').props.disabled, true);
  find(tree, node => node.type === 'textarea').props.onChange({target: {value: '  Unsigned copy.  '}});
  tree = render();
  assert.equal(button(tree, 'Reject the letter').props.disabled, false);
  assert.equal(button(tree, 'Accept the letter').props.disabled, false);
  button(tree, 'Cancel').props.onClick();
  assert.deepEqual(calls, [['cancel']]);
  button(tree, 'Reject the letter').props.onClick();
  assert.deepEqual(calls.at(-1), ['inspect_approval_letter', {outcome: 'rejected', reason: 'Unsigned copy.'}, 'Letter rejected. Its receipt is kept.']);
  hooks.dispose();
});
