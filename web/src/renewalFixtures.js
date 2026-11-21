// Invented walkthrough records. No identity here grants authority.
export const fixturePeople = {
  accountOwner: 'Morgan Reed', internalReviewer: 'Alex Chen', customerContact: 'Rachel Ellis',
  otherAccountOwner: 'Maya Chen', workspaceProfile: 'Quinn Harlow',
};

export const renewalRecords=[{id:'thornfield',name:'Thornfield Wren LLP',initials:'TW',owner:'Morgan Reed',days:24,value:'$84,000',state:'Document needed',next:'Obtain the current AI terms document',tone:'amber'}, {id:'calder',name:'Calder Vale Partners',initials:'CV',owner:'Maya Chen',days:8,value:'$42,000',state:'Review pending',next:'Review the commercial exception',tone:'blue'}, {id:'ashbourne',name:'Ashbourne Legal Group',initials:'AL',owner:'Daniel Reed',days:15,value:'$126,000',state:'Ready for review',next:'Review the complete renewal package',tone:'green'}, {id:'mercer',name:'Mercer Hollow Advisory',initials:'MH',owner:null,days:null,value:'$36,000',state:'Owner needed',next:'Confirm the customer decision owner',tone:'neutral'}];

export const renewalEvidence = [
  {
    id: 'E-017', binding: 'customer_statement', partial: false, readStatus: 'read', emphasis: 'only after our counsel approves', type: 'Customer call', speaker: 'Rachel Ellis',
    role: 'Customer · Operations director', time: 'Nov 18 · 04:12',
    quote: 'We can proceed only after our counsel approves the current AI rider.',
    context: 'We’re comfortable with the proposed renewal. We can proceed only after our counsel approves the current AI rider. Could you send the version that applies to this agreement?',
    meaning: 'Customer condition',
    detail: 'The renewal depends on counsel’s approval of the applicable AI terms. This is a condition, not an approval.',
    agreement: 'Agreement A-17', tone: 'blue',
  },
  {
    id: 'E-042', binding: 'comparison_statement', partial: false, readStatus: 'read', type: 'Internal Slack', speaker: 'Morgan Reed',
    role: 'Internal · Account owner', time: 'Nov 19 · 09:24',
    quote: 'We expect signature on Friday.',
    context: 'The renewal call went well. We expect signature on Friday. Rachel still needs the latest rider for their counsel to review.',
    meaning: 'Seller expectation',
    detail: 'An expected signature date does not establish that the customer has approved the AI terms.',
    agreement: 'Opportunity 003', tone: 'neutral',
  },
];

export const renewalFixture = {
  id: 'thornfield-a17',
  account: renewalRecords[0],
  renewal: {id: '003'},
  // The workflow-side agreement record. It, not the document or the letter, states which rider version applies.
  agreement: {id: 'A-17', label: 'Agreement A-17', title: 'AI service terms', displayTitle: 'AI service agreement', provenance: 'Invented workflow-side agreement for this walkthrough.',
    applicableRider: {documentId: 'rider-v4', version: 'v4', recordedAt: '2026-11-19T14:30:00.000Z',
      provenance: 'Invented workflow-side agreement record. It names the rider version that applies to Agreement A-17. It is not a source read.'}},
  requirementId: 'req-ai-counsel',
  bindings: {
    agreement: 'A-17', requirement: 'req-ai-counsel',
    customer_statement: renewalEvidence[0].id, comparison_statement: renewalEvidence[1].id,
  },
  roles: {
    actionOwners: [fixturePeople.accountOwner, fixturePeople.internalReviewer],
    internalReviewers: [fixturePeople.internalReviewer, fixturePeople.accountOwner],
  },
  paths: {
    root: '/renewals/thornfield', history: '/renewals/thornfield/history',
    evidence: '/renewals/thornfield/evidence', workplan: '/renewals/thornfield/workplan',
  },
  taskId: 'task-obtain-ai-terms',
  workflowClockStart: '2026-11-19T16:30:00.000Z',
};

export const fixtureEvidence = [
  {id: renewalEvidence[0].id, type: 'customer_quote', title: 'Customer requirement', quote: renewalEvidence[0].quote,
    source: `${renewalEvidence[0].speaker} · ${renewalEvidence[0].time}`, interpretation: renewalEvidence[0].detail},
  {id: renewalEvidence[1].id, type: 'seller_expectation', title: 'Seller expectation', quote: renewalEvidence[1].quote,
    source: `${renewalEvidence[1].speaker} · ${renewalEvidence[1].time}`, interpretation: renewalEvidence[1].detail},
  {
    id: 'rider-v4',
    type: 'document',
    title: 'Current AI terms',
    name: 'AI rider · Version 4',
    preview: {title: 'Additional AI service terms', description: 'This fixture represents the current document referenced by the customer. It contains no legal advice or operative agreement.'},
    version: 'v4',
    currentVersion: 'v4',
    checksum: 'sha256:9e41c07b2d55a8f3',
    source: 'Rachel Ellis · Customer-supplied fixture',
    interpretation: 'The document is initially missing. Acquisition can be authorized before it is received.',
  },
];

