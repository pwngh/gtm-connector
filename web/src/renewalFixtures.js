// Invented walkthrough records. No identity here grants authority.
export const fixturePeople = {
  accountOwner: 'Morgan Reed', internalReviewer: 'Alex Chen', customerContact: 'Rachel Ellis',
  customerCounsel: 'Nina Patel', otherAccountOwner: 'Maya Chen', workspaceProfile: 'Preston Neal',
};

export const renewalRecords=[{id:'thornfield',name:'Thornfield Wren LLP',initials:'TW',owner:'Morgan Reed',days:24,value:'$84,000',state:'Document needed',next:'Obtain the current AI terms document',tone:'amber'}, {id:'calder',name:'Calder Vale Partners',initials:'CV',owner:'Maya Chen',days:8,value:'$42,000',state:'Review pending',next:'Review the commercial exception',tone:'blue'}, {id:'ashbourne',name:'Ashbourne Legal Group',initials:'AL',owner:'Daniel Reed',days:15,value:'$126,000',state:'Ready for review',next:'Review the complete renewal package',tone:'green'}, {id:'mercer',name:'Mercer Hollow Advisory',initials:'MH',owner:null,days:null,value:'$36,000',state:'Owner needed',next:'Confirm the customer decision owner',tone:'neutral'}];

