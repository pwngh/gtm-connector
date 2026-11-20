import React from 'react';
import {GitCompareArrows} from 'lucide-react';

const labels={title:'Document task',owner:'Task owner',specifications:'Document specifications',instructions:'Task instructions'};
// Compare the saved form with the user's edits. No inferred or generated history.
export default function PlanChanges({before,after}) {
  const fields=Object.keys(labels).filter(key=>(before?.[key]||'').trim()!==(after?.[key]||'').trim());
  if(!fields.length)return null;
  return <details className="plan-changes">
    <summary><GitCompareArrows size={17}/><span>Review {fields.length} {fields.length===1?'change':'changes'}</span></summary>
    <div className="plan-changes-body">{fields.map(key=><section className="field-comparison" key={key}><h4>{labels[key]}</h4><div className="comparison-pair"><div className="comparison-before"><span>Saved version</span><p>{before?.[key]||'Not specified'}</p></div><div className="comparison-after"><span>Your edits</span><p>{after?.[key]?.trim()||'Required field is empty'}</p></div></div></section>)}</div>
  </details>;
}
