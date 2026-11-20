import React, {useEffect, useId, useLayoutEffect, useRef, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {BookOpen, FileText, GitBranch, ShieldCheck} from 'lucide-react';
import {getPlaybookTemplate, playbookTemplates} from './playbookTemplates.js';
import {
  buildPlaybookContext, buildTemplateWork, changedDraftFields, describeDocument, draftAutomaticItems,
  draftChangeLabels, draftFields, draftResponsibilityError, getContextSource, inspectPlaybookDraft,
  isDraftContextCurrent, playbookSearch, preparedDraftLocation, readPlaybookLocation,
} from './playbookDrafts.js';
import {confirmDraftNavigation, focusFirstDraftError, focusInView} from './playbooksBrowser.js';
import './playbooks.css';

const icons = {obtain_current_rider: FileText, review_renewal_condition: ShieldCheck, reconcile_approval_conflict: GitBranch};
const roleLabels = {actionOwner: 'Action owner', internalReviewer: 'Internal reviewer', customerAuthority: 'Customer authority'};
const proofLabels = {unit: 'Cited source unit', receipt: 'Artifact receipt', attestation: 'Labelled reviewer attestation'};
const readable = value => value ? value.replaceAll('_', ' ') : 'Not recorded';

function VersionLine({template, context, draft}) {
  return <dl className="pb-versions">
    <div><dt>Template version</dt><dd>{template.version}</dd></div>
    <div><dt>New work-plan revision</dt><dd>{draft ? `${draft.revision} · unapproved draft` : 'Not prepared'}</dd></div>
    <div><dt>Document version</dt><dd>{context.document?.version || 'Not known'}</dd></div>
  </dl>;
}

function ContextSummary({context, snapshot = false}) {
  const document = describeDocument(context.document, context.documentVersionBasis);
  return <section className="pb-section">
    <h3>{snapshot ? 'Confirmed context snapshot' : 'This synthetic renewal'}</h3>
    <dl className="pb-records">
      <div><dt>Renewal</dt><dd>{context.accountName} · Opportunity {context.refs.renewalId}</dd></div>
      <div><dt>Agreement</dt><dd>{context.agreement.label} · {context.agreement.title}</dd></div>
      <div><dt>Requirement</dt><dd>{context.requirement.title}<span className="pb-detail">{context.refs.requirementId} · {readable(context.requirement.status)}</span></dd></div>
      <div><dt>Existing work</dt><dd>Plan revision {context.existingPlan.revision} · {readable(context.existingPlan.status)}<span className="pb-detail">Separate from this draft. No replacement or approval is implied.</span></dd></div>
      {context.existingPlan.reviewNote && <div><dt>Existing review feedback</dt><dd>{context.existingPlan.reviewNote}<span className="pb-detail">Latest review event: {context.planReviews.at(-1)?.at || 'Time not recorded'}</span></dd></div>}
      <div><dt>Document</dt><dd>{document.label}<span className="pb-detail">Received version: {context.document?.version || 'unknown'} · Current version: {document.currentVersion || 'basis not recorded'}</span>
        {document.versionSource && <span className="pb-detail">Separate version source: {document.versionSource}</span>}</dd></div>
      <div><dt>Customer decision</dt><dd>{context.customerReview ? `${readable(context.customerReview.status)} in the existing walkthrough` : 'No decision recorded'}
        {context.customerReview && <span className="pb-detail">Document {context.customerReview.documentVersion || 'unknown'}. An existing walkthrough decision does not establish an authority basis for this draft. Draft edits do not change that decision.</span>}</dd></div>
      <div><dt>Commercial renewal</dt><dd>{readable(context.commercialStatus)}</dd></div>
    </dl>
    <details className="pb-sources">
      <summary>Inspect the synthetic source statements</summary>
      {context.evidence.length ? context.evidence.map(source => <article key={source.id}>
        <p className="pb-source-meta">{source.id} · {source.type} · {source.speaker} · {source.time}</p>
        <blockquote>{source.quote}</blockquote>
        <p className="pb-detail">Walkthrough speaker role: {source.role} · {source.agreement}. A role is not decision authority.</p>
        {source.detail && <aside className="pb-workflow-note"><strong>Workflow note</strong><p>{source.detail}</p></aside>}
      </article>) : <p>No source statement is available. The work must acquire it rather than infer its contents.</p>}
    </details>
    <div className="pb-context-links"><Link to={context.paths.evidence}>Open Evidence</Link><Link to={context.paths.workplan}>Inspect existing work plan</Link></div>
    {snapshot && <p className="pb-detail">The snapshot does not update when the walkthrough changes. Links open current records; this draft stays in this tab.</p>}
  </section>;
}

function Responsibility({context}) {
  return <section className="pb-section">
    <h3>Separate responsibilities</h3>
    <dl className="pb-records">
      <div><dt>Action owner</dt><dd>{context.roles.actionOwner || 'Unassigned; identify an owner before releasing work'}</dd></div>
      <div><dt>Internal reviewer</dt><dd>{context.roles.internalReviewer || 'Unassigned'}<span className="pb-detail">Must be independent of the action owner. A proposed reviewer does not record a review or act for the customer.</span></dd></div>
      <div><dt>Customer authority</dt><dd>{context.roles.customerAuthority ? `${context.roles.customerAuthority} · recorded contact (authority record)` : 'Unidentified'}<span className="pb-detail">Record who decides, with evidence. A name or role is not an authority basis or a recorded approval.</span></dd></div>
    </dl>
  </section>;
}

function TaskSequence({items, context, scope}) {
  const title = id => items.find(item => item.id === id)?.title || readable(id);
  return <section className="pb-section">
    <h3>Proposed work and dependencies</h3>
    <p className="pb-section-note">These are instructions for future work. No item has been released or completed, and no customer request has been sent.</p>
    <ol className="pb-tasks">{items.map(item => <li key={item.id}>
      <details id={`${scope}-${item.id}`}>
        <summary><span className="pb-task-title">{item.title}</span><span className="pb-detail">{roleLabels[item.role]} · {context.roles[item.role] || 'Unassigned'} · {item.kind === 'customer_request' ? 'Customer-facing request' : 'Internal task'}</span></summary>
        <div className="pb-task-body">
          {item.addedBecauseOf && <p className="pb-detail">Auto-added because of: {title(item.addedBecauseOf)}. Removable in the draft editor; no further items are added from this item.</p>}
          <dl className="pb-records pb-dependencies">
            <div><dt>Requires all</dt><dd>{item.dependencies.all.length ? item.dependencies.all.map(title).join('; ') : 'No prior item in this draft.'}</dd></div>
            {item.dependencies.oneOf.map((group, index) => <div key={index}><dt>Requires one, group {index + 1}</dt><dd>{group.map(title).join(' OR ')}</dd></div>)}
            {item.dependencies.excludes.length > 0 && <div><dt>Excludes</dt><dd>{item.dependencies.excludes.map(title).join('; ')}</dd></div>}
          </dl>
          <dl className="pb-records">
            <div><dt>Inputs</dt><dd><ul>{item.inputs.map((input, index) => <li key={`${input.ref}-${index}`}>{input.label}<span className="pb-detail">{input.mode === 'required' ? 'Required for this work' : 'Allowed context'} · {input.ref}</span></li>)}</ul></dd></div>
            <div><dt>Expected output</dt><dd>{item.output}</dd></div>
            <div><dt>Completion proof</dt><dd><strong>{proofLabels[item.completionProof.kind]}</strong><p>{item.completionProof.description}</p>
              {item.completionProof.kind === 'receipt' && <span className="pb-detail">{item.completionProof.inspection === 'accepted' ? 'Accepted inspection required for this item.' : 'Collection completes on receipt. Inspection is separate.'}</span>}</dd></div>
          </dl>
        </div>
      </details>
    </li>)}</ol>
  </section>;
}

function WorkDetails({work, template, context, scope, specifications, instructions}) {
  function inspectTask(event, taskId) {
    event.preventDefault();
    const details = document.getElementById(`${scope}-${taskId}`);
    if (!details) return;
    details.open = true;
    focusInView(details.querySelector('summary'));
  }
  const originalsPresent = ['customer_statement', 'comparison_statement'].every(binding => {
    const source = getContextSource(context, binding);
    return source?.quote && source?.context;
  });
  return <>
    {template.id === 'reconcile_approval_conflict' && <div className="pb-callout">
      <strong>An apparent mismatch, not two approvals.</strong>
      <p>{originalsPresent ? 'The customer condition and comparison statement must be compared by scope and authority. A seller forecast is not a second approval. Establish whether any real contradiction exists; do not invent one.' : 'One or both original statements are missing from this snapshot. Recover them before assessing a contradiction.'}</p>
    </div>}
    <section className="pb-section">
      <h3>Evidence, unknowns, and prerequisites</h3>
      <p className="pb-section-note">Gaps can become bounded work. Required sources are for completing proposed work, not preparing a draft. Expected outputs are not evidence prerequisites.</p>
      <ul className="pb-needs">{[...work.prerequisites, ...work.evidenceNeeds].map(need => <li key={need.id}>
        <div className="pb-need-heading"><strong>{need.label}</strong><span>{need.status}</span></div>
        {need.mode && <span className="pb-detail">{need.output ? 'Expected output' : need.mode === 'required' ? 'Required source for completion' : 'Allowed context'}</span>}
        <p>{need.detail}</p>
        {need.recoveryTaskId ? <a className="pb-task-link" href={`#${scope}-${need.recoveryTaskId}`} onClick={event => inspectTask(event, need.recoveryTaskId)}>Inspect next step: {work.items.find(item => item.id === need.recoveryTaskId)?.title}</a>
          : <p className="pb-detail">No recovery item is included for this prerequisite. The prerequisite remains open.</p>}
      </li>)}</ul>
    </section>
    <Responsibility context={context}/>
    {specifications !== undefined && <section className="pb-section">
      <h3>Specifications and instructions</h3>
      <dl className="pb-records pb-prescription"><div><dt>Specifications</dt><dd>{specifications}</dd></div><div><dt>Instructions</dt><dd>{instructions}</dd></div></dl>
    </section>}
    <TaskSequence items={work.items} context={context} scope={scope}/>
    <section className="pb-section pb-output">
      <span className="pb-kicker">EXPECTED OUTPUT</span><h3>{work.output?.title || template.output.title}</h3>
      <p>{work.output?.description || template.output.description}</p>
      <h4>Proof that completes this work</h4>
      <ul>{(work.completionRules || template.completionRules).map(rule => <li key={rule.id}>{rule.text}</li>)}</ul>
      <div className="pb-boundary"><strong>Human review boundary</strong><p>{work.reviewBoundary || template.reviewBoundary}</p></div>
    </section>
  </>;
}

function ConfirmContext({template, workflowState, evidence, onPrepare, onBack, headingRef}) {
  const id = useId();
  const [owner, setOwner] = useState(() => buildPlaybookContext(workflowState, evidence).roles.actionOwner || '');
  const [reviewer, setReviewer] = useState('');
  const [authority, setAuthority] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const context = buildPlaybookContext(workflowState, evidence, {actionOwner: owner, internalReviewer: reviewer, customerAuthority: authority});
  const roleError = draftResponsibilityError(context);
  return <>
    <span className="pb-kicker">BEFORE PREPARING A DRAFT</span>
    <h2 ref={headingRef} tabIndex={-1}>Confirm the synthetic context</h2>
    <p className="pb-lead">{template.title} · Template {template.version}. Only this invented renewal is available in the local workbench.</p>
    <ContextSummary context={context}/>
    <form onSubmit={event => {event.preventDefault(); if (confirmed) onPrepare(context);}}>
      <fieldset className="pb-fields"><legend>Who would do and review the work?</legend>
        <label htmlFor={`${id}-owner`}>Proposed action owner</label>
        <select id={`${id}-owner`} name="actionOwner" value={owner} onChange={event => setOwner(event.target.value)}>
          <option value="">Unassigned; identify an owner</option>{context.roleOptions.actionOwners.map(name => <option key={name} value={name}>{name} · walkthrough fixture</option>)}
        </select>
        <p className="pb-field-hint">Only people tied to this renewal are offered. If unknown, a removable owner-assignment proposal is added.</p>
        <label htmlFor={`${id}-reviewer`}>Proposed internal reviewer</label>
        <select id={`${id}-reviewer`} name="internalReviewer" value={reviewer} onChange={event => setReviewer(event.target.value)} aria-invalid={Boolean(roleError)} aria-describedby={`${id}-reviewer-hint`}>
          <option value="">Unassigned</option>{context.roleOptions.internalReviewers.map(name => <option key={name} value={name}>{name} · walkthrough fixture</option>)}
        </select>
        <p className={roleError ? 'pb-field-error' : 'pb-field-hint'} id={`${id}-reviewer-hint`}>{roleError || 'Choose someone other than the action owner, or leave the reviewer unassigned. No review or authorization is recorded here.'}</p>
        <label htmlFor={`${id}-authority`}>Customer decision authority</label>
        <select id={`${id}-authority`} name="customerAuthority" value={authority} onChange={event => setAuthority(event.target.value)} aria-describedby={`${id}-authority-hint`}>
          <option value="">Unidentified</option>{context.roleOptions.customerAuthorityOptions.map(name => <option key={name} value={name}>{name} · recorded contact (authority record)</option>)}
        </select>
        <p className="pb-field-hint" id={`${id}-authority-hint`}>Only a contact recorded from an accepted signed letter is offered. Record who decides, with evidence, in the Work plan.</p>
      </fieldset>
      <label className="pb-confirm"><input type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} required/><span>I confirm this invented {context.agreement.label} context. Preparing a separate draft will not change the existing plan or send anything.</span></label>
      <div className="pb-actions"><button type="button" className="pb-button" onClick={onBack}>Back to template</button><button type="submit" className="pb-button pb-primary" disabled={!confirmed || Boolean(roleError)} aria-describedby={`${id}-prepare-hint`}>Prepare separate draft</button></div>
      <p className="pb-field-hint" id={`${id}-prepare-hint`}>{roleError || (!confirmed ? 'Confirm the invented context to prepare a draft.' : 'Preparation only creates a temporary, unapproved draft.')}</p>
    </form>
  </>;
}