// The signed approval letters are walkthrough data, keyed to the version they approve. They are
// the only source that names the general counsel. Pages show that name as a contact only after a
// reviewer records the signatory, with an accepted letter's receipt as the basis. The second file is a
// distinct replacement the customer supplies if the first is rejected or its authority is withdrawn.
const letterSignatory = {name: 'Helen Okafor', role: 'General counsel'};
export const approvalLetters = [{
  artifactId: 'artifact-approval-letter-v4', kind: 'approval_letter', title: 'Signed approval letter',
  name: 'Signed approval letter · AI rider v4',
  source: 'Rachel Ellis · Customer-supplied walkthrough file',
  checksum: 'sha256:51d0a7e3c96b24f8',
  approves: {agreementId: 'A-17', documentId: 'rider-v4', version: 'v4'},
  signatory: letterSignatory,
  contactId: 'contact-general-counsel',
  summary: 'The customer’s general counsel signs to approve AI rider version 4 for Agreement A-17.',
  excerpt: ['Re: Agreement A-17, AI service terms.', 'We approve AI rider version 4 for use under Agreement A-17.', `Signed: ${letterSignatory.name}, ${letterSignatory.role}.`],
  provenance: 'Invented walkthrough letter. It is not a source read, a real signature, or legal advice.',
}, {
  artifactId: 'artifact-approval-letter-v4-resigned', kind: 'approval_letter', title: 'Signed approval letter (second copy)',
  name: 'Signed approval letter · AI rider v4 · second copy',
  source: 'Rachel Ellis · Customer-supplied walkthrough file',
  checksum: 'sha256:8c3f61b0d2a9e475',
  approves: {agreementId: 'A-17', documentId: 'rider-v4', version: 'v4'},
  signatory: letterSignatory,
  contactId: 'contact-general-counsel',
  summary: 'A second signed copy of the customer’s approval of AI rider version 4 for Agreement A-17.',
  excerpt: ['Re: Agreement A-17, AI service terms. Second signed copy.', 'We approve AI rider version 4 for use under Agreement A-17.', `Signed: ${letterSignatory.name}, ${letterSignatory.role}.`],
  provenance: 'Invented walkthrough letter. It is not a source read, a real signature, or legal advice.',
}];

export const initialState = {
  plan: {
    id: 'plan-ai-terms',
    title: 'Obtain the current AI terms document',
    owner: 'Morgan Reed',
    ownerId: 'principal-portfolio-1',
    specifications: 'Confirm the current document applies to Agreement A-17 and preserve its source and version.',
    instructions: 'Obtain the document from Rachel Ellis. Verify the version before requesting customer counsel review. Record who decides only from a signed, inspected source.',
    revision: 3,
    status: 'draft',
    approvedRevision: null,
    requestedAt: null,
    approvedAt: null,
    approver: null,
    reviewNote: '',
  },
  task: null,
  document: null,
  customerReview: null,
  evidence: {artifactReceipts: [], contacts: [], customerAuthority: null},
  requirement: {
    id: 'req-ai-counsel',
    title: 'Customer counsel approves current AI terms',
    status: 'open',
    resolvedAt: null,
  },
  commercial: {
    status: 'not_confirmed',
    label: 'Renewal not confirmed',
    note: 'Resolving this legal requirement does not confirm a signed renewal.',
  },
  policyVersion: 'walkthrough-3',
  history: [],
  error: null,
};

// The current-version basis the checks read: the agreement record, independent of the document and of the letter.
export const agreementVersionBasis = {id: `version-basis-agreement-${renewalFixture.agreement.id}`, revision: '1', status: 'accepted',
  recordedAt: renewalFixture.agreement.applicableRider.recordedAt, recordedBy: null, agreementId: renewalFixture.agreement.id,
  documentId: renewalFixture.agreement.applicableRider.documentId, version: renewalFixture.agreement.applicableRider.version,
  source: {kind: 'agreement', id: renewalFixture.agreement.id}};

// The signed-in demo principals. Actors are explicit on every command, and duties are compared by these IDs.
export const walkthroughActors = {
  acquisition: fixturePeople.accountOwner, reviewer: fixturePeople.internalReviewer,
  acquisitionId: 'principal-portfolio-1', reviewerId: 'principal-reviewer',
};

// Worklist snapshots stay separate from the executable walkthrough.
export const worklistPeople = [
  ...renewalRecords.flatMap((record, index) => record.owner ? [{id: `principal-portfolio-${index + 1}`, displayName: record.owner}] : []),
  {id: 'principal-reviewer', displayName: fixturePeople.internalReviewer},
];
// Internal principals only. A customer contact is never a principal, so an approver can never record.
export const walkthroughPrincipals = worklistPeople;

