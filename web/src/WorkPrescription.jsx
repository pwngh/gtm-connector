import {renewalFixture,fixturePeople,walkthroughPrincipals} from './renewalFixtures.js';
import React from 'react';
import PlanChanges from './PlanChanges';
import {FileText, Pencil} from 'lucide-react';

// A consultation-style prescription: a named document, specifications, and staff instructions.
export function WorkPrescriptionFields({edit, setEdit, revision, currentPlan, onCancel, onSubmit}) {
  const field=(key,value)=>setEdit({...edit,[key]:value});
  const labels={title:'document task',owner:'task owner',specifications:'document specifications',instructions:'task instructions'};
  const changedFields=Object.keys(labels).filter(key=>(edit[key]||'').trim()!==(currentPlan?.[key]||'').trim());
  const complete=Object.keys(labels).every(key=>(edit[key]||'').trim());
  const canSave=complete&&changedFields.length>0;
  return <form className="dialog-body prescription-form" onSubmit={event=>{if(!canSave){event.preventDefault();return;}onSubmit(event);}}>
    <div className="edit-mode-label"><Pencil size={14}/> Editing plan version {revision}</div>
    <div className="form-context"><FileText size={18}/><div><strong>{renewalFixture.agreement.label}</strong><span>{renewalFixture.account.name} · Customer counsel approval</span></div></div>
    <p>Define the document and the work needed to prepare it for review.</p>
    <fieldset className="form-group"><legend>The work and its owner</legend><div className="form-grid">
      <label className="field"><span className="field-title">Document task <span className="field-required">(required)</span></span><input required maxLength={160} value={edit.title||''} onChange={e=>field('title',e.target.value)}/></label>
      <label className="field"><span className="field-title">Document task owner <span className="field-required">(required)</span></span><select required value={walkthroughPrincipals.find(p=>p.displayName===edit.owner)?.id||''} onChange={e=>{const principal=walkthroughPrincipals.find(p=>p.id===e.target.value);setEdit({...edit,owner:principal?.displayName||'',ownerId:principal?.id||null})}}>{[fixturePeople.accountOwner,fixturePeople.internalReviewer,fixturePeople.otherAccountOwner].map(n=>walkthroughPrincipals.find(p=>p.displayName===n)).map(p=><option key={p.id} value={p.id}>{p.displayName}</option>)}</select></label>
    </div></fieldset>
    <fieldset className="form-group"><legend>Document requirements</legend>
      <label className="field"><span className="field-title">Document specifications <span className="field-required">(required)</span></span><span className="field-hint">What must be true of the document you receive?</span><textarea required maxLength={1200} rows={3} value={edit.specifications||''} onChange={e=>field('specifications',e.target.value)}/></label>
      <label className="field"><span className="field-title">Instructions for the task owner <span className="field-required">(required)</span></span><span className="field-hint">Where to obtain it and what to do before handing it over.</span><textarea required maxLength={1200} rows={3} value={edit.instructions||''} onChange={e=>field('instructions',e.target.value)}/></label>
    </fieldset>
    <PlanChanges before={currentPlan} after={edit}/>
    <p className="small-note">Saving creates a new version for internal approval. Changing specifications also requires checking the document again. Earlier instructions remain in history.</p>
    <div className="form-savebar"><p className="change-summary" role="status" aria-live="polite">{changedFields.length?`Changed: ${changedFields.map(key=>labels[key]).join(', ')}.`:'No changes yet.'}</p><div className="dialog-actions"><button type="button" className="button ghost" onClick={onCancel}>Cancel</button><button className="button primary" type="submit" disabled={!canSave}>Save plan version</button></div></div>
  </form>;
}

export function PrescriptionDetails({plan}) {
  return <dl className="prescription-details"><div><dt>Document specifications</dt><dd>{plan.specifications}</dd></div><div><dt>Task instructions</dt><dd>{plan.instructions}</dd></div></dl>;
}
