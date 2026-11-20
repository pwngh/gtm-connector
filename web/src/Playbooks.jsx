import React, {useId, useLayoutEffect, useRef, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {BookOpen, FileText, GitBranch, ShieldCheck} from 'lucide-react';
import {getPlaybookTemplate, playbookTemplates} from './playbookTemplates.js';
import {
  buildPlaybookContext, buildTemplateWork, changedDraftFields, describeDocument, draftFields,
  isDraftContextCurrent, playbookSearch, readPlaybookLocation, validateDraftEdits,
} from './playbookDrafts.js';
import './playbooks.css';

const icons = {obtain_current_rider: FileText, review_renewal_condition: ShieldCheck, reconcile_approval_conflict: GitBranch};
const roleLabels = {actionOwner: 'Action owner', internalReviewer: 'Internal reviewer', customerAuthority: 'Customer authority'};
const readable = value => value ? value.replaceAll('_', ' ') : 'Not recorded';

function VersionLine({template, context, draft}) {
  return <dl className="pb-versions">
    <div><dt>Template version</dt><dd>{template.version}</dd></div>
    <div><dt>New work-plan revision</dt><dd>{draft ? `${draft.revision} · unapproved draft` : 'Not prepared'}</dd></div>
    <div><dt>Document version</dt><dd>{context.document?.version || 'Not known'}</dd></div>
  </dl>;
}

function ContextSummary({context, snapshot = false}) {
  return <section className="pb-section">
    <h3>{snapshot ? 'Confirmed context snapshot' : 'This synthetic renewal'}</h3>
    <dl className="pb-records">
      <div><dt>Renewal</dt><dd>{context.accountName} · Opportunity {context.refs.renewalId}</dd></div>
      <div><dt>Agreement</dt><dd>Agreement {context.refs.agreementId} · AI service terms</dd></div>
      <div><dt>Requirement</dt><dd>{context.requirement.title}<span className="pb-detail">{context.refs.requirementId} · {readable(context.requirement.status)}</span></dd></div>
      <div><dt>Existing work</dt><dd>Plan revision {context.existingPlan.revision} · {readable(context.existingPlan.status)}<span className="pb-detail">Separate from this draft. No replacement or approval is implied.</span></dd></div>
      <div><dt>Document</dt><dd>{describeDocument(context.document).label}<span className="pb-detail">Received version: {context.document?.version || 'unknown'} · Current version: {context.document?.currentVersion || 'unknown'}</span></dd></div>
      <div><dt>Customer decision</dt><dd>{context.customerReview ? `${readable(context.customerReview.status)} in the existing walkthrough` : 'No decision recorded'}
        {context.customerReview && <span className="pb-detail">Document {context.customerReview.documentVersion || 'unknown'} · Existing plan revision {context.customerReview.planRevision || 'unknown'}. Not an approval of this draft.</span>}</dd></div>
      <div><dt>Commercial renewal</dt><dd>{readable(context.commercialStatus)}</dd></div>
    </dl>
    <details className="pb-sources">
      <summary>Inspect the synthetic source statements</summary>
      {context.evidence.length ? context.evidence.map(source => <article key={source.id}>
        <p className="pb-source-meta">{source.id} · {source.type} · {source.speaker} · {source.time}</p>
        <blockquote>“{source.context}”</blockquote>
        <p>{source.detail}</p><p className="pb-detail">{source.role} · {source.agreement}</p>
      </article>) : <p>No source statement is available. The work must acquire it rather than infer its contents.</p>}
    </details>
    <div className="pb-context-links"><Link to="/renewals/thornfield/evidence">Open Evidence</Link><Link to="/renewals/thornfield/workplan">Inspect existing work plan</Link></div>
    {snapshot && <p className="pb-detail">The snapshot does not update when the walkthrough changes. Links open current records; this draft stays in this tab.</p>}
  </section>;
}

function Responsibility({context}) {
  return <section className="pb-section">
    <h3>Separate responsibilities</h3>
    <dl className="pb-records">
      <div><dt>Action owner</dt><dd>{context.roles.actionOwner || 'Not assigned — identify an owner before releasing work'}</dd></div>
      <div><dt>Internal reviewer</dt><dd>{context.roles.internalReviewer}<span className="pb-detail">Reviews work scope and supporting evidence, not on behalf of the customer.</span></dd></div>
      <div><dt>Customer authority</dt><dd>{context.roles.customerAuthority ? `${context.roles.customerAuthority} · Customer counsel in the fixture` : 'Not established — identify who can decide'}<span className="pb-detail">A role or name is not authenticated authority or a recorded approval.</span></dd></div>
    </dl>
  </section>;
}

function TaskSequence({tasks, context, scope}) {
  return <section className="pb-section">
    <h3>Proposed work and dependencies</h3>
    <p className="pb-section-note">These are instructions for future work. No task has been released or completed.</p>
    <ol className="pb-tasks">{tasks.map(task => <li key={task.id}>
      <details id={`${scope}-${task.id}`}>
        <summary><span className="pb-task-title">{task.title}</span><span className="pb-detail">{roleLabels[task.role]} · {context.roles[task.role] || 'Assignment needed'}</span></summary>
        <div className="pb-task-body">
          <p className="pb-dependency"><strong>Depends on:</strong> {task.dependsOn.length ? task.dependsOn.map(id => tasks.find(item => item.id === id)?.title || id).join('; ') : 'No prior task in this draft.'}</p>
          <dl className="pb-records">
            <div><dt>Inputs</dt><dd><ul>{task.inputs.map(input => <li key={input}>{input}</li>)}</ul></dd></div>
            <div><dt>Expected output</dt><dd>{task.output}</dd></div>
            <div><dt>Completion proof</dt><dd>{task.completionRule}</dd></div>
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
    details.querySelector('summary')?.focus({preventScroll: true});
    details.scrollIntoView({block: 'start', behavior: 'instant'});
  }
  return <>
    {template.id === 'reconcile_approval_conflict' && <div className="pb-callout">
      <strong>An apparent mismatch, not two approvals.</strong>
      <p>{['E-017', 'E-042'].every(id => context.evidence.some(source => source.id === id && source.context)) ? 'E-017 states a customer condition. E-042 is a seller forecast. The comparison must establish whether any real contradiction exists; it must not invent one.' : 'One or both original statements are missing from this snapshot. Acquire them before assessing a contradiction. The supplied example concerns a customer condition and a seller forecast, not two approvals.'}</p>
    </div>}
    <section className="pb-section">
      <h3>Evidence, unknowns, and prerequisites</h3>
      <p className="pb-section-note">Gaps can become bounded work. They do not block preparing this draft.</p>
      <ul className="pb-needs">{[...work.prerequisites, ...work.evidenceNeeds].map(need => <li key={need.id}>
        <div className="pb-need-heading"><strong>{need.label}</strong><span>{need.status}</span></div>
        <p>{need.detail}</p>
        <a className="pb-task-link" href={`#${scope}-${need.recoveryTaskId}`} onClick={event => inspectTask(event, need.recoveryTaskId)}>Inspect next step: {work.tasks.find(task => task.id === need.recoveryTaskId)?.title}</a>
      </li>)}</ul>
    </section>
    <Responsibility context={context}/>
    {specifications !== undefined && <section className="pb-section">
      <h3>Specifications and instructions</h3>
      <dl className="pb-records pb-prescription"><div><dt>Specifications</dt><dd>{specifications}</dd></div><div><dt>Instructions</dt><dd>{instructions}</dd></div></dl>
    </section>}
    <TaskSequence tasks={work.tasks} context={context} scope={scope}/>
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
  const [owner, setOwner] = useState(workflowState.plan.owner || '');
  const [authority, setAuthority] = useState('Nina Patel');
  const [confirmed, setConfirmed] = useState(false);
  const context = buildPlaybookContext(workflowState, evidence, {actionOwner: owner, customerAuthority: authority});
  const owners = [...new Set([workflowState.plan.owner, 'Morgan Reed', 'Maya Chen'].filter(Boolean))];
  return <>
    <span className="pb-kicker">BEFORE PREPARING A DRAFT</span>
    <h2 ref={headingRef} tabIndex={-1}>Confirm the synthetic context</h2>
    <p className="pb-lead">{template.title} · Template {template.version}. Only this invented renewal is available in the local workbench.</p>
    <ContextSummary context={context}/>
    <form onSubmit={event => {event.preventDefault(); if (confirmed) onPrepare(context);}}>
      <fieldset className="pb-fields"><legend>Who would do and review the work?</legend>
        <label htmlFor={`${id}-owner`}>Proposed action owner</label>
        <select id={`${id}-owner`} value={owner} onChange={event => setOwner(event.target.value)}>
          <option value="">Not assigned — identify an owner</option>{owners.map(name => <option key={name}>{name}</option>)}
        </select>
        <p className="pb-field-hint">If unknown, the draft begins with an owner-assignment task for the internal reviewer.</p>
        <label htmlFor={`${id}-authority`}>Customer decision authority</label>
        <select id={`${id}-authority`} value={authority} onChange={event => setAuthority(event.target.value)}>
          <option value="Nina Patel">Nina Patel — fixture counsel, authority unconfirmed</option>
          <option value="">Not established — identify who can decide</option>
        </select>
        <p className="pb-field-hint">Alex Chen is the proposed internal reviewer. Neither an internal reviewer nor a seller forecast establishes the customer’s approval.</p>
      </fieldset>
      <label className="pb-confirm"><input type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} required/><span>I confirm this invented A-17 context. Preparing a separate draft will not change the existing plan or send anything.</span></label>
      <div className="pb-actions"><button type="button" className="pb-button" onClick={onBack}>Back to template</button><button type="submit" className="pb-button pb-primary" disabled={!confirmed}>Prepare separate draft</button></div>
    </form>
  </>;
}

function DraftEdits({session, dispatch, onInspect, onCancel, onBack}) {
  const id = useId();
  const changes = changedDraftFields(session.draft, session.editor);
  const errors = validateDraftEdits(session.editor);
  return <form className="pb-edit-form" onSubmit={event => {event.preventDefault(); onInspect();}}>
    <fieldset className="pb-fields"><legend>Adapt this draft, not the reusable template</legend>
      {Object.entries(draftFields).map(([field, definition]) => {
        const error = session.fieldErrors[field];
        const props = {id: `${id}-${field}`, required: true, maxLength: definition.maxLength, value: session.editor[field],
          'aria-invalid': Boolean(error), 'aria-describedby': error ? `${id}-${field}-error` : undefined,
          onChange: event => dispatch({type: 'edit_field', field, value: event.target.value})};
        return <div className="pb-edit-field" key={field}>
          <label htmlFor={props.id}>{definition.label} <span>(required)</span></label>
          {field === 'title' ? <input {...props}/> : <textarea {...props} rows={5}/>}
          {error && <p className="pb-field-error" id={`${id}-${field}-error`}>{error}</p>}
        </div>;
      })}
    </fieldset>
    <p className="pb-edit-summary" role="status">{changes.length ? `Changed fields: ${changes.map(field => draftFields[field].label.toLowerCase()).join(', ')}. Inspecting applies these edits to a new draft revision.` : 'No unapplied changes. Inspecting will keep this draft revision.'}</p>
    <p className="pb-field-hint">Specifications refine the deliverable; instructions guide the owner. They cannot change completion rules, grant authority, or bypass prerequisites. Blank fields must be completed before inspection.</p>
    <div className="pb-actions">
      <button type="button" className="pb-button" onClick={() => dispatch({type: 'revert_edits'})} disabled={!changes.length}>Discard edits</button>
      <button type="submit" className="pb-button pb-primary" disabled={Object.keys(errors).length > 0}>Inspect draft</button>
    </div>
    <div className="pb-secondary-actions"><button type="button" className="pb-link-button" onClick={onBack}>Back to template</button><button type="button" className="pb-link-button" onClick={onCancel}>Cancel draft</button></div>
  </form>;
}

function HumanEdits({draft}) {
  return <details className="pb-provenance">
    <summary>Inspect draft provenance and human edits</summary>
    <dl className="pb-records"><div><dt>Local draft ID</dt><dd>{draft.id}</dd></div><div><dt>Template</dt><dd>{draft.templateRef.id} · {draft.templateRef.version}</dd></div><div><dt>Prepared</dt><dd>{draft.createdAt}</dd></div><div><dt>Schema</dt><dd>Draft schema {draft.schemaVersion} · temporary, in-memory only</dd></div></dl>
    {draft.humanEdits.length ? draft.humanEdits.map(edit => <section className="pb-human-edit" key={edit.revision}>
      <h4>Draft revision {edit.revision}</h4><p className="pb-detail">{edit.at} · {edit.actor}</p>
      {edit.changes.map(change => <div key={change.field}><strong>{draftFields[change.field].label}</strong><dl className="pb-records pb-prescription"><div><dt>Before</dt><dd>{change.before}</dd></div><div><dt>After</dt><dd>{change.after}</dd></div></dl></div>)}
    </section>) : <p>No human text edits have been applied. The confirmed context and role choices are retained in this draft’s snapshot.</p>}
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
  const headingRef = useRef(null), errorRef = useRef(null), discardRef = useRef(null), cancelOpener = useRef(null);
  const [cancelling, setCancelling] = useState(false);
  const focusKey = `${params.toString()}|${session.draft?.id || ''}`;
  const previousFocusKey = useRef(focusKey);
  useLayoutEffect(() => {
    if (previousFocusKey.current === focusKey) return;
    previousFocusKey.current = focusKey;
    setCancelling(false);
    headingRef.current?.focus({preventScroll: true});
    headingRef.current?.scrollIntoView({block: 'start', behavior: 'instant'});
  }, [focusKey]);
  useLayoutEffect(() => {if (session.error) errorRef.current?.focus();}, [session.error]);
  useLayoutEffect(() => {
    if (cancelling) discardRef.current?.focus();
    else if (cancelOpener.current?.restoreOnClose) {
      const opener = cancelOpener.current.element;
      cancelOpener.current.restoreOnClose = false;
      (opener?.isConnected ? opener : headingRef.current)?.focus({preventScroll: true});
    }
  }, [cancelling]);

  function go(view, selected = template.id, draftId = null) {
    dispatch({type: 'clear_error'});
    setParams(playbookSearch(selected, view, draftId), {preventScrollReset: true});
  }
  function prepare(contextSnapshot) {
    if (session.draft) return;
    const id = `pb-${session.sessionId}-${session.nextDraftNumber}`;
    dispatch({type: 'prepare', templateId: template.id, context: contextSnapshot, confirmed: true, at: new Date().toISOString()});
    setParams(playbookSearch(template.id, 'edit', id), {preventScrollReset: true});
  }
  function inspect() {
    dispatch({type: 'save_edits', at: new Date().toISOString()});
    if (!Object.keys(validateDraftEdits(session.editor)).length) setParams(playbookSearch(template.id, 'inspect', session.draft.id), {preventScrollReset: true});
  }
  function requestCancel(event) {
    // Safari need not focus a clicked button. Capture the actual initiating control,
    // not document.activeElement, and restore only after the confirmation is removed.
    cancelOpener.current = {element: event.currentTarget, restoreOnClose: false};
    setCancelling(true);
  }
  function keepDraft() {cancelOpener.current.restoreOnClose = true; setCancelling(false);}
  function discardDraft() {
    cancelOpener.current = null;
    dispatch({type: 'discard_confirmed'});
    setCancelling(false);
    setParams(playbookSearch(template.id), {preventScrollReset: true});
  }
  const resume = () => go('edit', session.draft.templateRef.id, session.draft.id);

  return <div className="pb-page">
    {page.view === 'template' && <section className="pb-intro" aria-label="Playbooks workbench">
      <div><span className="pb-kicker">A REUSABLE START, NOT AN APPROVAL</span><h2>Define the work.<br/>Keep the evidence.</h2><p>Choose a pattern. Inspect what it needs. Prepare a separate draft for this renewal.</p></div>
      <img src="/images/ordered-evidence.png" alt="" aria-hidden="true" width="1536" height="1024"/>
    </section>}
    <div className="pb-simulation"><BookOpen size={20} aria-hidden="true"/><div><strong>Local simulation · one temporary draft</strong><p>Drafts and in-progress edits stay in this tab during in-app navigation. Reloading or closing the tab discards them. Nothing is submitted, sent, approved, or persisted.</p></div></div>
    {session.notice && <p className="pb-notice" role="status">{session.notice}</p>}
    <div className="pb-workbench">
      <aside className="pb-library" aria-label="Reusable playbooks">
        <div className="pb-library-heading"><span className="pb-kicker">CHOOSE THE WORK</span><h2>Reusable playbooks</h2><p>Selection only inspects a template.</p></div>
        <ul>{playbookTemplates.map(item => {const Icon = icons[item.id]; return <li key={item.id}><button type="button" aria-pressed={item.id === template.id} aria-controls={`${scope}-details`} onClick={() => go('template', item.id)}>
          <Icon size={21} aria-hidden="true"/><span><strong>{item.title}</strong><span>{item.summary}</span></span>
        </button></li>;})}</ul>
        {session.draft && !draftView && <div className="pb-retained"><strong>A separate draft is retained</strong><p>{session.draft.title}</p><p className="pb-detail">Draft revision {session.draft.revision}. Resume or discard it before preparing another.</p><button type="button" className="pb-button" onClick={resume}>Resume draft</button><button type="button" className="pb-link-button" onClick={requestCancel}>Cancel draft</button></div>}
        <p className="pb-library-foot">Template versions describe reusable patterns. Draft revisions describe your instructions. Document versions identify files.</p>
      </aside>
      <section className={`pb-folio ${page.view === 'edit' ? 'pb-editing' : ''}`} id={`${scope}-details`} aria-label="Selected playbook and draft">
        {page.invalidSelection && <p className="pb-callout" role="status">That template or view is not available. Showing a supported template; no draft was created.</p>}
        {session.error && <div className="pb-error" role="alert" tabIndex={-1} ref={errorRef}><strong>{session.error}</strong><button type="button" className="pb-link-button" onClick={() => dispatch({type: 'clear_error'})}>Dismiss error</button></div>}
        {cancelling && <section className="pb-cancel" aria-labelledby={`${scope}-discard`}>
          <h3 id={`${scope}-discard`} ref={discardRef} tabIndex={-1}>Discard this temporary draft?</h3><p>Its prepared text and unapplied edits will be lost. The reusable template and existing work plan will not change.</p>
          <div className="pb-actions"><button type="button" className="pb-button" onClick={keepDraft}>Keep draft</button><button type="button" className="pb-button pb-primary" onClick={discardDraft}>Discard draft</button></div>
        </section>}
        {page.view === 'unavailable' && <>
          <span className="pb-kicker">TEMPORARY DRAFT UNAVAILABLE</span><h2 ref={headingRef} tabIndex={-1}>This draft is no longer available here.</h2>
          <p className="pb-lead">Draft URLs do not store draft contents. It may have been discarded, reloaded, or prepared in another tab. Existing work and reusable templates are unchanged.</p>
          <div className="pb-actions"><button type="button" className="pb-button pb-primary" onClick={() => go('template')}>Return to template</button>{session.draft && <button type="button" className="pb-button" onClick={resume}>Resume retained draft</button>}</div>
        </>}
        {page.view === 'context' && (session.draft ? <>
          <span className="pb-kicker">ONE DRAFT AT A TIME</span><h2 ref={headingRef} tabIndex={-1}>Your existing draft is still here.</h2><p className="pb-lead">Preparing another would hide your work. Resume it, or explicitly cancel it before confirming a new context.</p>
          <div className="pb-actions"><button type="button" className="pb-button" onClick={() => go('template')}>Back to template</button><button type="button" className="pb-button pb-primary" onClick={resume}>Resume draft</button></div>
        </> : <ConfirmContext key={`${template.id}:${JSON.stringify(liveContext)}`} template={template} workflowState={workflowState} evidence={evidence} headingRef={headingRef} onPrepare={prepare} onBack={() => go('template')}/>)}
        {page.view === 'template' && <>
          <span className="pb-kicker">REUSABLE TEMPLATE · {template.version}</span><h2 ref={headingRef} tabIndex={-1}>{template.title}</h2><p className="pb-lead">{template.summary}</p>
          <section className="pb-section"><h3>When to use this</h3><p>{template.whenToUse}</p></section>
          <div className="pb-actions pb-start"><p>Inspecting never changes the existing plan.</p><button type="button" className="pb-button pb-primary" disabled={Boolean(session.draft)} onClick={() => go('context')}>Confirm context for a draft</button></div>
          <VersionLine template={template} context={context}/><ContextSummary context={context}/>
          <WorkDetails work={work} template={template} context={context} scope={scope} specifications={template.specifications} instructions={template.instructions}/>
          {!session.draft && <div className="pb-actions"><button type="button" className="pb-button pb-primary" onClick={() => go('context')}>Confirm context for a draft</button></div>}
        </>}
        {draftView && <>
          <span className="pb-kicker">{page.view === 'edit' ? 'EDIT A SEPARATE DRAFT' : 'READ-ONLY DRAFT INSPECTION'}</span>
          <h2 ref={headingRef} tabIndex={-1}>{page.view === 'edit' ? 'Make the work specific.' : draft.title}</h2>
          <p className="pb-lead">From “{template.title}”. {page.view === 'edit' ? 'Edits belong only to this temporary work-plan draft.' : 'Inspect the proposed work, not a completed or approved outcome.'}</p>
          <VersionLine template={draft.templateRef} context={context} draft={draft}/>
          {!isDraftContextCurrent(draft, liveContext) && <div className="pb-callout"><strong>The existing walkthrough has changed.</strong><p>This draft retains its earlier context snapshot. It is not synchronized. To use the changed records, cancel this draft and confirm the context again.</p></div>}
          {page.view === 'edit' ? <>
            <DraftEdits session={session} dispatch={dispatch} onInspect={inspect} onCancel={requestCancel} onBack={() => go('template')}/>
            <ContextSummary context={context} snapshot/>
            <details className="pb-draft-work"><summary>Inspect proposed tasks, outputs, and completion rules</summary><WorkDetails work={draft} template={template} context={context} scope={scope}/></details>
          </> : <>
            {changedDraftFields(draft, session.editor).length > 0 && <div className="pb-callout"><strong>Unapplied edits are still retained.</strong><p>This inspection shows prepared revision {draft.revision}. Back to editing resumes your changed fields; inspecting from the form applies them.</p></div>}
            <div className="pb-boundary"><strong>Inspection only. No review decision recorded.</strong><p>This view does not release tasks, authenticate a reviewer, send a message, approve terms, resolve a condition, or renew an agreement.</p></div>
            <div className="pb-actions"><button type="button" className="pb-button pb-primary" onClick={() => go('edit', template.id, draft.id)}>Back to editing</button><button type="button" className="pb-button" onClick={requestCancel}>Cancel draft</button><button type="button" className="pb-link-button" onClick={() => go('template')}>Back to template</button></div>
            <ContextSummary context={context} snapshot/>
            <WorkDetails work={draft} template={template} context={context} scope={scope} specifications={draft.specifications} instructions={draft.instructions}/>
            <HumanEdits draft={draft}/>
          </>}
        </>}
      </section>
    </div>
  </div>;
}