export const worklistFixture = {
  revision: '1',
  asOf: renewalFixture.workflowClockStart,
  people: worklistPeople,
  walkthrough: {renewalId: renewalFixture.id, reviewerId: 'principal-reviewer', paths: renewalFixture.paths, agreementLabel: renewalFixture.agreement.label},
  renewals: [
    {
      id: renewalFixture.id, revision: '1', accountId: renewalFixture.account.id,
      accountName: renewalFixture.account.name, mode: 'walkthrough', ownerId: worklistPeople[0].id,
      renewalDate: '2026-12-13', lastChangedAt: '2026-11-19T14:30:00.000Z', requirementsRecorded: true,
      crm: {stage: 'negotiation', readAt: '2026-11-19T15:45:00.000Z', source: 'Invented CRM snapshot'},
      commercial: {status: 'not_confirmed', label: 'Not confirmed'},
    },
    {
      id: 'calder-renewal', revision: '1', accountId: renewalRecords[1].id,
      accountName: renewalRecords[1].name, mode: 'illustrative', ownerId: worklistPeople[1].id,
      renewalDate: '2026-11-27', snapshotAt: renewalFixture.workflowClockStart, requirementsRecorded: true,
      crm: {stage: 'negotiation', readAt: '2026-11-19T15:45:00.000Z', source: 'Invented CRM snapshot'},
      commercial: {status: 'unknown', label: 'Unknown'},
    },
    {
      id: 'ashbourne-renewal', revision: '1', accountId: renewalRecords[2].id,
      accountName: renewalRecords[2].name, mode: 'illustrative', ownerId: worklistPeople[2].id,
      renewalDate: '2026-12-04', snapshotAt: renewalFixture.workflowClockStart, requirementsRecorded: true,
      crm: {stage: 'proposal', readAt: '2026-11-19T15:45:00.000Z', source: 'Invented CRM snapshot'},
      commercial: {status: 'unknown', label: 'Unknown'},
    },
    {
      id: 'mercer-renewal', revision: '1', accountId: renewalRecords[3].id,
      accountName: renewalRecords[3].name, mode: 'illustrative', ownerId: null,
      renewalDate: null, snapshotAt: renewalFixture.workflowClockStart, requirementsRecorded: false,
      crm: {stage: null, readAt: null, source: 'Not recorded'},
      commercial: {status: 'unknown', label: 'Unknown'},
    },
  ],
  requirements: [
    {
      id: renewalFixture.requirementId, revision: '1', renewalId: renewalFixture.id,
      title: initialState.requirement.title, kind: 'customer_approval', status: 'open', blocking: true,
      ownerId: 'principal-reviewer',
      description: 'Customer counsel must approve the applicable document. Internal work approval and the commercial outcome remain separate.',
      evidence: {label: `${renewalEvidence[0].id} · ${renewalEvidence[0].type}`, href: renewalFixture.paths.evidence},
    },
    {
      id: 'req-calder-exception', revision: '1', renewalId: 'calder-renewal',
      title: 'Commercial exception needs review', kind: 'commercial_exception', status: 'open', blocking: true,
      ownerId: worklistPeople[1].id,
      description: 'An illustrative exception is awaiting supporting approval evidence. No review workflow is implemented for this account.',
      evidence: null,
    },
    {
      id: 'req-calder-decision-owner', revision: '1', renewalId: 'calder-renewal',
      title: 'Identify the customer decision owner', kind: 'decision_owner', status: 'open', blocking: true,
      ownerId: null,
      description: 'The decision owner is unidentified in this illustrative snapshot. A role alone would not establish decision authority.',
      evidence: null,
    },
  ],
  workItems: [
    {
      id: 'work-plan-review-ai-terms', revision: '1', renewalId: renewalFixture.id,
      requirementId: renewalFixture.requirementId, kind: 'plan_review', title: 'Review the document work plan', readinessCheckId: 'submit_plan',
      dueAt: null, step: 1, lastChangedAt: '2026-11-19T14:30:00.000Z',
      completionRule: 'A separate reviewer approves the scoped plan revision. This is internal work approval, not customer approval.',
    },
    {
      id: renewalFixture.taskId, revision: '1', renewalId: renewalFixture.id,
      requirementId: renewalFixture.requirementId, kind: 'collection', title: 'Obtain the current AI terms document', readinessCheckId: 'receive_document',
      dueAt: null, step: 2, lastChangedAt: '2026-11-19T14:30:00.000Z',
      completionRule: 'Collection has an output when the document is received. Inspection, customer review, and requirement resolution are separate.',
    },
    {
      id: 'work-inspect-ai-terms', revision: '1', renewalId: renewalFixture.id,
      requirementId: renewalFixture.requirementId, kind: 'inspection', title: 'Inspect the received document version', readinessCheckId: 'verify_document',
      dueAt: null, step: 2, lastChangedAt: '2026-11-19T14:30:00.000Z',
      completionRule: 'Inspect the received version and its applicability to the agreement. Receipt alone does not complete inspection.',
    },
    {
      id: 'work-review-ai-terms', revision: '1', renewalId: renewalFixture.id,
      requirementId: renewalFixture.requirementId, kind: 'customer_review', title: 'Obtain customer counsel review', readinessCheckId: 'request_customer_review',
      dueAt: null, step: 3, lastChangedAt: '2026-11-19T14:30:00.000Z',
      awaitedParty: {id: null, label: 'Customer counsel · unidentified', basis: 'The customer statement identifies a role, not a decision owner.'},
      completionRule: 'Record the customer decision for the reviewed document. Completing this item does not resolve the requirement or confirm a renewal.',
    },
    {
      id: 'work-calder-exception', revision: '1', renewalId: 'calder-renewal',
      requirementId: 'req-calder-exception', kind: 'commercial_review', title: 'Review the commercial exception',
      ownerId: worklistPeople[1].id, waitingOn: null, status: 'blocked',
      dueAt: '2026-11-18T17:00:00.000Z',
      blockedReason: 'Supporting exception evidence is not recorded in this snapshot.',
      completionRule: 'Illustrative review only. No completion command or review record exists for this account.',
    },
    {
      id: 'work-ashbourne-package', revision: '1', renewalId: 'ashbourne-renewal',
      requirementId: null, kind: 'package_review', title: 'Review the renewal package',
      ownerId: worklistPeople[2].id, status: 'waiting',
      waitingOn: {id: null, label: 'Customer decision owner · unidentified', basis: 'No supporting contact evidence is recorded in this snapshot.'},
      dueAt: '2026-11-22T17:00:00.000Z',
      waitingReason: 'A customer decision is not recorded in this snapshot.',
      completionRule: 'Illustrative review only. The package cannot be approved or completed here.',
    },
    {
      id: 'work-mercer-owner', revision: '1', renewalId: 'mercer-renewal',
      requirementId: null, kind: 'owner_check', title: 'Identify the customer decision owner',
      ownerId: null, status: 'blocked', dueAt: null, waitingOn: null,
      blockedReason: 'An action owner and supporting contact evidence have not been recorded.',
      completionRule: 'Illustrative owner check only. No assignment or customer authority can be recorded here.',
    },
  ],
};

