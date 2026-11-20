import React from 'react';
import {CircleDashed, Database, MessageSquare, Phone} from 'lucide-react';
import {Pill} from './WorkspaceControls.jsx';
import {decisionAuthorityLabel, evidenceCut, excerptAround, sourceCard, sourceReadLabel, speakerLine, speakerRole} from './renewalEvidence.js';

// Shared evidence labels for Work plan, Evidence and their dialogs. Authored
// context, typed input and gaps each carry a label, apart from the quotation.

// Authored for this walkthrough, never said by the speaker. Compact notes keep the meaning only.
export const workflowNoteLabel = 'Workflow note · applied for this walkthrough';
export function WorkflowNote({source, detail = false}) {
  return <div className="workflow-note">
    <span className="note-label">{workflowNoteLabel}</span>
    <p>{source.meaning}.</p>{detail && source.detail && <p>{source.detail}</p>}
  </div>;
}

// The role, its basis and authority in one caption, for previews beside a quotation.
export function SpeakerLine({source}) {
  return <p className="speaker-line">{speakerLine(source)}</p>;
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

// The text around the cited unit, labelled as context and never shown as the quotation.
export function SourceExcerpt({source}) {
  if (!source.context) return null;
  const around = excerptAround(source);
  const parts = around ? [['Before', around.before], ['After', around.after]].filter(([, text]) => text) : [['Excerpt', source.context]];
  if (!parts.length) return null;
  return <div className="source-excerpt">
    <span className="note-label">Neighbouring text in the preserved excerpt</span>
    {parts.map(([place, text]) => <p key={place}><span className="excerpt-place">{place}</span>{text}</p>)}
  </div>;
}

export function SpeakerRole({source}) {
  return <dl className="speaker-role"><SpeakerRows source={source}/></dl>;
}


export function DecisionAuthority({state = null}) {
  return <div><dt>Customer decision authority</dt><dd>{decisionAuthorityLabel(state)}</dd></div>;
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
