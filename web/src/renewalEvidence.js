// The same invented E-017 / E-042 records used by Evidence, Operations, and Playbooks.
// Extracted unchanged from V6 main.jsx; no original/private source rows are included.
export const renewalEvidence = [
  {
    id: 'E-017', type: 'Customer call', speaker: 'Rachel Ellis',
    role: 'Customer · Operations director', time: 'Nov 18 · 04:12',
    quote: 'We can proceed only after our counsel approves the current AI rider.',
    context: 'We’re comfortable with the proposed renewal. We can proceed only after our counsel approves the current AI rider. Could you send the version that applies to this agreement?',
    meaning: 'Customer condition',
    detail: 'The renewal depends on counsel’s approval of the applicable AI terms. This is a condition, not an approval.',
    agreement: 'Agreement A-17', tone: 'blue',
  },
  {
    id: 'E-042', type: 'Internal Slack', speaker: 'Morgan Reed',
    role: 'Internal · Account owner', time: 'Nov 19 · 09:24',
    quote: 'We expect signature on Friday.',
    context: 'The renewal call went well. We expect signature on Friday. Rachel still needs the latest rider for their counsel to review.',
    meaning: 'Seller expectation',
    detail: 'An expected signature date does not establish that the customer has approved the AI terms.',
    agreement: 'Opportunity 003', tone: 'neutral',
  },
];