export const worklistResponseExamples = [
  {id: 'normal', label: 'Available records', status: 'ready'},
  {id: 'truncated', label: 'Truncated list', status: 'ready', limit: 2},
  {id: 'denied', label: 'Not permitted (403)', status: 'not_permitted', code: 403,
    title: 'This list is not permitted.', message: 'This simulated response withholds records and counts. It is not an empty result and does not change any real access.'},
  {id: 'error', label: 'Read failed', status: 'error', code: 503,
    title: 'The list could not be read.', message: 'This is a local failure example. No partial results are shown and no external request was made.'},
  {id: 'loading', label: 'Loading preview', status: 'loading',
    title: 'Waiting for a list response.', message: 'This is a paused loading example, not a running request. Return to available records to continue.'},
];

// Readiness projects the existing story, not a new connected source read.
export const renewalSourceBoundaries = [
  {code: 'calls_outside_records', title: 'Calls outside the records',
    text: 'Calls outside the recorded excerpts were not read. Their contents and any conditions they contain are unknown.', count: null},
];
const readinessCardId = 'card-thornfield-walkthrough';
const readinessReadAt = worklistFixture.asOf;
export const readinessFixture = {
  id: 'readiness-thornfield', revision: '2', renewalId: renewalFixture.id, agreement: renewalFixture.agreement,
  initialEvaluationAt: renewalFixture.workflowClockStart,
  paths: {...renewalFixture.paths, readiness: `${renewalFixture.paths.root}/readiness`},
  worklist: worklistFixture,
  policyVersion: initialState.policyVersion,
  scope: 'Existing local workflow rules. No real access, approval, or source connection.',
  policyBoundary: 'Transition checks never offer a step the walkthrough reducer refuses, and the reducer refuses a decision or resolution that its evidence checks do not support. A decision cites the accepted signed letter for the exact agreement, document and version; typed text and source units are not accepted as a decision. The letter inspector, the authority recorder and the decision recorder are each a different principal from the plan owner and the letter’s uploader. No check grants real authority.',
  documentVersionBasis: agreementVersionBasis,
  customerAuthority: null,
  decisionReceipt: null,
  contacts: [],
  artifactReceipts: [],
  card: {
    id: readinessCardId, revision: '1', label: 'Walkthrough source card', readAt: readinessReadAt,
    provenance: 'Numbered projection of the existing invented excerpts and CRM snapshot. This is not a new source read or the connector demo card.',
    units: [
      {
        id: 'unit-customer-condition', cardId: readinessCardId, n: '1', kind: 'sentence',
        text: renewalEvidence[0].quote, legacyId: renewalEvidence[0].id,
        sourceId: 'source-customer-call', sourceLabel: renewalEvidence[0].type,
        eventAt: renewalEvidence[0].time, readAt: readinessReadAt, shown: true, partial: renewalEvidence[0].partial,
        speaker: {name: renewalEvidence[0].speaker, role: renewalEvidence[0].role,
          basis: `Synthetic contact match: the speaker label matches ${fixturePeople.customerContact}, the customer operations contact in the existing fixture.`,
          authority: 'unidentified'},
        receipt: {id: 'receipt-call-excerpt', status: 'Local excerpt preserved', readAt: readinessReadAt},
        neighbours: [],
      },
      {
        id: 'unit-seller-expectation', cardId: readinessCardId, n: '2', kind: 'sentence',
        text: renewalEvidence[1].quote, legacyId: renewalEvidence[1].id,
        sourceId: 'source-internal-slack', sourceLabel: renewalEvidence[1].type,
        eventAt: renewalEvidence[1].time, readAt: readinessReadAt, shown: true, partial: renewalEvidence[1].partial,
        speaker: {name: renewalEvidence[1].speaker, role: renewalEvidence[1].role,
          basis: `Synthetic principal match: the speaker label matches ${fixturePeople.accountOwner}, the account owner in the existing fixture.`,
          authority: 'not_customer_authority'},
        receipt: {id: 'receipt-slack-excerpt', status: 'Local excerpt preserved', readAt: readinessReadAt},
        neighbours: [],
      },
      {
        id: 'unit-crm-renewal', cardId: readinessCardId, n: '3', kind: 'record',
        sourceId: 'source-crm-snapshot', sourceLabel: worklistFixture.renewals[0].crm.source,
        eventAt: null, readAt: worklistFixture.renewals[0].crm.readAt, shown: true, partial: false,
        fields: [
          {label: 'Account', value: renewalFixture.account.name},
          {label: 'Opportunity', value: renewalFixture.renewal.id},
          {label: 'CRM stage', value: worklistFixture.renewals[0].crm.stage},
        ],
        receipt: {id: 'receipt-crm-snapshot', status: 'Local record preserved', readAt: worklistFixture.renewals[0].crm.readAt},
        neighbours: [],
      },
    ],
    sources: [
      {id: 'source-crm-snapshot', label: 'CRM snapshot', kind: 'record', status: 'read', readAt: worklistFixture.renewals[0].crm.readAt,
        scope: 'Account and opportunity fields only. The agreement is a separate workflow-side record.', unitNumbers: ['3']},
      {id: 'source-customer-call', label: renewalEvidence[0].type, kind: 'call', status: renewalEvidence[0].readStatus, readAt: readinessReadAt,
        scope: 'The preserved customer excerpt only, not every call.', unitNumbers: ['1']},
      {id: 'source-internal-slack', label: renewalEvidence[1].type, kind: 'message', status: renewalEvidence[1].readStatus, readAt: readinessReadAt,
        scope: 'The preserved seller message only, not every channel message.', unitNumbers: ['2']},
    ],
    gaps: renewalSourceBoundaries,
  },
  requirementsRecorded: true,
  requirements: [
    {
      id: renewalFixture.requirementId, revision: '1', renewalId: renewalFixture.id,
      title: initialState.requirement.title, kind: 'customer_approval', kindLabel: 'Customer approval',
      binding: 'workflow_requirement', status: 'open', ownerId: worklistFixture.walkthrough.reviewerId,
      resolutionCriteria: [
        'Review customer counsel approval for the applicable document under the workflow-side agreement.',
        'Keep the approving contact, their authority basis, and the cited decision evidence distinct from the person recording it.',
        'Record an explicit requirement resolution. It is not a work-item completion or a commercial outcome.',
      ],
      citations: [
        {cardId: readinessCardId, n: '1', context: 'Workflow context: this customer condition is cited by the requirement.'},
        {cardId: readinessCardId, n: '2', context: 'Workflow context: this seller expectation is comparison evidence, not customer approval.'},
      ],
      awaitedParty: worklistFixture.workItems[3].awaitedParty,
      checkIds: ['submit_plan', 'approve_plan', 'receive_document', 'verify_document', 'request_customer_review', 'receive_approval_letter', 'inspect_approval_letter', 'record_customer_authority', 'record_customer_decision', 'resolve_requirement'],
      evidenceCheckIds: ['current_version_basis', 'customer_authority', 'decision_citation'],
    },
  ],
  checks: [
    {id: 'submit_plan', kind: 'transition', title: 'Submit the plan', command: 'request_review', workKind: 'plan_review',
      actionLabel: 'Open plan submission', inputKinds: ['plan', 'policy'],
      scope: 'Submit the current revision for an independent internal review. A missing document does not block this step.'},
    {id: 'approve_plan', kind: 'transition', title: 'Approve the work plan', command: 'approve_plan', workKind: 'plan_review',
      actionLabel: 'Open submitted plan review', inputKinds: ['plan', 'policy'],
      scope: 'Internal approval releases document work only. It cannot approve the customer terms.'},
    {id: 'receive_document', kind: 'transition', title: 'Receive the document', command: 'obtain_document', workKind: 'collection',
      actionLabel: 'Open document collection', inputKinds: ['plan', 'document', 'inspection_work_item', 'policy'],
      replacementTitle: 'Receive a replacement document', replacementActionLabel: 'Open replacement collection',
      scope: 'Receipt produces a collection output. A replacement can be received under the approved plan. Inspection and customer review remain separate.'},
    {id: 'verify_document', kind: 'transition', title: 'Verify the document', command: 'verify_document', workKind: 'inspection',
      actionLabel: 'Open version inspection', inputKinds: ['plan', 'document', 'inspection_work_item', 'policy'],
      scope: 'Inspect the received version and accept or reject it with a reason. A rejected version is kept. Acceptance is not an independently sourced current-version fact.'},
    {id: 'request_customer_review', kind: 'transition', title: 'Request customer review', command: 'request_customer_review', workKind: 'customer_review',
      actionLabel: 'Open customer review request', inputKinds: ['plan', 'document', 'customer_decision', 'customer_review_work_item', 'policy'],
      scope: 'Prepare a local review request for the inspected document. No message is sent and no authority is inferred.'},
    {id: 'receive_approval_letter', kind: 'transition', title: 'Receive the signed approval letter', command: 'upload_approval_letter', workKind: 'customer_review',
      actionLabel: 'Open letter upload', inputKinds: ['plan', 'document', 'customer_decision', 'customer_review_work_item', 'artifact_receipts', 'policy'],
      scope: 'Upload the customer’s signed letter for the version under review. The upload records a receipt only. Nobody is named until a reviewer records who decides.'},
    {id: 'inspect_approval_letter', kind: 'transition', title: 'Inspect the approval letter', command: 'inspect_approval_letter', workKind: 'customer_review',
      actionLabel: 'Open letter inspection', inputKinds: ['artifact_receipts', 'customer_review_work_item', 'policy'],
      scope: 'Accept or reject the received letter with a typed reason. Each inspection is appended to the receipt. The inspector is neither the plan owner nor the letter’s uploader.'},
    {id: 'record_customer_authority', kind: 'transition', title: 'Record who decides', command: 'record_customer_authority', workKind: 'customer_review',
      actionLabel: 'Open authority recording', inputKinds: ['artifact_receipts', 'authority', 'customer_review_work_item', 'policy'],
      scope: 'Record the letter’s signatory as a contact, with the accepted letter receipt as the authority basis for this agreement, document and version. The recorder is neither the plan owner nor the letter’s uploader.'},
    {id: 'record_customer_decision', kind: 'transition', title: 'Record the customer decision', command: 'record_customer_approval', workKind: 'customer_review',
      actionLabel: 'Open decision recording', inputKinds: ['plan', 'document', 'customer_decision', 'customer_review_work_item', 'authority', 'decision_receipt', 'version_basis', 'card', 'policy'],
      scope: 'Record a decision that cites the accepted signed letter for this agreement, document and version, names the contact from the authority record, and records which authority record it relied on. Typed text and source units are not accepted.'},
    {id: 'resolve_requirement', kind: 'transition', title: 'Resolve the requirement', command: 'resolve_requirement',
      actionLabel: 'Open requirement resolution', inputKinds: ['plan', 'document', 'customer_decision', 'customer_review_work_item', 'requirement', 'authority', 'decision_receipt', 'version_basis', 'card', 'policy'],
      scope: 'Review the local recorded decision before explicit resolution. This check does not confirm a signed renewal or complete separate work items.'},
    {id: 'current_version_basis', kind: 'evidence', title: 'Independent current-version basis', inputKinds: ['document', 'version_basis', 'card'],
      correctiveLabel: 'Identify the current version with independent evidence',
      guidance: 'The workflow-side agreement record names the rider version that applies. Neither the document file nor the approval letter establishes it.',
      scope: 'The file cannot identify itself as the current version, and a letter approving a version does not make it current. A separate record must identify the applicable version for this agreement.'},
    {id: 'customer_authority', kind: 'evidence', title: 'Customer decision authority', inputKinds: ['authority', 'card'],
      correctiveLabel: 'Record who decides, with evidence',
      guidance: 'Record who decides from an accepted signed letter. The authority record carries the agreement, document, version and signatory from that receipt, and covers only that version. A typed name will not clear this check.',
      scope: 'A customer role or a typed counsel name does not establish authority to approve this agreement. Authority for one version does not carry over to another.'},
    {id: 'decision_citation', kind: 'evidence', title: 'Cited customer decision', inputKinds: ['document', 'customer_decision', 'decision_receipt', 'authority', 'card'],
      correctiveLabel: 'Cite the customer decision for this version',
      guidance: 'The accepted decision must cite the accepted signed-letter receipt for this agreement, document and version, and name the contact and authority record that receipt supports. Typed text is not that citation, and a source unit cannot establish an approval until units carry a speaker-to-authority binding and a reviewer label.',
      scope: 'The approval needs the accepted signed-letter receipt for the applicable document and agreement.'},
  ],
  responses: [
    {id: 'current', label: 'Current evaluation', status: 'ready'},
    {id: 'not-evaluated', label: 'Not evaluated example', status: 'not_evaluated',
      title: 'No evaluation in this example.', message: 'Results and actions are withheld, not marked as passed. The underlying walkthrough and its saved evaluations are unchanged.'},
    {id: 'stale', label: 'Failed refresh example', status: 'stale',
      title: 'Refresh failed. The last result is retained.', message: 'This local failure example keeps the last complete evaluation and source card. Its actions are withheld until you return to the current evaluation. No external request was made.'},
  ],
};

