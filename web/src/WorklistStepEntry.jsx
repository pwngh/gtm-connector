import React from 'react';
import './worklistEntry.css';

export default function WorklistStepEntry({item, onAction}) {
  return <section className="wl-step-entry" aria-labelledby={item.entryTargetId} data-work-item={item.id}>
    <h3 id={item.entryTargetId} tabIndex={-1}>{item.title}</h3>
    <p>{item.statusLabel} · Action owner: {item.ownerLabel}</p>
    <p>{item.completionRule}</p>
    {item.waitingOn && <p><strong>Waiting on: </strong>{item.waitingLabel}<br/>{item.waitingReason}</p>}
    {item.blockedReasons.length > 0 && <div className="wl-step-prerequisites"><h4>Prerequisites</h4><ul>{item.blockedReasons.map(reason => <li key={reason.code}>{reason.reason}</li>)}</ul></div>}
    {item.output && <div className="wl-step-output"><h4>Recorded output</h4><p>{item.output}</p><p>{item.outputTimeLabel}</p></div>}
    {item.workflowAction && <button type="button" className="button" onClick={() => onAction(item.workflowAction.command)}>{item.workflowAction.label}</button>}
    {item.replacementAction && <button type="button" className="button" onClick={() => onAction(item.replacementAction.command)}>{item.replacementAction.label}</button>}
  </section>;
}
