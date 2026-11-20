import React from 'react';
import {CircleDashed, Database, MessageSquare, Phone} from 'lucide-react';
import {Pill} from './WorkspaceControls.jsx';
import {customerAuthorityLabel, evidenceCut, sourceCard, sourceReadLabel, speakerRole, workflowNoteText} from './renewalEvidence.js';

// Shared evidence labels for Work plan, Evidence and their dialogs. Authored
// context, typed input and gaps each carry a label, apart from the quotation.

// Compact notes add the speaker's authority. Detailed notes sit beside the full role.
export function WorkflowNote({source, detail = false}) {
  return <div className="workflow-note">
    <span className="note-label">Workflow note</span>
    {detail ? <><p>{source.meaning}.</p>{source.detail && <p>{source.detail}</p>}</> : <p>{workflowNoteText(source)}</p>}
  </div>;
}

// Only a cut unit gets a label. A whole sentence is shown as it is.
export function QuoteCut({source}) {
  const cut = evidenceCut(source);
  return cut ? <p className="evidence-cut">{cut}</p> : null;
}

// Rows for a description list: the role, how it was established, and authority.
export function SpeakerRows({source}) {
  const speaker = speakerRole(source);
  return <>
    <div><dt>Speaker</dt><dd>{speaker.name}</dd></div>
    <div><dt>Role</dt><dd>{speaker.role}</dd></div>
    <div><dt>Role basis</dt><dd>{speaker.basis}</dd></div>
    <div><dt>Customer decision authority</dt><dd>{speaker.authority}</dd></div>
  </>;
}

export function SpeakerRole({source}) {
  return <dl className="speaker-role"><SpeakerRows source={source}/></dl>;
}

export function ReviewerInput({label, children}) {
  return <div className="reviewer-input"><span className="note-label">{label} (reviewer input)</span><p>{children || 'Not recorded'}</p></div>;
}

export function DecisionAuthority() {
  return <div><dt>Customer decision authority</dt><dd>{customerAuthorityLabel}</dd></div>;
}

const sourceIcons = {call: Phone, message: MessageSquare, record: Database};

export function SourceReads() {
  return <ul className="coverage-list">{sourceCard.sources.map(source => {
    const Icon = sourceIcons[source.kind] || Database;
    return <li className="coverage-row" key={source.id} data-source-id={source.id}><Icon size={20} aria-hidden="true"/>
      <div><strong>{source.label}</strong><small>{source.scope}</small></div>
      <Pill tone={source.status === 'read' ? 'green' : 'amber'}>{sourceReadLabel(source.status)}</Pill></li>;
  })}</ul>;
}

export function SourceGaps() {
  return <ul className="coverage-list coverage-gaps">{sourceCard.gaps.map(gap => <li className="coverage-row" key={gap.code} data-gap-code={gap.code}>
    <CircleDashed size={20} aria-hidden="true"/><div><strong>{gap.title}</strong><small>{gap.text}</small></div>
  </li>)}</ul>;
}