// Isolated validation examples, never added to the walkthrough automatically. The example contact is
// an invented name for this check only. It is not the walkthrough's counsel and signs nothing there.
const exampleSignatory = {name: 'Iris Calloway', role: 'Example signatory'};
export const readinessEvidenceExample = {
  source: {id: 'source-version-example', label: 'Separate version message example', kind: 'message', status: 'read',
    readAt: readinessReadAt, scope: 'Isolated evidence-check example.', unitNumbers: ['4']},
  unit: {id: 'unit-version-example', cardId: readinessCardId, n: '4', kind: 'sentence',
    sourceId: 'source-version-example', sourceLabel: 'Separate version message example', shown: true, partial: false,
    text: `The current document for ${renewalFixture.agreement.label} is AI rider ${fixtureEvidence[2].version}.`,
    readAt: readinessReadAt, eventAt: readinessReadAt, neighbours: []},
  contact: {id: 'contact-counsel-example', displayName: exampleSignatory.name, role: exampleSignatory.role, status: 'active', recordedAt: readinessReadAt,
    exampleLabel: 'Invented example contact for an isolated check. Not the walkthrough’s counsel.'},
  receipt: {id: 'receipt-decision-example', artifactId: 'artifact-counsel-decision-example', kind: 'approval_letter', status: 'received',
    approves: {agreementId: renewalFixture.agreement.id, documentId: fixtureEvidence[2].id, version: fixtureEvidence[2].version},
    signatory: exampleSignatory, contactId: 'contact-counsel-example',
    receivedAt: readinessReadAt, inspections: [{status: 'accepted', at: readinessReadAt, reason: 'Example inspection for an isolated check.'}]},
  documentVersionBasis: {id: 'version-basis-example', revision: '1', recordedAt: readinessReadAt,
    agreementId: renewalFixture.agreement.id, documentId: fixtureEvidence[2].id, version: fixtureEvidence[2].version,
    source: {kind: 'source', id: 'source-version-example'}},
  customerAuthority: {id: 'authority-example', revision: '1', recordedAt: readinessReadAt, status: 'accepted',
    agreementId: renewalFixture.agreement.id, documentId: fixtureEvidence[2].id, version: fixtureEvidence[2].version, signatory: exampleSignatory,
    contactId: 'contact-counsel-example', evidenceRef: {kind: 'receipt', id: 'receipt-decision-example'}},
  decisionReceipt: {id: 'decision-example', revision: '1', recordedAt: readinessReadAt, status: 'accepted', outcome: 'approved',
    agreementId: renewalFixture.agreement.id, documentId: fixtureEvidence[2].id, documentVersion: fixtureEvidence[2].version,
    contactId: 'contact-counsel-example', authorityRef: {id: 'authority-example', revision: '1'}, evidenceRef: {kind: 'receipt', id: 'receipt-decision-example'}},
};