function DraftEdits({session, dispatch, onInspect, onDelete, onBack}) {
  const id = useId();
  const formRef = useRef(null);
  const changes = changedDraftFields(session.draft, session.editor);
  useLayoutEffect(() => {focusFirstDraftError(formRef.current, session.fieldErrors);}, [session.validationAttempt]);
  return <form ref={formRef} className="pb-edit-form" noValidate onSubmit={event => {
    event.preventDefault();
    dispatch({type: 'save_revision', expectedRevision: session.editorRevision, at: new Date().toISOString()});
  }}>
    <fieldset className="pb-fields"><legend>Adapt this draft, not the reusable template</legend>
      {Object.entries(draftFields).map(([field, definition]) => {
        const error = session.fieldErrors[field];
        const props = {id: `${id}-${field}`, name: field, required: true, maxLength: definition.maxLength, value: session.editor[field],
          'aria-invalid': Boolean(error), 'aria-describedby': error ? `${id}-${field}-error` : undefined,
          onChange: event => dispatch({type: 'edit_field', field, value: event.target.value})};
        return <div className="pb-edit-field" key={field}>
          <label htmlFor={props.id}>{definition.label} <span>(required)</span></label>
          {field === 'title' ? <input {...props}/> : <textarea {...props} rows={5}/>}
          {error && <p className="pb-field-error" id={`${id}-${field}-error`}>{error}</p>}
        </div>;
      })}
    </fieldset>
    <fieldset className="pb-additions"><legend>Auto-added items</legend>
      <p>These proposals go one level deep. Remove or restore them before saving. Removing a review or assignment item never authorizes work or supplies an owner.</p>
      {draftAutomaticItems(session.draft).map(item => <label className="pb-confirm" key={item.id}>
        <input type="checkbox" checked={!session.editor.removedItemIds.includes(item.id)} onChange={() => dispatch({type: 'toggle_auto_item', id: item.id})}/>
        <span>{item.title}<span className="pb-detail">Added because of: {session.draft.items.find(source => source.id === item.addedBecauseOf)?.title}. Uncheck to remove.</span></span>
      </label>)}
    </fieldset>
    <p className="pb-edit-summary" role="status">{changes.length ? `Unsaved changes: ${changes.map(field => draftChangeLabels[field].toLowerCase()).join(', ')}. Save explicitly to create revision ${session.draft.revision + 1}.` : `No unsaved changes. Saved revision ${session.draft.revision} is current.`}</p>
    <p className="pb-field-hint">Specifications refine the deliverable; instructions guide the owner. Neither grants authority or changes completion proof. Inspect is read-only and shows the last saved revision.</p>
    <div className="pb-actions">
      <button type="button" className="pb-button" onClick={() => dispatch({type: 'cancel_edits'})}>Cancel</button>
      <button type="button" className="pb-button" onClick={onInspect}>Inspect draft</button>
      <button type="submit" className="pb-button pb-primary">Save revision {session.draft.revision + 1}</button>
    </div>
    <div className="pb-secondary-actions"><button type="button" className="pb-link-button" onClick={onBack}>Back to template</button><button type="button" className="pb-link-button" onClick={onDelete}>Delete draft</button></div>
  </form>;
}