export const renewalEvidence = [
  {
    id: 'E-017', binding: 'customer_statement', emphasis: 'only after our counsel approves', type: 'Customer call', speaker: 'Rachel Ellis',
    role: 'Customer · Operations director', time: 'Nov 18 · 04:12',
    quote: 'We can proceed only after our counsel approves the current AI rider.',
    context: 'We’re comfortable with the proposed renewal. We can proceed only after our counsel approves the current AI rider. Could you send the version that applies to this agreement?',
    meaning: 'Customer condition',
    detail: 'The renewal depends on counsel’s approval of the applicable AI terms. This is a condition, not an approval.',
    agreement: 'Agreement A-17', tone: 'blue',
  },
  {
    id: 'E-042', binding: 'comparison_statement', type: 'Internal Slack', speaker: 'Morgan Reed',
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
  agreement: {id: 'A-17', label: 'Agreement A-17', title: 'AI service terms', displayTitle: 'AI service agreement', provenance: 'Invented workflow-side agreement for this walkthrough.'},
  requirementId: 'req-ai-counsel',
  bindings: {
    agreement: 'A-17', requirement: 'req-ai-counsel',
    customer_statement: renewalEvidence[0].id, comparison_statement: renewalEvidence[1].id,
  },
  roles: {
    actionOwners: [fixturePeople.accountOwner, fixturePeople.internalReviewer],
    internalReviewers: [fixturePeople.internalReviewer, fixturePeople.accountOwner],
    customerAuthorityOptions: [fixturePeople.customerCounsel],
  },
  paths: {
    root: '/renewals/thornfield', history: '/renewals/thornfield/history',
    evidence: '/renewals/thornfield/evidence', workplan: '/renewals/thornfield/workplan',
  },
  documentVersionBasis: null,
  taskId: 'task-obtain-ai-terms',
  workflowClockStart: '2026-11-19T16:30:00.000Z',
};

export const fixtureEvidence = [
  {id: renewalEvidence[0].id, type: 'customer_quote', title: 'Customer requirement', quote: renewalEvidence[0].quote,
    source: `${renewalEvidence[0].speaker} · ${renewalEvidence[0].time}`, interpretation: renewalEvidence[0].detail},
  {id: renewalEvidence[1].id, type: 'seller_expectation', title: 'Seller expectation', quote: renewalEvidence[1].quote,
    source: `${renewalEvidence[1].speaker} · ${renewalEvidence[1].time}`, interpretation: renewalEvidence[1].detail},
  {
    id: 'ev-document-003',
    type: 'document',
    title: 'Current AI terms',
    name: 'AI Terms \u2014 Version 3',
    preview: {title: 'Additional AI service terms', description: 'This fixture represents the current document referenced by the customer. It contains no legal advice or operative agreement.'},
    version: 'v3',
    currentVersion: 'v3',
    source: 'Rachel Ellis · Customer-supplied fixture',
    interpretation: 'The document is initially missing. Acquisition can be authorized before it is received.',
  },
];

export const initialState = {
  plan: {
    id: 'plan-ai-terms',
    title: 'Obtain the current AI terms document',
    owner: 'Morgan Reed',
    specifications: 'Confirm the current document applies to Agreement A-17 and preserve its source and version.',
    instructions: 'Obtain the document from Rachel Ellis. Verify the version before routing it to Nina Patel for customer counsel review.',
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
  history: [],
  error: null,
};


export const walkthroughDecisions = {
  nextAction: 'Rachel Ellis confirmed that customer counsel approved AI Terms v3 for Agreement A-17.',
  step: 'Customer counsel approved AI Terms v3 for Agreement A-17, confirmed by Rachel Ellis.',
  reviewedBy: 'Nina Patel, customer counsel',
};

// Worklist snapshots stay separate from the executable walkthrough.
export const worklistPeople = [
  ...renewalRecords.flatMap((record, index) => record.owner ? [{id: `principal-portfolio-${index + 1}`, displayName: record.owner}] : []),
  {id: 'principal-reviewer', displayName: fixturePeople.internalReviewer},
];

export const worklistFixture = {
  revision: '1',
  asOf: renewalFixture.workflowClockStart,
  people: worklistPeople,
  walkthrough: {renewalId: renewalFixture.id, reviewerId: 'principal-reviewer', paths: renewalFixture.paths},
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
const readinessCardId = 'card-thornfield-walkthrough';
const readinessReadAt = worklistFixture.asOf;
export const readinessFixture = {
  id: 'readiness-thornfield', revision: '1', renewalId: renewalFixture.id, agreement: renewalFixture.agreement,
  initialEvaluationAt: renewalFixture.workflowClockStart,
  paths: {...renewalFixture.paths, readiness: `${renewalFixture.paths.root}/readiness`},
  worklist: worklistFixture,
  policyVersion: 'walkthrough-1',
  scope: 'Existing local workflow rules. No real access, approval, or source connection.',
  policyBoundary: 'Transition checks use the existing walkthrough rules. Those rules do not establish customer authority or an independently sourced current version. Evidence checks stay separate and unknowns stay visible.',
  documentVersionBasis: renewalFixture.documentVersionBasis,
  customerAuthority: null,
  decisionReceipt: null,
  card: {
    id: readinessCardId, revision: '1', label: 'Walkthrough source card', readAt: readinessReadAt,
    provenance: 'Numbered projection of the existing invented excerpts and CRM snapshot. This is not a new source read or the connector demo card.',
    units: [
      {
        id: 'unit-customer-condition', cardId: readinessCardId, n: '1', kind: 'sentence',
        text: renewalEvidence[0].quote, legacyId: renewalEvidence[0].id,
        sourceId: 'source-customer-call', sourceLabel: renewalEvidence[0].type,
        eventAt: renewalEvidence[0].time, readAt: readinessReadAt, shown: true, partial: true,
        partialReason: 'Cut at the edge of the recorded excerpt.',
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
        eventAt: renewalEvidence[1].time, readAt: readinessReadAt, shown: true, partial: true,
        partialReason: 'Cut at the edge of the recorded excerpt.',
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
      {id: 'source-crm-snapshot', label: 'CRM snapshot', status: 'read', readAt: worklistFixture.renewals[0].crm.readAt,
        scope: 'Account and opportunity fields only. The agreement is a separate workflow-side record.', unitNumbers: ['3']},
      {id: 'source-customer-call', label: renewalEvidence[0].type, status: 'read', readAt: readinessReadAt,
        scope: 'The preserved customer excerpt only, not every call.', unitNumbers: ['1']},
      {id: 'source-internal-slack', label: renewalEvidence[1].type, status: 'read', readAt: readinessReadAt,
        scope: 'The preserved seller message only. Linked file contents were not read.', unitNumbers: ['2']},
    ],
    gaps: [
      {code: 'calls_outside_records', title: 'Calls outside the records',
        text: 'Calls outside the recorded excerpts were not read. Their contents and any conditions they contain are unknown.', count: null},
      {code: 'slack_channel', title: 'One unread Slack file',
        text: 'One linked Slack file was not read. The link supplies no document text, current-version basis, or customer approval.', count: 1},
      {code: 'neutralized', title: 'Text not shown',
        text: 'Two pieces of source text are not shown after neutralization. Their original contents are not available in this local card.', count: 2},
    ],
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
      checkIds: ['submit_plan', 'approve_plan', 'receive_document', 'verify_document', 'request_customer_review', 'record_customer_decision', 'resolve_requirement'],
      evidenceCheckIds: ['current_version_basis', 'customer_authority', 'decision_citation'],
    },
  ],
  checks: [
    {id: 'submit_plan', kind: 'transition', title: 'Submit the plan', command: 'request_review', allowedFlag: 'canRequestReview', workKind: 'plan_review',
      actionLabel: 'Open plan submission', inputKinds: ['plan', 'policy'],
      scope: 'Submit the current revision for an independent internal review. A missing document does not block this step.'},
    {id: 'approve_plan', kind: 'transition', title: 'Approve the work plan', command: 'approve_plan', allowedFlag: 'canApprovePlan', workKind: 'plan_review',
      actionLabel: 'Open submitted plan review', inputKinds: ['plan', 'policy'],
      scope: 'Internal approval releases document work only. It cannot approve the customer terms.'},
    {id: 'receive_document', kind: 'transition', title: 'Receive the document', command: 'obtain_document', allowedFlag: 'canObtainDocument', workKind: 'collection',
      actionLabel: 'Open document collection', inputKinds: ['plan', 'document', 'policy'],
      scope: 'Receipt produces a collection output. Inspection and customer review remain separate.'},
    {id: 'verify_document', kind: 'transition', title: 'Verify the document', command: 'verify_document', allowedFlag: 'canVerifyDocument', workKind: 'inspection',
      actionLabel: 'Open version verification', inputKinds: ['plan', 'document', 'policy'],
      scope: 'Open the existing walkthrough inspection. Its verification flag is not an independently sourced current-version fact.'},
    {id: 'request_customer_review', kind: 'transition', title: 'Request customer review', command: 'request_customer_review', allowedFlag: 'canRequestCustomerReview', workKind: 'customer_review',
      actionLabel: 'Open customer review request', inputKinds: ['plan', 'document', 'customer_decision', 'policy'],
      scope: 'Prepare a local review request for the inspected document. No message is sent and no authority is inferred.'},
    {id: 'record_customer_decision', kind: 'transition', title: 'Record the customer decision', command: 'record_customer_approval', allowedFlag: 'canRecordCustomerApproval', workKind: 'customer_review',
      actionLabel: 'Open decision recording', inputKinds: ['plan', 'document', 'customer_decision', 'policy'],
      scope: 'The existing form records a simulated decision. Typed text is not a cited receipt or an authority basis.'},
    {id: 'resolve_requirement', kind: 'transition', title: 'Resolve the requirement', command: 'resolve_requirement', allowedFlag: 'canResolveRequirement',
      actionLabel: 'Open requirement resolution', inputKinds: ['plan', 'document', 'customer_decision', 'requirement', 'policy'],
      scope: 'Review the local recorded decision before explicit resolution. This check does not confirm a signed renewal or complete separate work items.'},
    {id: 'current_version_basis', kind: 'evidence', title: 'Independent current-version basis', inputKinds: ['document', 'version_basis', 'card'],
      scope: 'The file cannot identify itself as the current version. A separate source must identify the applicable version for this agreement.'},
    {id: 'customer_authority', kind: 'evidence', title: 'Customer decision authority', inputKinds: ['authority', 'card'],
      scope: 'A customer role or a typed counsel name does not establish authority to approve this agreement.'},
    {id: 'decision_citation', kind: 'evidence', title: 'Cited customer decision', inputKinds: ['customer_decision', 'decision_receipt', 'card'],
      scope: 'The approval needs a cited source unit or an inspected artifact receipt for the applicable document and agreement.'},
  ],
  responses: [
    {id: 'current', label: 'Current evaluation', status: 'ready'},
    {id: 'not-evaluated', label: 'Not evaluated example', status: 'not_evaluated',
      title: 'No evaluation in this example.', message: 'Results and actions are withheld, not marked as passed. The underlying walkthrough and its saved evaluations are unchanged.'},
    {id: 'stale', label: 'Failed refresh example', status: 'stale',
      title: 'Refresh failed. The last result is retained.', message: 'This local failure example keeps the last complete evaluation and source card. Its actions are withheld until you return to the current evaluation. No external request was made.'},
  ],
};