export const walkthroughDocumentExamples = {
  outdated: {...fixtureEvidence[2], id: 'rider-v3', name: 'AI rider · Version 3', version: 'v3', checksum: 'sha256:0b7d2e94f1a6c358'},
  replacement: {...fixtureEvidence[2], id: 'rider-v5', name: 'AI rider · Version 5', version: 'v5', currentVersion: 'v5', checksum: 'sha256:c2f8a1d04e7b9365'},
  // The same version, scanned again. A distinct file the customer supplies after a rejected copy.
  rescan: {...fixtureEvidence[2], name: 'AI rider · Version 4 · new scan', checksum: 'sha256:4a6e2c90b17fd853'},
};

export const readinessEvidenceFixture = {
  ...readinessFixture,
  card: {...readinessFixture.card,
    sources: [...readinessFixture.card.sources, readinessEvidenceExample.source],
    units: [...readinessFixture.card.units, readinessEvidenceExample.unit]},
  contacts: [readinessEvidenceExample.contact], artifactReceipts: [readinessEvidenceExample.receipt],
  documentVersionBasis: readinessEvidenceExample.documentVersionBasis,
  customerAuthority: readinessEvidenceExample.customerAuthority,
  decisionReceipt: readinessEvidenceExample.decisionReceipt,
};