function HumanEdits({draft}) {
  const valueText = value => Array.isArray(value) ? value.join(', ') || 'None' : value;
  return <details className="pb-provenance">
    <summary>Inspect draft provenance and human edits</summary>
    <dl className="pb-records"><div><dt>Local draft ID</dt><dd>{draft.id}</dd></div><div><dt>Template</dt><dd>{draft.templateRef.id} · {draft.templateRef.version}</dd></div><div><dt>Prepared</dt><dd>{draft.createdAt}</dd></div><div><dt>Schema</dt><dd>Draft schema {draft.schemaVersion} · temporary, in-memory only</dd></div></dl>
    {draft.humanEdits.length ? draft.humanEdits.map(edit => <section className="pb-human-edit" key={edit.revision}>
      <h4>Draft revision {edit.revision}</h4><p className="pb-detail">{edit.at} · {edit.actor}</p>
      {edit.changes.map(change => <div key={change.field}><strong>{draftChangeLabels[change.field]}</strong><dl className="pb-records pb-prescription"><div><dt>Before</dt><dd>{valueText(change.before)}</dd></div><div><dt>After</dt><dd>{valueText(change.after)}</dd></div></dl></div>)}
    </section>) : <p>No edits have been saved. The confirmed context and role choices are retained in this draft snapshot.</p>}
    <p className="pb-field-hint">This is not a durable audit log or an authenticated review record. The reusable template remains unchanged.</p>
  </details>;
}

