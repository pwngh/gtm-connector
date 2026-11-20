// Reusable proposals, not approved work or executable authority.
const input = (ref, label, mode = 'required') => ({ref, label, mode});
const dependencies = (all = [], oneOf = [], excludes = []) => ({all, oneOf, excludes});
const proof = (kind, ref, description, extra = {}) => ({kind, ref, description, ...extra});
const item = (id, title, role, requires, inputs, output, completionProof, kind = 'internal_task') => ({
  id, title, role, kind, dependencies: requires, inputs, output, completionProof,
});
const agreement = input('agreement', 'The renewal agreement');
const customerStatement = input('customer_statement', 'Customer condition statement');
const comparisonStatement = input('comparison_statement', 'Comparison statement');
const document = input('current_document', 'Applicable AI terms document', 'allowed');
const versionBasis = input('current_version_basis', 'Separate source for the current version', 'allowed');
const authority = input('customer_authority', 'Evidence of who decides for the customer', 'allowed');

function additions(sourceId) {
  return [
    {...item('assign_owner', 'Identify an accountable action owner', 'internalReviewer', dependencies(),
      [agreement, input(sourceId, 'Proposed work scope')],
      'A proposed action owner with responsibility for this work.',
      proof('attestation', 'owner_assignment', 'A labelled reviewer attestation of the proposed responsibility, not customer authority.')),
      addedBecauseOf: sourceId, when: 'owner_missing'},
    {...item('authorize_scope', 'Prepare a separate scope review', 'internalReviewer', dependencies(['assign_owner']),
      [input('draft_revision', 'This draft revision, specifications, and instructions'), agreement],
      'A scope-review proposal for an independent internal reviewer. No approval is recorded here.',
      proof('attestation', 'scope_review', 'A separate, labelled internal review of this exact revision would be needed before release. Existing plan approval is not inherited.')),
      addedBecauseOf: sourceId, when: 'always'},
  ];
}

function freeze(value) {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(freeze);
    Object.freeze(value);
  }
  return value;
}