// History examples are inspect-only and never enter the saved walkthrough.
export const historyFixture = {
  id: 'history-thornfield', revision: '1', renewalId: renewalFixture.id,
  paths: readinessFixture.paths, readiness: readinessFixture,
  baseline: {id: 'history-opening-snapshot', recordedAt: renewalFixture.workflowClockStart, actor: null, eventAt: null,
    title: 'Opening walkthrough snapshot', state: initialState,
    note: 'This fixed opening fixture already contains the plan and open requirement. Their original creation times and authors were not recorded. It is not a reconstructed earlier decision.'},
  scope: 'Synthetic, browser-only history. No real authorization, customer message, or connected source read.',
  policyBoundary: 'Effects follow the Work plan rules and Readiness checks of the policy version each entry records. A title or owner edit keeps the customer decision; a specifications edit, a replacement document or a withdrawn authority marks it for re-review and reopens a resolved requirement with a reason and an actor. A decision is recorded only with the accepted signed letter and an authority record for that version.',
  filename: `${renewalFixture.account.id}-history-view.json`,
  examples: [
    {id: 'walkthrough', label: 'Your walkthrough', mode: 'live'},
    {id: 'correction', label: 'Correction example (not your walkthrough)', mode: 'example',
      title: 'A correction and its original record',
      description: 'This is a separate invented history, not your walkthrough. Its letter names its signatory; your walkthrough names nobody until you record who decides. Inspect its source dates, later recordings, and typed attribution changes. Its records never change your walkthrough.',
      steps: [
        {id: 'example-plan-request', action: {type: 'request_review', actor: fixturePeople.accountOwner, at: '2026-11-19T16:31:00.000Z'}},
        {id: 'example-plan-approval', action: {type: 'approve_plan', approver: fixturePeople.internalReviewer, at: '2026-11-19T16:32:00.000Z'}},
        {id: 'example-document-receipt', action: {type: 'obtain_document', actorId: walkthroughActors.acquisitionId,
          eventAt: '2026-11-18T14:00:00.000Z', at: '2026-11-19T16:33:00.000Z'}},
        {id: 'example-inspection', action: {type: 'verify_document', verifiedBy: fixturePeople.accountOwner, actorId: walkthroughActors.acquisitionId,
          reason: 'Version 4 matches the version Agreement A-17 names, and the file meets the specifications.', at: '2026-11-19T16:34:00.000Z'}},
        {id: 'example-customer-request', action: {type: 'request_customer_review', at: '2026-11-19T16:35:00.000Z'}},
        {id: 'example-letter-receipt', action: {type: 'upload_approval_letter', actorId: walkthroughActors.acquisitionId,
          eventAt: '2026-11-19T15:00:00.000Z', at: '2026-11-19T16:35:15.000Z'}},
        {id: 'example-letter-inspection', action: {type: 'inspect_approval_letter', outcome: 'accepted', actorId: walkthroughActors.reviewerId,
          reason: 'Signed and legible. It names Agreement A-17 and rider v4.', at: '2026-11-19T16:35:30.000Z'}},
        {id: 'example-authority', action: {type: 'record_customer_authority', actorId: walkthroughActors.reviewerId, at: '2026-11-19T16:35:45.000Z'}},
        {id: 'example-customer-decision', action: {type: 'record_customer_approval', actorId: walkthroughActors.reviewerId,
          evidenceRef: {kind: 'receipt', id: 'receipt-approval-letter-v4-1'}, contactId: approvalLetters[0].contactId,
          eventAt: '2026-11-19T15:00:00.000Z', at: '2026-11-19T16:36:00.000Z'}},
        {id: 'example-resolution', action: {type: 'resolve_requirement', actorId: walkthroughActors.reviewerId, at: '2026-11-19T16:37:00.000Z'}},
        {id: 'example-correction', correction: {
          type: 'correction', title: 'Customer decision attribution corrected',
          at: '2026-11-19T16:40:00.000Z', eventAt: '2026-11-19T15:05:00.000Z', actor: fixturePeople.internalReviewer,
          reason: 'The earlier entry named the wrong approving contact. The document scope and separate evidence records are unchanged.',
          supersedes: 'example-customer-decision',
          changes: {after: {customerReview: {contactId: 'contact-operations-director'}}},
        }},
        {id: 'example-reopening', action: {type: 'reopen_requirement', actorId: walkthroughActors.reviewerId,
          reason: 'The corrected decision does not support the recorded resolution. Obtain a supported customer decision for the applicable version.',
          at: '2026-11-19T16:41:00.000Z'}},
        {id: 'example-authority-withdrawal', action: {type: 'withdraw_customer_authority', actorId: walkthroughActors.reviewerId,
          reason: 'The authority basis needs new supporting evidence.', at: '2026-11-19T16:42:00.000Z'}},
        {id: 'example-plan-revision', action: {type: 'save_plan', actorId: walkthroughActors.acquisitionId,
          specifications: 'Preserve the agreement reference, version, and independent source identifying the current document.',
          instructions: 'Re-inspect the received file and request a supported customer decision for that version.',
          at: '2026-11-19T16:44:00.000Z'}},
      ],
    },
  ],
};

