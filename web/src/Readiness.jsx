import React, {useId, useLayoutEffect, useRef} from 'react';
import {Link, useLocation, useSearchParams} from 'react-router';
import {ArrowUpRight, Check, CheckCircle2, CircleDashed, Clock, FileText, GitBranch, History, Info, MinusCircle, ShieldCheck, X, CircleX} from 'lucide-react';
import {
  compareReadinessChecks, formatReadinessTime, readinessActionHref, readinessHref, readinessSearch,
  readinessSelectionKey, readinessStates, selectReadiness,
} from './readiness.js';
import {closeReadinessOnEscape, focusReadinessElement, moveReadinessFocus, readinessFocusTarget, restoreReadinessFocus} from './readinessBrowser.js';
import {historyHref} from './historyLocation.js';
import {sourceReadLabel} from './renewalEvidence.js';
import './readiness.css';

const icons = {passed: CheckCircle2, failed: CircleX, not_evaluated: CircleDashed, not_applicable: MinusCircle};
const plural = (count, noun) => `${count} ${noun}${count === 1 ? '' : 's'}`;

function Time({value}) {
  return <time dateTime={value || undefined}>{formatReadinessTime(value)}</time>;
}

function ResultBadge({check, stale = false}) {
  const Icon = icons[check.result];
  return <span className={`rd-result ${stale ? 'retained' : check.result}`} data-check-result={check.result}>
    <Icon size={16} aria-hidden="true" data-result-icon={check.result}/>{stale ? 'Last result: ' : ''}{check.resultLabel}
  </span>;
}

function InputVersions({versions}) {
  return <dl className="rd-inputs">{versions.map(input => <div key={input.kind}>
    <dt>{input.label}</dt><dd>{input.recorded || input.id ? <><span className="rd-record-id">{input.id || 'Record ID not recorded'}</span><span className="rd-detail">Revision {input.revision ?? 'not recorded'}{input.workItemRevision && ` · Work-item revision ${input.workItemRevision}`}{input.version && ` · Version ${input.version}`}</span></> : 'Not recorded'}</dd>
  </div>)}</dl>;
}

function UnitContent({unit, full = false}) {
  if (!unit) return <p className="rd-notice">The cited unit is not available. Its contents have not been inferred.</p>;
  if (!unit.shown) return <p className="rd-notice">Unit not shown. Reason: {unit.notShownReason || 'not recorded'}. Its contents have not been inferred.</p>;
  return <>
    {unit.kind === 'record' ? <dl className="rd-inputs rd-record-fields">{unit.fields.map(field => <div key={field.label}><dt>{field.label}</dt><dd>{field.value ?? 'Not recorded'}</dd></div>)}</dl>
      : <blockquote className="rd-quote">{unit.text}</blockquote>}
    {unit.partial && <p className="rd-label rd-cut">Partial excerpt · {unit.partialReason}</p>}
    {unit.speaker && <div className="rd-speaker"><strong>{unit.speaker.name}</strong><span>{unit.speaker.role}</span><p><span className="rd-label">Role basis</span>{unit.speaker.basis}</p><p className="rd-label">A role is not customer decision authority.</p></div>}
    {full && <dl className="rd-facts">
      <div><dt>Source event time</dt><dd><Time value={unit.eventAt}/></dd></div>
      <div><dt>Local read time</dt><dd><Time value={unit.readAt}/></dd></div>
      <div><dt>Receipt</dt><dd>{unit.receipt?.status || 'Not recorded'}<span className="rd-detail">{unit.receipt?.id || 'Receipt ID not recorded'}</span></dd></div>
    </dl>}
  </>;
}