export default function Playbooks({workflowState, evidence, session, dispatch}) {
  const [params, setParams] = useSearchParams();
  const page = readPlaybookLocation(params, session);
  const template = getPlaybookTemplate(page.templateId);
  const liveContext = buildPlaybookContext(workflowState, evidence);
  const draftView = ['edit', 'inspect'].includes(page.view);
  const draft = draftView ? session.draft : null;
  const context = draft?.contextSnapshot || liveContext;
  const work = draft || buildTemplateWork(template.id, context);
  const scope = useId();
  const headingRef = useRef(null), errorRef = useRef(null), deleteRef = useRef(null), deleteOpener = useRef(null);
  const pendingPreparation = useRef(null);
  const [deleting, setDeleting] = useState(false);
  const focusKey = `${params.toString()}|${session.draft?.id || ''}`;
  const previousFocusKey = useRef(focusKey);
  const inspectionError = page.view === 'inspect' ? inspectPlaybookDraft(draft).error : null;

  useLayoutEffect(() => {
    const previous = pendingPreparation.current;
    if (!previous || previous === session) return;
    pendingPreparation.current = null;
    const location = preparedDraftLocation(previous, session);
    if (location) setParams(location, {preventScrollReset: true});
  }, [session, setParams]);
  useLayoutEffect(() => {
    if (previousFocusKey.current === focusKey) return;
    previousFocusKey.current = focusKey;
    setDeleting(false);
    focusInView(headingRef.current);
  }, [focusKey]);
  useLayoutEffect(() => {
    if (session.error && !Object.values(session.fieldErrors).some(Boolean)) focusInView(errorRef.current);
  }, [session.error, session.validationAttempt]);
  useLayoutEffect(() => {
    if (deleting) focusInView(deleteRef.current);
    else if (deleteOpener.current?.restoreOnClose) {
      const opener = deleteOpener.current.element;
      deleteOpener.current.restoreOnClose = false;
      focusInView(opener?.isConnected ? opener : headingRef.current);
    }
  }, [deleting]);
  useEffect(() => {
    if (!deleting) return;
    const close = event => {if (event.key === 'Escape') {event.preventDefault(); keepDraft();}};
    document.addEventListener('keydown', close);
    return () => document.removeEventListener('keydown', close);
  }, [deleting]);

  function go(view, selected = template.id, draftId = null) {
    const next = playbookSearch(selected, view, draftId);
    if (next.toString() !== params.toString() && !confirmDraftNavigation(session)) return;
    dispatch({type: 'clear_error'});
    setParams(next, {preventScrollReset: true});
  }
  function prepare(contextSnapshot) {
    pendingPreparation.current = session;
    dispatch({type: 'prepare', templateId: template.id, context: contextSnapshot, confirmed: true, at: new Date().toISOString()});
  }
  function inspect() {
    dispatch({type: 'inspect'});
    if (inspectPlaybookDraft(session.draft).ok) go('inspect', session.draft.templateRef.id, session.draft.id);
  }
  function requestDelete(event) {
    deleteOpener.current = {element: event.currentTarget, restoreOnClose: false};
    setDeleting(true);
  }
  function keepDraft() {if (deleteOpener.current) deleteOpener.current.restoreOnClose = true; setDeleting(false);}
  function deleteDraft() {
    deleteOpener.current = null;
    dispatch({type: 'delete_confirmed'});
    setDeleting(false);
    setParams(playbookSearch(template.id), {preventScrollReset: true});
  }
  const resume = () => go('edit', session.draft.templateRef.id, session.draft.id);

  return <div className="pb-page">
    {page.view === 'template' && <section className="pb-intro" aria-label="Playbooks workbench">
      <div><span className="pb-kicker">A REUSABLE START, NOT AN APPROVAL</span><h2>Define the work.<br/>Keep the evidence.</h2><p>Choose a pattern. Inspect what it needs. Prepare a separate draft for this renewal.</p></div>
      <img src="/images/ordered-evidence.png" alt="" aria-hidden="true" width="1536" height="1024"/>
    </section>}
    <div className="pb-simulation"><BookOpen size={20} aria-hidden="true"/><div><strong>Local simulation · one temporary draft</strong><p>Drafts and unsaved edits stay in this tab during in-app navigation. Reloading or closing the tab discards them. Unsaved edits trigger a reload or close warning. Nothing is submitted, sent, approved, or persisted.</p></div></div>
    {session.notice && <p className="pb-notice" role="status">{session.notice}</p>}
    <div className="pb-workbench">
      <aside className="pb-library" aria-label="Reusable playbooks">
        <div className="pb-library-heading"><span className="pb-kicker">CHOOSE THE WORK</span><h2>Reusable playbooks</h2><p>Selection only inspects a template.</p></div>
        <ul>{playbookTemplates.map(item => {const Icon = icons[item.id]; return <li key={item.id}><button type="button" aria-current={item.id === template.id ? 'page' : undefined} aria-controls={`${scope}-details`} onClick={() => go('template', item.id)}>
          <Icon size={21} aria-hidden="true"/><span><strong>{item.title}</strong><span>{item.summary}</span></span>
        </button></li>;})}</ul>
        {session.draft && !draftView && <div className="pb-retained"><strong>A separate draft is retained</strong><p>{session.draft.title}</p><p className="pb-detail">Draft revision {session.draft.revision}. Resume or delete it before preparing another.</p><button type="button" className="pb-button" onClick={resume}>Resume draft</button><button type="button" className="pb-link-button" onClick={requestDelete}>Delete draft</button></div>}
        <p className="pb-library-foot">Template versions describe reusable patterns. Draft revisions describe your instructions. Document versions identify files.</p>
      </aside>
      <section className={`pb-folio ${page.view === 'edit' ? 'pb-editing' : ''}`} id={`${scope}-details`} aria-label="Selected playbook and draft">
        {page.invalidSelection && <p className="pb-callout" role="status">That template or view is not available. Showing a supported template; no draft was created.</p>}
        {session.error && <div className="pb-error" role="alert" tabIndex={-1} ref={errorRef}><strong>{session.error}</strong><button type="button" className="pb-link-button" onClick={() => dispatch({type: 'clear_error'})}>Dismiss error</button></div>}
        {deleting && <section className="pb-cancel" aria-labelledby={`${scope}-delete`}>
          <h3 id={`${scope}-delete`} ref={deleteRef} tabIndex={-1}>Delete this temporary draft?</h3><p>Its saved revisions and unsaved edits will be lost. The reusable template and existing work plan will not change.</p>
          <div className="pb-actions"><button type="button" className="pb-button" onClick={keepDraft}>Keep draft</button><button type="button" className="pb-button pb-primary" onClick={deleteDraft}>Delete draft permanently</button></div>
        </section>}
        {page.view === 'unavailable' && <>
          <span className="pb-kicker">TEMPORARY DRAFT UNAVAILABLE</span><h2 ref={headingRef} tabIndex={-1}>This draft is no longer available here.</h2>
          <p className="pb-lead">Draft URLs do not store draft contents. It may have been deleted, reloaded, or prepared in another tab. Existing work and reusable templates are unchanged.</p>
          <div className="pb-actions"><button type="button" className="pb-button pb-primary" onClick={() => go('template')}>Return to template</button>{session.draft && <button type="button" className="pb-button" onClick={resume}>Resume retained draft</button>}</div>
        </>}
        {page.view === 'context' && (session.draft ? <>
          <span className="pb-kicker">ONE DRAFT AT A TIME</span><h2 ref={headingRef} tabIndex={-1}>Your existing draft is still here.</h2><p className="pb-lead">Preparing another would hide your work. Resume it, or explicitly delete it before confirming a new context.</p>
          <div className="pb-actions"><button type="button" className="pb-button" onClick={() => go('template')}>Back to template</button><button type="button" className="pb-button pb-primary" onClick={resume}>Resume draft</button></div>
        </> : <ConfirmContext key={`${template.id}:${JSON.stringify(liveContext)}`} template={template} workflowState={workflowState} evidence={evidence} headingRef={headingRef} onPrepare={prepare} onBack={() => go('template')}/>)}
        {page.view === 'template' && <>
          <span className="pb-kicker">REUSABLE TEMPLATE · {template.version}</span><h2 ref={headingRef} tabIndex={-1}>{template.title}</h2><p className="pb-lead">{template.summary}</p>
          <section className="pb-section"><h3>When to use this</h3><p>{template.whenToUse}</p></section>
          <div className="pb-actions pb-start"><p id={`${scope}-start-note`}>{session.draft ? 'A draft is already retained. Resume or delete it before preparing another.' : 'Inspecting never changes the existing plan.'}</p><button type="button" className="pb-button pb-primary" disabled={Boolean(session.draft)} aria-describedby={`${scope}-start-note`} onClick={() => go('context')}>Confirm context for a draft</button></div>
          <VersionLine template={template} context={context}/><ContextSummary context={context}/>
          <WorkDetails work={work} template={template} context={context} scope={scope} specifications={template.specifications} instructions={template.instructions}/>
        </>}
        {draftView && <>
          <span className="pb-kicker">{page.view === 'edit' ? 'EDIT A SEPARATE DRAFT' : 'READ-ONLY DRAFT INSPECTION'}</span>
          <h2 ref={headingRef} tabIndex={-1}>{page.view === 'edit' ? 'Make the work specific.' : inspectionError ? 'An independent reviewer is needed.' : draft.title}</h2>
          <p className="pb-lead">From “{template.title}”. {page.view === 'edit' ? 'Edits belong only to this temporary work-plan draft.' : 'Inspect the saved proposed work, not a completed or approved outcome.'}</p>
          <VersionLine template={draft.templateRef} context={context} draft={draft}/>
          {!isDraftContextCurrent(draft, liveContext) && <div className="pb-callout" role="status" data-context-stale="true"><strong>The existing walkthrough has changed.</strong><p>This draft retains its earlier context snapshot, including review feedback and times. It is not synchronized. To use changed records, delete this draft and confirm the context again. Existing decisions remain unchanged.</p></div>}
          {page.view === 'edit' ? <>
            <DraftEdits session={session} dispatch={dispatch} onInspect={inspect} onDelete={requestDelete} onBack={() => go('template')}/>
            <ContextSummary context={context} snapshot/>
            <details className="pb-draft-work"><summary>Inspect proposed items, outputs, and completion proof</summary><WorkDetails work={draft} template={template} context={context} scope={scope}/></details>
          </> : inspectionError ? <>
            <p className="pb-error" role="alert">{inspectionError}</p>
            <div className="pb-actions"><button type="button" className="pb-button" onClick={() => go('template')}>Back to template</button><button type="button" className="pb-button" onClick={requestDelete}>Delete draft</button></div>
          </> : <>
            {changedDraftFields(draft, session.editor).length > 0 && <div className="pb-callout"><strong>Unsaved edits are still retained.</strong><p>This read-only inspection shows saved revision {draft.revision}. Back to editing resumes your changed fields. Only Save revision creates a new revision.</p></div>}
            <div className="pb-boundary"><strong>Inspection only. No review decision recorded.</strong><p>This view does not save edits, release tasks, authenticate a reviewer, send a message, approve terms, resolve a condition, or renew an agreement.</p></div>
            <div className="pb-actions"><button type="button" className="pb-button pb-primary" onClick={() => go('edit', template.id, draft.id)}>Back to editing</button><button type="button" className="pb-button" onClick={requestDelete}>Delete draft</button><button type="button" className="pb-link-button" onClick={() => go('template')}>Back to template</button></div>
            <ContextSummary context={context} snapshot/>
            <WorkDetails work={draft} template={template} context={context} scope={scope} specifications={draft.specifications} instructions={draft.instructions}/>
            <HumanEdits draft={draft}/>
          </>}
        </>}
      </section>
    </div>
  </div>;
}