export const historyTestRecords = {
  secondPlan: {...initialState.plan, id: 'plan-history-second', title: 'Collect the separate document scope'},
  secondRequirement: {...readinessFixture.requirements[0], id: 'req-history-second', title: 'Separate review requirement', binding: null, citations: [], checkIds: [], evidenceCheckIds: []},
  missingEvent: {id: 'history-missing-fields', type: 'request_changes', title: 'Review record with missing metadata'},
  literalTime: '2026-11-19T16:36:00',
  afterClock: '2026-11-19T18:00:00.000Z',
  beforeClock: '2026-11-19T15:00:00.000Z',
  editReason: 'The document scope needs another review.',
  revisedTitle: 'Obtain the applicable terms and preserve their source',
  revisedSpecifications: 'Preserve the agreement reference and the independent current-version source.',
  pendingUnitCitation: {...readinessEvidenceExample.decisionReceipt, status: 'pending', outcome: null, evidenceRef: {kind: 'unit', cardId: readinessFixture.card.id, n: readinessFixture.card.units[0].n}},
  followupCorrection: {id: 'example-correction-followup', type: 'correction', title: 'Follow-up attribution correction',
    at: '2026-11-19T16:46:00.000Z', actor: fixturePeople.internalReviewer,
    supersedes: 'example-correction', reason: 'The recorder field was attributed to the wrong walkthrough record. The received document scope is unchanged.',
    changes: {after: {customerReview: {recordedBy: fixturePeople.accountOwner}}}},
  commercialOutcome: {id: 'example-commercial-outcome', type: 'commercial_outcome', title: 'Separate commercial result recorded',
    at: '2026-11-19T18:00:00.000Z', actor: fixturePeople.accountOwner,
    commercial: {status: 'confirmed', label: 'Separate synthetic renewal confirmation'}},
};

export const historyReviewTestRecords = {
  cutoff: '2026-11-19T16:45:00.000Z',
  laterRecording: '2026-11-19T17:30:00.000Z',
  earlierRecording: '2026-11-19T16:31:00.000Z',
  lateReceipt: {...readinessEvidenceExample.receipt, id: 'receipt-late-history', artifactId: 'artifact-late-history', receivedAt: '2026-11-19T17:30:00.000Z',
    inspections: [{status: 'accepted', at: '2026-11-19T17:31:00.000Z'}]},
  sameVersionReplacement: {...fixtureEvidence[2], id: 'rider-v4-replacement', checksum: 'sha256:7d15e0c3a92b4f68'},
  resolutionCorrection: {id: 'history-resolution-correction', type: 'correction', title: 'Resolution status corrected',
    at: '2026-11-19T16:43:00.000Z', actor: fixturePeople.internalReviewer,
    reason: 'The resolution state was recorded incorrectly. Retain the original recording for comparison.',
    changes: {after: {requirement: {status: 'open', resolvedAt: null}}}},
  contextChange: {...readinessEvidenceFixture, policyVersion: 'walkthrough-changed-example'},
};