function Requirement({item, selected, outside, entryLink, unitLink, action, onClose}) {
  const key = readinessSelectionKey({kind: 'requirement', id: item.id});
  return <article className={`rd-requirement rd-paper ${selected ? 'is-selected' : ''}`} data-requirement-id={item.id}>
    {outside && <p className="rd-notice">Selected requirement outside this filter. Your filter has not changed.</p>}
    <div className="rd-item-head"><div><span className="rd-kicker">{item.kindLabel}</span><h3>
      {entryLink('requirement', item.id, item.title, `entry:${key}`, true)}
    </h3></div>{selected && <button type="button" className="rd-icon-button" aria-label="Close requirement selection" onClick={onClose}><X size={19}/></button>}</div>
    <div className="rd-record-line"><span className={`rd-requirement-state ${item.status === 'resolved' ? 'recorded' : 'open'}`}>{item.statusLabel}</span>{item.recordedStatus !== item.status && <span className="rd-requirement-state recorded">{item.recordedStatusLabel}</span>}<span className="rd-label">{item.id} · Revision {item.revision}</span></div>
    {item.recordedStatus === 'resolved' && item.evidenceStatus === 'open' && <p className="rd-notice">The evidence requirement remains open. A walkthrough resolution does not establish the missing authority, decision citation, or version basis.</p>}
    <p className="rd-agreement rd-label">{item.agreement?.label || 'Agreement not recorded'} · {item.agreement?.provenance || 'Provenance not recorded'}</p>
    <section className="rd-section"><h4>What resolves it</h4><ul className="rd-criteria">{item.resolutionCriteria.map(criterion => <li key={criterion}>{criterion}</li>)}</ul></section>
    <dl className="rd-responsibility">
      <div><dt>Next-step owner</dt><dd>{item.nextStepOwnerLabel}</dd></div>
      <div><dt>Waiting on</dt><dd>{item.waitingLabel}{item.waitingOn && <span className="rd-detail">{item.waitingOn.basis}</span>}</dd></div>
      <div><dt>Requirement owner</dt><dd>{item.ownerLabel}</dd></div>
      <div><dt>Customer decision authority</dt><dd>{item.customerAuthorityLabel}</dd></div>
    </dl>
    <section className="rd-section"><h4>{item.recordedStatus === 'resolved' ? 'Evidence still not established' : 'Missing or not established'}</h4>
      {item.missing === null ? <p>Missing inputs have not been evaluated in this example. Their absence cannot be inferred.</p> : item.missing.length ? <ul className="rd-missing">{item.missing.map(reason => <li key={reason.code}>
        {entryLink('check', reason.checkId, reason.name, `requirement:${item.id}:missing:${reason.code}`)}
        <p>{reason.reason}</p>
      </li>)}</ul> : <p>No missing inputs are recorded for this item. This does not clear unread sources.</p>}
    </section>
    <section className="rd-section"><h4>Evidence cited by this requirement</h4>
      {item.citations.length ? item.citations.map(citation => <article className="rd-citation" key={`${citation.cardId}:${citation.n}`}>
        {unitLink(citation, `requirement:${item.id}:unit:${citation.n}`, <><FileText size={16} aria-hidden="true"/>Unit {citation.n} · {citation.unit?.sourceLabel || 'Source not recorded'}<ArrowUpRight size={16} aria-hidden="true"/></>)}
        <UnitContent unit={citation.unit}/><aside className="rd-workflow-note">{citation.context}</aside>
      </article>) : <p>No cited units are recorded. This is not evidence that a condition is absent.</p>}
    </section>
    {item.recordedDecision && <section className="rd-section rd-recorded-decision"><h4>{item.recordedDecision.label}</h4>
      <dl className="rd-inputs"><div><dt>Approving contact</dt><dd>{item.recordedDecision.contact || 'Not recorded'}</dd></div><div><dt>Cited evidence</dt><dd>{item.recordedDecision.citation || 'Not recorded'}</dd></div><div><dt>Recorded by</dt><dd>{item.recordedDecision.recordedBy || 'Not recorded'}</dd></div><div><dt>Decision time</dt><dd><Time value={item.recordedDecision.recordedAt}/></dd></div><div><dt>Document version</dt><dd>{item.recordedDecision.documentVersion || 'Not recorded'}</dd></div></dl>
      <p>The decision is a record bound to its citation. It is not a source quotation, and its evidence checks are listed above.</p>
    </section>}
    {item.resolvedAt && <p className="rd-resolution-time">Recorded resolution: <Time value={item.resolvedAt}/>. Separate work-item outputs and the commercial outcome are unchanged.</p>}
    <div className="rd-item-action"><span className="rd-kicker">{item.status === 'resolved' ? 'RECORDED STATE, NOT COMMERCIAL COMPLETION' : 'ONE NEXT CORRECTIVE STEP'}</span>
      {item.allowedActions.length ? item.allowedActions.map(allowed => action(allowed, `requirement:${item.id}:action`)) : <p>{item.actionUnavailableReason || 'Actions are withheld in this evaluation view.'}</p>}
      {item.inspectAction && action(item.inspectAction, `requirement:${item.id}:inspect`)}
      {item.reopenAction && action(item.reopenAction, `requirement:${item.id}:reopen`)}
    </div>
  </article>;
}