export const playbookTemplates = freeze([
  {
    id: 'obtain_current_rider', version: '1.1',
    title: 'Obtain the current document',
    summary: 'Locate the applicable AI terms on our side, inspect their version, and prepare them for sending.',
    whenToUse: 'The customer has asked us to send the applicable AI terms. Locate the file on our side and establish which version applies. A rider is an additional document that adds or changes terms.',
    draftTitle: 'Prepare the applicable AI terms for the renewal agreement',
    inputs: [agreement, customerStatement, document, versionBasis, authority],
    requiredEvidence: ['customer_statement'],
    evidence: ['customer_statement', 'current_document', 'current_version_basis', 'customer_authority'],
    recoveryTasks: {customer_statement: 'preserve_condition', current_document: 'locate_document', current_version_basis: 'verify_document', customer_authority: 'confirm_authority'},
    specifications: 'Identify the AI terms applicable to the renewal agreement. Retain the file identity, source, receipt time, version, and a separate source for which version is current.',
    instructions: 'Preserve the customer condition and request in context. Locate the applicable version in our records or through our internal document owner. Inspect the agreement and version basis, then prepare the file for sending to the customer. Do not ask the customer for the file they asked us to send. Nothing is sent from this draft.',
    output: {id: 'document_handoff_packet', title: 'An inspected-document handoff packet', evidence: ['current_document', 'current_version_basis', 'customer_authority'], description: 'The applicable file, its provenance, a separately sourced version check, and a supported customer review route. This packet enables review; it is not customer approval.'},
    completionRules: [
      {id: 'document_provenance', text: 'Document collection completes on receipt of the applicable file with its source, version, and receipt time. Inspection is separate work.'},
      {id: 'current_version_check', text: 'The current version has a separate source. A document cannot establish currency by its own assertion.'},
      {id: 'handoff_inspection', text: 'An independent internal reviewer checks the packet and customer route. Customer approval remains a separate, evidence-backed decision about the document and agreement.'},
    ],
    reviewBoundary: 'These items only propose collection, inspection, and an unsent handoff. They grant no authority, record no customer decision, resolve no condition, and do not confirm a renewal.',
    automaticItems: additions('locate_document'),
    items: [
      item('preserve_condition', 'Recover and preserve the customer statement', 'actionOwner', dependencies(),
        [agreement, input('customer_statement', 'Original customer condition and request, if available', 'allowed')],
        'The original customer statement with its speaker, time, source, and context.',
        proof('unit', 'customer_statement', 'Cite the original statement and its source context. If absent, recover the source before assuming which document the customer requested.', {requiresContext: true})),
      item('locate_document', 'Locate the applicable AI terms on our side', 'actionOwner', dependencies(['preserve_condition', 'authorize_scope']),
        [agreement, customerStatement, input('internal_document_source', 'Our records or internal document owner', 'allowed')],
        'A received file from our records, with source, version, and receipt time.',
        proof('receipt', 'current_document', 'Keep a receipt for the file and its provenance. If it cannot be located, record the gap without claiming receipt.', {inspection: 'not_required'})),
      item('verify_document', 'Inspect agreement and current-version basis', 'actionOwner', dependencies(['locate_document']),
        [input('current_document', 'Received file and receipt'), input('current_version_basis', 'Separate evidence of the applicable current version'), agreement],
        'A recorded inspection against the agreement and an independently sourced version fact.',
        proof('receipt', 'current_document', 'Accept inspection only when identity, agreement, and the separately sourced current version match. Keep older or unknown versions open.', {inspection: 'accepted', versionBasisRef: 'current_version_basis'})),
      item('confirm_authority', 'Record who decides, with evidence', 'actionOwner', dependencies(['preserve_condition', 'authorize_scope']),
        [customerStatement, authority],
        'An evidence-backed customer decision route, or an explicit unidentified decision owner.',
        proof('unit', 'customer_authority', 'Cite who can decide and their authority for this agreement. A fixture name or job title alone is not an authority basis.'), 'customer_request'),
      item('prepare_handoff', 'Prepare the applicable file for sending', 'actionOwner', dependencies(['verify_document', 'confirm_authority']),
        [input('current_document', 'Inspected document receipt'), input('customer_authority', 'Supported customer review route')],
        'An unsent customer handoff with the exact file version, agreement, and requested decision.',
        proof('attestation', 'prepared_handoff', 'A labelled internal attestation that the packet is prepared. Preparation is not sending.'), 'customer_request'),
      item('inspect_output', 'Inspect the collection packet', 'internalReviewer', dependencies(['prepare_handoff']),
        [input('document_handoff_packet', 'Document handoff packet'), input('draft_revision', 'Collection specifications and completion rules')],
        'An independent internal inspection note with remaining gaps.',
        proof('attestation', 'packet_inspection', 'A labelled reviewer attestation checks the packet, not customer approval. The owner cannot review their own draft.')),
    ],
  },
  {
    id: 'review_renewal_condition', version: '1.1',
    title: 'Review a customer condition',
    summary: 'Preserve the customer prerequisite and prepare a supported condition assessment.',
    whenToUse: 'A customer has said what must be true before they can proceed. Clarify the exact condition, the evidence it needs, and who can decide, without treating a seller expectation as approval.',
    draftTitle: 'Assess the counsel-approval condition for the renewal agreement',
    inputs: [agreement, customerStatement, document, versionBasis, authority],
    requiredEvidence: ['customer_statement'],
    evidence: ['customer_statement', 'current_document', 'current_version_basis', 'customer_authority'],
    recoveryTasks: {customer_statement: 'preserve_condition', current_document: 'assess_prerequisites', current_version_basis: 'assess_prerequisites', customer_authority: 'confirm_authority'},
    specifications: 'Preserve the exact customer statement, its qualifiers, source, and agreement. Distinguish the requirement, available evidence, missing prerequisites, and the customer decision still needed.',
    instructions: 'Read the customer condition statement in context. Keep the action owner separate from the customer decision authority. If the file, its current-version basis, or authority is unknown, prepare a bounded evidence request rather than asserting that the condition is satisfied.',
    output: {id: 'condition_assessment', title: 'A condition assessment and review brief', evidence: ['customer_authority'], description: 'The exact prerequisite, an evidence checklist, the decision authority or its unresolved gap, and an owned next step. An assessment may conclude that more evidence is needed.'},
    completionRules: [
      {id: 'exact_condition', text: 'The original condition and qualifiers remain linked to their source and the renewal agreement.'},
      {id: 'bounded_gaps', text: 'Every missing prerequisite has a specific information request and responsible role. Unknowns remain explicit.'},
      {id: 'condition_review', text: 'An independent internal reviewer checks the assessment. Customer approval requires evidence bound to the applicable document and agreement, not this draft revision.'},
    ],
    reviewBoundary: 'An internally reviewed brief is not a customer decision, a resolved requirement, or a signed renewal. Editing this draft never changes an existing decision or approved work.',
    automaticItems: additions('assess_prerequisites'),
    items: [
      item('preserve_condition', 'Preserve the exact customer condition', 'actionOwner', dependencies(),
        [input('customer_statement', 'Customer statement and context, if available', 'allowed'), input('requirement', 'The renewal requirement')],
        'A sourced condition record retaining all qualifiers.',
        proof('unit', 'customer_statement', 'Retain the exact statement with speaker, time, source, and context. Acquire a missing original before interpreting it.', {requiresContext: true})),
      item('assess_prerequisites', 'Assess evidence and missing prerequisites', 'actionOwner', dependencies(['preserve_condition', 'authorize_scope']),
        [customerStatement, document, versionBasis],
        'An evidence checklist with bounded acquisition requests for gaps.',
        proof('attestation', 'evidence_checklist', 'A labelled internal assessment separates missing, received, and inspected evidence, with a next action for every gap.')),
      item('confirm_authority', 'Record who decides, with evidence', 'actionOwner', dependencies(['preserve_condition', 'authorize_scope']),
        [customerStatement, authority],
        'A supported customer decision route, with unresolved authority questions retained.',
        proof('unit', 'customer_authority', 'Cite the authority basis or leave the decision owner unidentified. Do not infer authority from a contact name or job title.'), 'customer_request'),
      item('prepare_condition_brief', 'Prepare the condition review brief', 'actionOwner', dependencies(['assess_prerequisites', 'confirm_authority']),
        [customerStatement, input('evidence_checklist', 'Evidence checklist and authority questions')],
        'An assessment of supported facts, unmet prerequisites, and the next customer decision needed.',
        proof('attestation', 'condition_assessment', 'A labelled internal assessment explains known facts and gaps. Missing evidence may support an information-needed assessment, not an approval.')),
      item('inspect_output', 'Inspect the condition assessment', 'internalReviewer', dependencies(['prepare_condition_brief']),
        [input('condition_assessment', 'Condition assessment'), customerStatement],
        'An independent internal review note and owned next step.',
        proof('attestation', 'condition_review', 'A labelled reviewer attestation checks that the assessment preserves the source and does not substitute internal review for a customer decision.')),
    ],
  },
  {
    id: 'reconcile_approval_conflict', version: '1.1',
    title: 'Reconcile conflicting approval',
    summary: 'Compare claims, versions, and authority before proposing a reconciliation.',
    whenToUse: 'Statements disagree, or appear to, about approval. First establish whether they concern the same decision and document. A newer statement is not automatically more authoritative.',
    draftTitle: 'Compare the approval claims for the renewal agreement',
    inputs: [agreement, customerStatement, comparisonStatement, document, versionBasis, authority],
    requiredEvidence: ['customer_statement', 'comparison_statement'],
    evidence: ['customer_statement', 'comparison_statement', 'current_document', 'current_version_basis', 'customer_authority'],
    recoveryTasks: {customer_statement: 'assemble_statements', comparison_statement: 'assemble_statements', current_document: 'compare_claims', current_version_basis: 'compare_claims', customer_authority: 'confirm_authority'},
    specifications: 'Keep both original statements intact. Compare authors, roles, timestamps, decision scope, and document versions. Distinguish a genuine contradiction from statements about different things.',
    instructions: 'Compare the customer condition statement with the comparison statement. A seller forecast is not a second approval. Obtain a targeted clarification or record a sourced no-conflict finding. Retain unresolved claims with an owner rather than choosing by recency.',
    output: {id: 'reconciliation_record', title: 'A sourced reconciliation proposal', evidence: ['clarification_statement', 'no_conflict_finding', 'customer_authority'], description: 'A statement comparison and proposed disposition: different claims, superseded with evidence, or unresolved. Both originals and remaining questions stay visible.'},
    completionRules: [
      {id: 'both_originals', text: 'Both statements retain their original identities, wording, authors, and timestamps.'},
      {id: 'scope_comparison', text: 'The comparison addresses authority, agreement, decision scope, and document version without filling gaps by assumption.'},
      {id: 'supported_disposition', text: 'An independent reviewer checks the proposal and either clarification evidence or a sourced no-conflict finding. Any unresolved claim keeps its owned next step.'},
    ],
    reviewBoundary: 'A reconciliation proposal does not create or revoke customer approval. Only a supported customer decision on the applicable document and agreement could inform a later, separately gated resolution.',
    automaticItems: additions('compare_claims'),
    items: [
      item('assemble_statements', 'Preserve both statements and their context', 'actionOwner', dependencies(),
        [input('customer_statement', 'Customer condition statement, if available', 'allowed'), input('comparison_statement', 'Comparison statement, if available', 'allowed')],
        'A two-source statement ledger, with missing originals explicitly requested.',
        proof('unit', 'customer_statement', 'Retain both originals with author, role, time, and context. Acquire missing statements instead of inventing a conflict.', {alsoRefs: ['comparison_statement'], requiresContext: true})),
      item('compare_claims', 'Compare decision scope and document versions', 'actionOwner', dependencies(['assemble_statements', 'authorize_scope']),
        [customerStatement, comparisonStatement, agreement, document, versionBasis],
        'A comparison of what each statement establishes and any missing version facts.',
        proof('attestation', 'claim_comparison', 'A labelled internal assessment checks authority, decision, and version, and identifies internal acquisition work for missing document facts.')),
      item('confirm_authority', 'Record who decides, with evidence', 'actionOwner', dependencies(['compare_claims']),
        [input('claim_comparison', 'Claim comparison'), authority],
        'Evidence-backed authority distinctions and a route for clarification.',
        proof('unit', 'customer_authority', 'Separate seller expectation, internal review, and customer authority. Record who decides, with evidence, or leave that question open.'), 'customer_request'),
      item('seek_clarification', 'Obtain a targeted customer clarification', 'actionOwner', dependencies(['compare_claims', 'confirm_authority'], [], ['record_no_conflict']),
        [input('claim_comparison', 'Unresolved scope, version, or authority questions')],
        'A sourced clarification response. An unsent or unanswered request leaves this item open.',
        proof('unit', 'clarification_statement', 'Cite the response to the specific question. Drafting a request does not establish an answer.'), 'customer_request'),
      item('record_no_conflict', 'Record a sourced no-conflict finding', 'actionOwner', dependencies(['compare_claims', 'confirm_authority'], [], ['seek_clarification']),
        [customerStatement, comparisonStatement, input('claim_comparison', 'Decision-scope comparison')],
        'A supported finding that the sources concern different claims, not contradictory approvals.',
        proof('attestation', 'no_conflict_finding', 'A labelled internal finding cites both originals and explains why they do not conflict. It does not record approval.', {cites: ['customer_statement', 'comparison_statement']})),
      item('draft_reconciliation', 'Propose a supported disposition', 'actionOwner', dependencies(['compare_claims', 'confirm_authority'], [['seek_clarification', 'record_no_conflict']]),
        [customerStatement, comparisonStatement, input('clarification_statement', 'Clarification response', 'allowed'), input('no_conflict_finding', 'Sourced no-conflict finding', 'allowed')],
        'A proposed disposition with evidence and remaining questions.',
        proof('attestation', 'reconciliation_record', 'A labelled proposal uses different claims, superseded with evidence, or unresolved. Never select a winner based only on recency.')),
      item('inspect_output', 'Inspect the reconciliation proposal', 'internalReviewer', dependencies(['draft_reconciliation']),
        [input('reconciliation_record', 'Reconciliation proposal'), customerStatement, comparisonStatement],
        'An independent internal review note preserving the evidence trail.',
        proof('attestation', 'reconciliation_review', 'A labelled reviewer attestation checks the disposition and retains both originals. The review records no customer approval or resolution.')),
    ],
  },
]);

export function getPlaybookTemplate(id) {
  return playbookTemplates.find(template => template.id === id) || null;
}
