// Invented walkthrough records. No identity here grants authority.
export const fixturePeople = {
  accountOwner: 'Morgan Reed', internalReviewer: 'Alex Chen', customerContact: 'Rachel Ellis',
  customerCounsel: 'Nina Patel', otherAccountOwner: 'Maya Chen', workspaceProfile: 'Preston Neal',
};

export const renewalRecords=[{id:'thornfield',name:'Thornfield Wren LLP',initials:'TW',owner:'Morgan Reed',days:24,value:'$84,000',state:'Document needed',next:'Obtain the current AI terms document',tone:'amber'}, {id:'calder',name:'Calder Vale Partners',initials:'CV',owner:'Maya Chen',days:8,value:'$42,000',state:'Review pending',next:'Review the commercial exception',tone:'blue'}, {id:'ashbourne',name:'Ashbourne Legal Group',initials:'AL',owner:'Daniel Reed',days:15,value:'$126,000',state:'Ready for review',next:'Review the complete renewal package',tone:'green'}, {id:'mercer',name:'Mercer Hollow Advisory',initials:'MH',owner:'Priya Shah',days:50,value:'$36,000',state:'Owner needed',next:'Confirm the customer decision owner',tone:'neutral'}];

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