function CheckItem({check, selected, outside, entryLink, action, stale, onClose}) {
  const key = readinessSelectionKey({kind: 'check', id: check.id});
  return <li className={`rd-check ${selected ? 'is-selected' : ''}`} data-check-id={check.id}>
    {outside && <p className="rd-notice">Selected check outside this filter. Your filter has not changed.</p>}
    <div className="rd-check-head"><h3>{entryLink('check', check.id, check.title, `entry:${key}`, true)}</h3><ResultBadge check={check} stale={stale}/></div>
    <p className="rd-check-summary">{check.resultDetail}{check.kind === 'transition' && check.allowed === true ? ' · Allowed in walkthrough' : check.kind === 'transition' && check.result === 'failed' ? stale ? ' · Blocked at the retained evaluation' : ' · Not ready under these checks' : ''}</p>
    {check.blockedReasons.length > 0 && <ul className="rd-blocker-names" aria-label={`All blockers for ${check.title}`}>{check.blockedReasons.map(reason => <li key={reason.code}>
      {reason.checkId && reason.checkId !== check.id ? entryLink('check', reason.checkId, reason.name, `check:${check.id}:blocker:${reason.code}`) : <span>{reason.name}</span>}
    </li>)}</ul>}
    {['not_evaluated', 'not_applicable'].includes(check.result) && <p className="rd-check-explanation">{check.explanation}</p>}
    {selected && <section className="rd-check-details rd-paper" aria-label={`${check.title} evaluation`}>
      <p>{check.scope}</p>
      {!['not_evaluated', 'not_applicable'].includes(check.result) && <p className="rd-detail">{check.explanation}</p>}
      {check.blockedReasons.length > 0 && <dl className="rd-blocker-reasons">{check.blockedReasons.map(reason => <div key={reason.code}><dt>{reason.name}</dt><dd>{reason.reason}</dd></div>)}</dl>}
      <p className="rd-evaluated-at"><Clock size={16} aria-hidden="true"/><span>{check.result === 'not_evaluated' ? 'Check has not been evaluated.' : <>Inputs as of: <Time value={check.evaluatedAt}/></>}</span></p>
      <h4>Input versions for this check</h4><InputVersions versions={check.inputVersions}/>
      {check.guidance && <section className="rd-section"><h4>{check.correctiveLabel}</h4><p>{check.guidance}</p></section>}
      {check.allowedActions.map(allowed => action(allowed, `check:${check.id}:action`))}
      {check.correctiveAction && action(check.correctiveAction, `check:${check.id}:corrective`)}
      <button type="button" className="rd-inline-link" onClick={onClose}>Close check details<X size={16} aria-hidden="true"/></button>
    </section>}
  </li>;
}

function UnitInspector({selection, onClose, unitLink}) {
  const key = readinessSelectionKey(selection);
  return <section className="rd-unit-inspector rd-paper" aria-label="Selected source unit">
    <div className="rd-item-head"><div><span className="rd-kicker">EXACT SOURCE UNIT</span><h2 tabIndex={-1} data-readiness-target={key}>{selection.status === 'ready' ? `Unit ${selection.n} · ${selection.resource.sourceLabel}` : 'That source unit is not available.'}</h2></div><button type="button" className="rd-icon-button" aria-label="Close source unit" onClick={onClose}><X size={20}/></button></div>
    {selection.status === 'ready' ? <>
      <p className="rd-label">Card {selection.cardId} · {selection.resource.kind === 'record' ? 'Record unit' : 'Sentence unit'}</p>
      <UnitContent unit={selection.resource} full/>
      <section className="rd-section"><h3>Neighbouring units</h3>{selection.resource.neighbours.length ? selection.resource.neighbours.map(number => unitLink({cardId: selection.cardId, n: number}, `unit:${selection.n}:neighbour:${number}`, `Inspect unit ${number}`)) : <p>No neighbouring units were supplied in this projection. No raw context has been substituted.</p>}</section>
    </> : <p>The card and unit in this URL do not match a local record. No substitute quotation has been opened.</p>}
    <button type="button" className="rd-inline-link" onClick={onClose}>Return to citation</button>
  </section>;
}

function Coverage({coverage, document, unitLink}) {
  return <section className="rd-coverage rd-paper" aria-labelledby="readiness-coverage">
    <div className="rd-section-heading"><div><span className="rd-kicker">BOUNDARIES OF THIS EVALUATION</span><h2 id="readiness-coverage" tabIndex={-1}>Sources, reads, and gaps</h2></div><span className="rd-label">{coverage ? plural(coverage.units.length, 'numbered unit') : 'Source coverage unknown'}</span></div>
    <p className="rd-lead">Missing or unread information is never evidence that a blocker is absent.</p>
    {!coverage ? <p className="rd-notice">No source card is recorded. Coverage has not been evaluated.</p> : <>
      <p className="rd-label">{coverage.label} · {coverage.id} · Revision {coverage.revision}</p><p className="rd-provenance">{coverage.provenance}</p>
      <div className="rd-coverage-grid"><section><h3>What was read</h3><ul className="rd-sources">{coverage.sources.map(source => <li key={source.id} data-source-id={source.id}>
        <div><strong>{source.label}</strong><span className="rd-label">{sourceReadLabel(source.status)}</span></div>
        <p>{source.scope}</p><p className="rd-label">Local read time: <Time value={source.readAt}/></p>
        <div className="rd-source-links">{source.unitNumbers.map(n => unitLink({cardId: coverage.id, n}, `source:${source.id}:unit:${n}`, `Inspect unit ${n}`))}</div>
      </li>)}</ul></section>
      <section><h3>What was not read or not shown</h3><ul className="rd-gaps">{coverage.gaps.map(gap => <li key={gap.code} data-gap-code={gap.code}><CircleDashed size={18} aria-hidden="true"/><div><h4>{gap.title}</h4><p>{gap.text}</p></div></li>)}</ul></section></div>
    </>}
    <div className="rd-document-coverage"><FileText size={20} aria-hidden="true"/><div><strong>Workflow document, separate from the source card</strong>
      {document ? <p>Received version {document.version || 'unknown'} · Receipt time: <Time value={document.receivedAt}/>. {document.verificationRecorded ? 'A walkthrough inspection is recorded.' : 'A walkthrough inspection is not recorded.'} This receipt does not close the card’s source gaps.</p> : <p>No workflow document has been received. A source excerpt is not a document receipt.</p>}
    </div></div>
  </section>;
}

function EvaluationChanges({journal, entryLink, historyLink, preview}) {
  const {current, previous, changes} = journal;
  return <section className="rd-changes" aria-labelledby="readiness-changes">
    <div className="rd-section-heading"><div><span className="rd-kicker">EVALUATION RECORD</span><h2 id="readiness-changes" tabIndex={-1}>What changed, and why</h2></div><History size={22} aria-hidden="true"/></div>
    {preview && <p className="rd-detail">The saved evaluation log is unchanged by the local response example above.</p>}
    <div className="rd-evaluation-pair"><div><span className="rd-label">Latest inputs as of</span><strong><Time value={current.evaluatedAt}/></strong><span className="rd-label">{current.evaluationId}</span></div><div><span className="rd-label">Previous inputs as of</span><strong>{previous ? <Time value={previous.evaluatedAt}/> : 'Not recorded'}</strong><span className="rd-label">{previous?.evaluationId || 'No prior result has been invented.'}</span></div></div>
    <div className="rd-change-cause"><span className="rd-kicker">{changes.cause.label}</span><strong>{changes.cause.title}</strong><p>{changes.cause.detail}</p><p className="rd-label">Actor: {changes.cause.actor || 'Not recorded by the existing walkthrough'}</p>{changes.cause.id && historyLink(changes.cause.id)}</div>
    {!previous ? <p className="rd-detail">The first evaluated result is retained. Future walkthrough changes will show their requirement and check differences here.</p> : <>
      <h3>Changed requirements</h3>{changes.requirements.length ? <ul className="rd-change-list">{changes.requirements.map(change => <li key={change.id}>
        {entryLink('requirement', change.id, change.title, `change:requirement:${change.id}`)}
        {change.cleared.length > 0 && <p><strong>No longer missing in local rules: </strong>{change.cleared.map(item => item.name).join('; ')}.</p>}
        {change.added.length > 0 && <p><strong>Now missing: </strong>{change.added.map(item => item.name).join('; ')}.</p>}
        {change.updated.map(item => <p key={item.code}><strong>{item.after.name}: </strong>Before: {item.before.reason} Now: {item.after.reason}</p>)}
        <dl className="rd-diff">{change.differences.map(difference => <div key={difference.field}><dt>{difference.label}</dt><dd><span>Before: {difference.before}</span><span>Now: {difference.after}</span></dd></div>)}</dl>
      </li>)}</ul> : <p>No requirement changed in this evaluation. The inputs were rechecked; no resolution was inferred.</p>}
      <details className="rd-previous"><summary>Compare checks with the retained result</summary><ul>{compareReadinessChecks(previous, current).map(check => <li key={check.id} data-comparison-id={check.id}>
        <strong>{check.title}</strong><span>Previous: {check.beforeLabel}{check.beforeBlockerCount != null ? ` · ${plural(check.beforeBlockerCount, 'blocker')}` : ''}</span><span>Current: {check.afterLabel}{check.afterBlockerCount != null ? ` · ${plural(check.afterBlockerCount, 'blocker')}` : ''}</span>
        {check.added.map(item => <p key={`added:${item.code}`}><strong>Added blocker: {item.name}. </strong>{item.reason}</p>)}
        {check.cleared.map(item => <p key={`cleared:${item.code}`}><strong>Cleared blocker: {item.name}. </strong>{item.reason}</p>)}
        {check.updated.map(item => <p key={item.code}><strong>Changed blocker: {item.after.name}. </strong>Before: {item.before.reason} Now: {item.after.reason}</p>)}
        {check.changed && !check.updated.length && check.beforeExplanation !== check.afterExplanation && <p>Before: {check.beforeExplanation || 'Not recorded'} Now: {check.afterExplanation || 'Not recorded'}</p>}
        {!check.changed && <span>Result unchanged</span>}
      </li>)}</ul><h3>Previous input versions</h3><InputVersions versions={previous.inputVersions}/></details>
    </>}
  </section>;
}

export default function Readiness({journal, onAction, children}) {
  const [params, setParams] = useSearchParams();
  const navigation = useLocation(), id = useId();
  const view = selectReadiness(journal, params);
  const root = useRef(null), heading = useRef(null), previous = useRef(null), recovery = useRef(null), closing = useRef(false);
  const key = readinessSelectionKey(view.focusSelection);
  const baseKey = readinessSelectionKey(view.selection);
  const candidateOpener = view.unitSelection ? navigation.state?.readinessUnitOpener : navigation.state?.readinessOpener;
  const opener = candidateOpener?.selection === key ? candidateOpener : null;
  useLayoutEffect(() => {
    const prior = previous.current;
    if (!prior && navigation.state?.readinessRestoreFocus) {
      restoreReadinessFocus(root.current, navigation.state.readinessRestoreFocus, `entry:${key}`, heading.current);
    } else if (closing.current || prior?.kind === 'unit' && !view.unitSelection && prior.parentKey === key) {
      restoreReadinessFocus(root.current, prior?.opener, `entry:${prior?.parentKey || prior?.key}`, heading.current);
    } else if (key && (prior?.key !== key || prior?.fromEntry !== opener?.fromEntry || prior?.evaluationId !== journal.current.evaluationId)) {
      focusReadinessElement(readinessFocusTarget(root.current, key));
    } else if (!key && prior?.key) {
      restoreReadinessFocus(root.current, prior.opener, `entry:${prior.key}`, heading.current);
    }
    if (recovery.current) {
      restoreReadinessFocus(root.current, recovery.current, null, heading.current);
      recovery.current = null;
    }
    closing.current = false;
    previous.current = {key, kind: view.focusSelection?.kind, parentKey: view.unitSelection ? baseKey : null, opener: opener?.focusKey, fromEntry: opener?.fromEntry, evaluationId: journal.current.evaluationId};
  }, [key, navigation.key, opener?.fromEntry, journal.current.evaluationId]);

  function update(changes, state = navigation.state) {
    setParams(readinessSearch(params, changes), {preventScrollReset: true, state: {...state, readinessRestoreFocus: null}});
  }
  function closeSelection() {
    closing.current = true;
    if (view.unitSelection) update({card: null, unit: null}, {...navigation.state, readinessUnitOpener: null});
    else update({requirement: null, check: null}, {...navigation.state, readinessOpener: null});
  }
  function entryLink(kind, itemId, label, focusKey, primary = false, className) {
    const selectionKey = readinessSelectionKey({kind, id: itemId});
    return <Link key={focusKey} className={className || (primary ? 'rd-entry' : 'rd-inline-link')} to={readinessHref(params, {[kind]: itemId}, view.paths)}
      data-readiness-focus={focusKey} data-readiness-target={primary ? selectionKey : undefined} data-readiness-entry={primary || undefined} aria-current={baseKey === selectionKey ? 'true' : undefined}
      state={{...navigation.state, readinessRestoreFocus: null, readinessUnitOpener: null, readinessOpener: {selection: selectionKey, focusKey, fromEntry: navigation.key}}}>{label}</Link>;
  }
  function unitLink(citation, focusKey, label) {
    const selection = {kind: 'unit', cardId: citation.cardId, n: citation.n};
    return <Link key={focusKey} className="rd-inline-link rd-unit-link" to={readinessHref(params, {card: citation.cardId, unit: citation.n}, view.paths)} data-readiness-focus={focusKey}
      state={{...navigation.state, readinessRestoreFocus: null, readinessUnitOpener: {selection: readinessSelectionKey(selection), focusKey, fromEntry: navigation.key}}}>{label}</Link>;
  }
  function action(allowed, focusKey) {
    if (allowed.intent === 'inspect_evidence') return entryLink('check', allowed.checkId, <>{allowed.label}<ArrowUpRight size={17} aria-hidden="true"/></>, focusKey, false, 'rd-button rd-primary');
    return allowed.href ? <Link key={allowed.id} className={`rd-button${allowed.intent === 'inspect' ? '' : ' rd-primary'}`} to={readinessActionHref(allowed, params, view.paths)} data-readiness-focus={focusKey}
      state={{...navigation.state, readinessRestoreFocus: null, readinessActionOpener: focusKey}}>{allowed.label}<ArrowUpRight size={17} aria-hidden="true"/></Link>
      : <button key={allowed.id} type="button" className="rd-button" data-readiness-focus={focusKey} onClick={() => onAction(allowed.command)}>{allowed.label}</button>;
  }
  const selectedRequirementId = view.selection?.status === 'ready' && view.selection.kind === 'requirement' ? view.selection.id : null;
  const selectedCheckId = view.selection?.status === 'ready' && view.selection.kind === 'check' ? view.selection.id : null;
  const requirement = (item, outside = false) => <Requirement key={item.id} item={item} outside={outside} selected={item.id === selectedRequirementId} entryLink={entryLink} unitLink={unitLink} action={action} onClose={closeSelection}/>;
  const check = (item, outside = false) => <CheckItem key={item.id} check={item} outside={outside} selected={item.id === selectedCheckId} entryLink={entryLink} action={action} stale={view.status === 'stale'} onClose={closeSelection}/>;

  return <div className={`rd-page ${view.status === 'stale' ? 'is-stale' : ''}`} ref={root} onKeyDown={event => {if (view.focusSelection) closeReadinessOnEscape(event, closeSelection);}}>
    <div className="rd-intro"><div><span className="rd-kicker">READINESS · SYNTHETIC, BROWSER ONLY</span><h2 ref={heading} tabIndex={-1}>Know what can move next.</h2><p>Each transition has its own prerequisites. A passed check is not a signed renewal or a complete search of the sources.</p></div>{children}</div>
    <dl className="rd-position" aria-label="Three separate renewal facts">
      <div data-fact="crm"><dt>CRM stage</dt><dd>{view.crm.stageLabel}<span className="rd-detail">Read <Time value={view.crm.readAt}/></span><span className="rd-label">{view.crm.source}</span></dd></div>
      <div data-fact="readiness"><dt>Requirement readiness</dt><dd>{view.requirementCountLabel}<span className="rd-detail">{view.evidenceGapCount === null ? 'Evidence checks not evaluated' : view.evidenceGapCount ? `${plural(view.evidenceGapCount, 'evidence check')} not established` : 'Evidence checks evaluated separately'}</span><span className="rd-label">Readiness is evaluated per transition below.</span></dd></div>
      <div data-fact="commercial"><dt>Commercial outcome</dt><dd>{view.commercial.label}<span className="rd-detail">Not inferred from requirements or CRM stage.</span></dd></div>
    </dl>
    <div className="rd-evaluation-line"><span><Clock size={17} aria-hidden="true"/>{view.status === 'not_evaluated' ? 'Not evaluated in this example' : <>{view.status === 'stale' ? 'Retained inputs as of' : 'Inputs as of'}: <Time value={view.evaluatedAt}/></>}</span><button type="button" className="rd-inline-link" onClick={() => focusReadinessElement(root.current?.querySelector('#readiness-changes'))}>Compare evaluations<History size={16} aria-hidden="true"/></button></div>
    {view.status !== 'ready' && <section className="rd-response" role={view.status === 'stale' ? 'alert' : 'status'}><Info size={23} aria-hidden="true"/><div><span className="rd-kicker">LOCAL RESPONSE EXAMPLE</span><h3>{view.response.title}</h3><p>{view.response.message}</p><button type="button" className="rd-button" onClick={() => {recovery.current = 'response-summary'; update({evaluation: null});}}>Return to current evaluation</button></div></section>}
    <details className="rd-help"><summary><Info size={17} aria-hidden="true"/>How to read these checks and use the keyboard</summary><div><p>{view.policyBoundary}</p><dl className="rd-state-definitions"><div><dt>Not evaluated</dt><dd>No result is known. Blocker count is unknown, never zero.</dd></div><div><dt>Already recorded / not applicable</dt><dd>The step is already recorded in the walkthrough, or is not needed in this state.</dd></div><div><dt>Failed</dt><dd>Evaluated with named workflow or evidence blockers. Any difference from the demonstration reducer is labelled separately.</dd></div><div><dt>Passed</dt><dd>Evaluated with zero blockers in that check’s scope only.</dd></div></dl><p>Tab to an item link. Up and Down move within the list; Home and End go to its edges. Enter selects it. Escape closes the selection and returns focus. Selection, filters, and order stay in the URL.</p></div></details>
    {view.nextAction && <section className="rd-next" aria-label="Next corrective step"><GitBranch size={25} aria-hidden="true"/><div><span className="rd-kicker">NEXT CORRECTIVE STEP</span><h3>{view.nextAction.label}</h3><p>Action owner: {view.nextAction.owner?.displayName || 'Unassigned'}. {view.nextAction.intent === 'inspect_evidence' ? 'Opens the missing evidence check and its recording boundary. It does not grant authority or save a decision.' : 'Opens the exact item in the existing work plan.'}</p></div>{action(view.nextAction, 'next-action')}</section>}
    {view.location.invalid && <div className="rd-notice" role="status"><p>This URL contains an unavailable view setting. Defaults are shown; no record has been substituted.</p><button type="button" className="rd-inline-link" onClick={() => {recovery.current = 'requirement-filter'; update(Object.fromEntries(view.location.invalidFields.map(field => [field, null])));}}>Clear invalid view settings</button></div>}
    {view.selection?.status === 'not_found' && <section className="rd-selection-error rd-paper"><h2 tabIndex={-1} data-readiness-target={readinessSelectionKey(view.selection)}>That selection is not available.</h2><p>The URL does not identify one local requirement or check. No substitute item has been selected.</p><button type="button" className="rd-button" onClick={closeSelection}>Clear this selection</button></section>}
    <div className="rd-workbench">
      <section className="rd-requirements" aria-labelledby={`${id}-requirements`}>
        <div className="rd-section-heading"><div><span className="rd-kicker">THE RECORDED CONDITIONS</span><h2 id={`${id}-requirements`}>Requirements</h2></div><ShieldCheck size={22} aria-hidden="true"/></div>
        <div className="rd-tools"><label htmlFor={`${id}-requirement-filter`}>Show requirements<select id={`${id}-requirement-filter`} name="requirements" data-readiness-focus="requirement-filter" value={view.location.requirements} onChange={event => update({requirements: event.target.value})}><option value="open">Open requirements</option><option value="all">All recorded requirements</option><option value="resolved">Resolved in walkthrough</option></select></label></div>
        <p className="rd-result-count" role="status">{view.visibleRequirements === null ? 'Requirements are not recorded. Unknown is not zero.' : `${view.visibleRequirements.length} of ${plural(view.requirementCount, 'recorded requirement')} shown. This is not a complete inventory of unread sources.`}</p>
        <div className="rd-requirement-list" onKeyDown={event => moveReadinessFocus(event, event.currentTarget)}>
          {view.visibleRequirements?.map(item => requirement(item))}
          {view.visibleRequirements?.length === 0 && <section className="rd-empty rd-paper"><CircleDashed size={26} aria-hidden="true"/><h3>No requirements in this view.</h3><p>A recorded resolution does not clear the evidence gaps or confirm the renewal.</p>{view.location.requirements !== 'all' && <button type="button" className="rd-button" onClick={() => {recovery.current = 'requirement-filter'; update({requirements: 'all'});}}>Show all recorded requirements</button>}</section>}
          {view.selectedRequirementOutside && requirement(view.selectedRequirementOutside, true)}
        </div>
      </section>
      <div className="rd-check-column"><section className="rd-transition-section" aria-labelledby={`${id}-transitions`}>
        <div className="rd-section-heading"><div><span className="rd-kicker">LOCAL WORKFLOW RULES</span><h2 id={`${id}-transitions`}>Readiness by transition</h2></div><GitBranch size={22} aria-hidden="true"/></div>
        <div className="rd-tools"><label htmlFor={`${id}-check-filter`}>Check result<select id={`${id}-check-filter`} name="checkState" data-readiness-focus="check-filter" value={view.location.checkState} onChange={event => update({checkState: event.target.value})}><option value="all">All results ({view.transitionCount})</option>{readinessStates.map(state => <option key={state.id} value={state.id}>{state.label} ({view.counts[state.id]})</option>)}</select></label><label htmlFor={`${id}-order`}>Order<select id={`${id}-order`} name="order" value={view.location.order} onChange={event => update({order: event.target.value})}><option value="workflow">Workflow order</option><option value="blockers">Blockers first</option></select></label></div>
        <p className="rd-result-count" role="status">{view.visibleChecks.length} of {plural(view.transitionCount, 'transition check')} shown. Counts are checks, not requirements.</p>
        <ol className="rd-checks" onKeyDown={event => moveReadinessFocus(event, event.currentTarget)}>{view.visibleChecks.map(item => check(item))}{view.selectedCheckOutside && check(view.selectedCheckOutside, true)}</ol>
        {!view.visibleChecks.length && <section className="rd-empty"><h3>No checks match this result.</h3><p>Other states may include unknowns or open blockers.</p><button type="button" className="rd-button" onClick={() => {recovery.current = 'check-filter'; update({checkState: 'all'});}}>Show all transition checks</button></section>}
      </section>
        <section className="rd-evidence-checks" aria-labelledby={`${id}-evidence-checks`}><span className="rd-kicker">NOT ESTABLISHED BY WORKFLOW FLAGS</span><h2 id={`${id}-evidence-checks`}>Evidence checks</h2><p>These checks are required for decision recording and resolution here. Walkthrough permission alone does not establish them.</p><ul className="rd-checks" onKeyDown={event => moveReadinessFocus(event, event.currentTarget)}>{view.evidenceChecks.map(item => check(item))}</ul></section>
      </div>
    </div>
    {view.unitSelection && <UnitInspector selection={view.unitSelection} onClose={closeSelection} unitLink={unitLink}/>}
    <Coverage coverage={view.coverage} document={view.document} unitLink={unitLink}/>
    <EvaluationChanges journal={journal} entryLink={entryLink} historyLink={eventId => <Link className="rd-inline-link" data-readiness-focus="history-recording" to={readinessActionHref({href: historyHref('', {entry: eventId, view: 'recorded', as_of: eventId}, view.paths)}, params, view.paths)} state={{...navigation.state, readinessRestoreFocus: null, readinessActionOpener: 'history-recording'}}>Trace this recording in History<ArrowUpRight size={17} aria-hidden="true"/></Link>} preview={view.status !== 'ready'}/>
    <details className="rd-examples"><summary data-readiness-focus="response-summary">Local evaluation examples</summary><p>Preview an unevaluated result or a failed refresh. These controls never change workflow records, source text, or saved evaluations.</p><label htmlFor={`${id}-response`}>Simulated evaluation<select id={`${id}-response`} name="evaluation" data-readiness-focus="response-control" value={view.location.evaluation} onChange={event => update({evaluation: event.target.value === 'current' ? null : event.target.value})}>{view.examples.map(example => <option key={example.id} value={example.id}>{example.label}</option>)}</select></label></details>
    <p className="rd-footnote"><Check size={16} aria-hidden="true"/>{view.scope}</p>
  </div>;
}
