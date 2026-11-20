import React, {useId, useLayoutEffect, useMemo, useRef} from 'react';
import {Link, useLocation, useSearchParams} from 'react-router';
import {ArrowUpRight, Check, Clock, Download, FileText, GitCompareArrows, History as HistoryIcon, Info, RotateCcw, X} from 'lucide-react';
import {formatHistoryTime, historyActionHref, historyCategories, historyExport, historyHref, historyScopeLabel, historySearch, selectHistory} from './history.js';
import {readinessHref} from './readiness.js';
import {closeHistoryOnEscape, focusHistoryElement, historyFocusTarget, moveHistoryFocus, restoreHistoryFocus} from './historyBrowser.js';
import './history.css';

function Time({value}) {
  return value ? <time dateTime={value}>{formatHistoryTime(value)}</time> : <span>not recorded</span>;
}

function TypedValue({value}) {
  return <div className="hs-typed-value" data-value-type={value.type}>
    {value.type === 'source_text' && value.value !== null ? <blockquote className="hs-source-text">{value.value}</blockquote> : <p className="hs-value">{value.type === 'time' ? <Time value={value.value}/> : value.display}</p>}
    {value.details?.length > 0 && <dl className="hs-reference-fields">{value.details.map(detail => <div key={detail.key}><dt>{detail.label}</dt><dd>{detail.type === 'time' ? <Time value={detail.value}/> : detail.display}</dd></div>)}</dl>}
    {value.attribution.partial && <p className="hs-label hs-warning">Partial excerpt · {value.attribution.partialReason || 'cut boundary not recorded'}</p>}
    <p className="hs-label hs-attribution">{value.attributionLabel}</p>
    <p className="hs-label hs-value-scope">{historyScopeLabel(value.scope)}</p>{value.attribution.role && <p className="hs-label">Role: {value.attribution.role}</p>}{value.attribution.basis && <><p className="hs-label">Role basis: {value.attribution.basis}</p><p className="hs-label">A role is not decision authority.</p></>}
  </div>;
}

function ValueList({values}) {
  return <dl className="hs-values">{values.map(value => <div key={value.field} data-history-field={value.field}><dt>{value.label}</dt><dd><TypedValue value={value}/></dd></div>)}</dl>;
}

function Comparison({entry, entryLink}) {
  return <section className="hs-section hs-comparison">
    <div className="hs-section-title"><h4>What changed</h4><GitCompareArrows size={20} aria-hidden="true"/></div>
    <p className="hs-muted">{entry.comparisonLabel}.{entry.comparisonEntryId && <> {entryLink(entry.comparisonEntryId, 'Inspect comparison record', `compare:${entry.id}`)}</>}</p>
    {entry.differences.length ? <div className="hs-differences">{entry.differences.map(difference => <section className="hs-difference" key={difference.field} data-diff-field={difference.field}>
      <div className="hs-difference-heading"><h5>{difference.label}</h5><span className="hs-label">{[difference.valueChanged && 'Value changed', difference.attributionChanged && 'Attribution changed', difference.scopeChanged && 'Scope changed'].filter(Boolean).join(' · ')}</span></div>
      <div className="hs-comparison-pair"><div><span className="hs-kicker">BEFORE</span><TypedValue value={difference.before}/></div><div><span className="hs-kicker">{entry.appliedCorrections.length ? 'EFFECTIVE PROJECTION AT KNOWN-BY POINT' : entry.type === 'correction' ? 'CORRECTION RECORDED' : 'AT THIS RECORDING'}</span><TypedValue value={difference.after}/></div></div>
    </section>)}</div> : <p className="hs-muted">{entry.complete ? 'No typed value changed in this comparison. A request, decision, or output can still be a separate event.' : 'A typed comparison is not available. Missing fields have not been filled from today’s records.'}</p>}
    <details className="hs-disclosure"><summary>All retained values for this entry</summary><ValueList values={entry.values}/></details>
  </section>;
}

function Impacts({entry, actionLink, paths}) {
  return <section className="hs-section">
    <h4>Why it mattered</h4>
    {entry.appliedCorrections.length > 0 && <p className="hs-label">Effects of the corrected projection under the retained rule version. The original effects are preserved separately above.</p>}
    {entry.trigger && <p className="hs-impact-trigger">{entry.trigger}</p>}
    {!entry.impacts.known ? <p className="hs-notice">The full before and after inputs or their rule version were not recorded. Historical checks cannot be re-evaluated safely. No invalidation has been guessed from event text.</p> : <>
      {entry.impacts.effects.length ? <ul className="hs-effects">{entry.impacts.effects.map(effect => <li key={effect.id} data-effect={effect.id} data-effect-status={effect.status}>
        <span className={`hs-effect-state ${effect.status}`}>{effect.status === 'invalidated' ? 'Needs review' : effect.status === 'kept' ? 'Kept' : 'Recorded'}</span>
        <div><strong>{effect.description}</strong>{actionLink({id: effect.id, label: 'Inspect current readiness check', href: readinessHref('', {check: effect.checkId}, paths)}, `effect:${entry.id}:${effect.id}`)}</div>
      </li>)}</ul> : <p className="hs-muted">No existing approval or inspection changed under these local rules.</p>}
      {entry.impacts.checks.length > 0 && <details className="hs-disclosure"><summary>Compare changed Readiness checks ({entry.impacts.checks.length})</summary><ul className="hs-check-changes">{entry.impacts.checks.map(check => <li key={check.id} data-check-comparison={check.id}>
        <strong>{check.title}</strong><p>Before: {check.beforeLabel} · After: {check.afterLabel}</p>
        {check.added.map(reason => <p key={`added:${reason.code}`}><strong>Added blocker: {reason.name}. </strong>{reason.reason}</p>)}
        {check.cleared.map(reason => <p key={`cleared:${reason.code}`}><strong>Cleared blocker: {reason.name}. </strong>{reason.reason}</p>)}
        {check.updated.map(reason => <p key={reason.code}><strong>Changed blocker: {reason.after.name}. </strong>Before: {reason.before.reason} Now: {reason.after.reason}</p>)}
        {!check.added.length && !check.cleared.length && !check.updated.length && <p>{check.afterExplanation}</p>}
      </li>)}</ul></details>}
      {entry.impacts.evidenceChecks.length > 0 && <details className="hs-disclosure"><summary>Evidence checks on these retained inputs</summary><p className="hs-muted">Walkthrough flags do not establish customer authority, a cited decision, or an independent current version.</p><ul className="hs-check-changes">{entry.impacts.evidenceChecks.map(check => <li key={check.id} data-evidence-result={check.result}>
        <strong>{check.title}</strong><span className="hs-label">{check.resultLabel}</span><p>{check.explanation}</p>
        {actionLink({id: check.id, label: 'Inspect current evidence check', href: readinessHref('', {check: check.id}, paths)}, `evidence-check:${entry.id}:${check.id}`)}
      </li>)}</ul></details>}
    </>}
  </section>;
}

function Reference({reference, entry, entryLink, actionLink}) {
  const unit = reference.unit;
  return <li className="hs-reference" data-reference-kind={reference.kind}>
    <div><span className="hs-kicker">{reference.kind}</span><strong>{reference.label}</strong></div>
    {reference.entryId && entryLink(reference.entryId, 'Inspect retained record', `ref:${entry.id}:${reference.kind}:${reference.id}`)}
    {reference.href && actionLink({id: reference.id, label: reference.kind === 'unit' ? 'Inspect exact unit in Readiness' : 'Inspect current evidence check', href: reference.href}, `ref:${entry.id}:${reference.kind}:${reference.id}`)}
    {reference.note && <p className="hs-muted">{reference.note}</p>}
    {reference.kind === 'unit' && <details className="hs-disclosure"><summary>Source unit retained with this entry</summary>
      {!unit ? <p>The unit snapshot was not recorded with this older entry. The link opens the current card, not a substitute historical quotation.</p>
        : !unit.shown ? <p>Unit not shown. Reason: {unit.notShownReason || 'not recorded'}. No text has been substituted.</p>
        : <>{unit.kind === 'sentence' ? <blockquote className="hs-source-text">{unit.text}</blockquote> : <dl className="hs-facts">{unit.fields.map(field => <div key={field.label}><dt>{field.label}</dt><dd>{field.value ?? 'not recorded'}</dd></div>)}</dl>}
          {unit.partial && <p className="hs-notice">Partial excerpt · {unit.partialReason || 'cut boundary not recorded'}</p>}
          {unit.speaker && <div className="hs-source-speaker"><strong>{unit.speaker.name}</strong><p>{unit.speaker.role}</p><p className="hs-muted">Role basis: {unit.speaker.basis || 'not recorded'}</p><p className="hs-label">A customer role is not decision authority.</p></div>}
          <dl className="hs-facts"><div><dt>Source event time</dt><dd><Time value={unit.eventAt}/></dd></div><div><dt>Source read time</dt><dd><Time value={unit.readAt}/></dd></div></dl>
        </>}
    </details>}
    {reference.record && <details className="hs-disclosure"><summary>Retained basis and receipt identities</summary><dl className="hs-facts">
      <div><dt>Basis record</dt><dd>{reference.record.id || 'not recorded'} · Revision {reference.record.revision ?? 'not recorded'}</dd></div>
      <div><dt>Recorded scope</dt><dd>{historyScopeLabel({agreementId: reference.record.agreementId, documentId: reference.record.documentId, documentVersion: reference.record.documentVersion || reference.record.version})}</dd></div>
      <div><dt>Recorded time</dt><dd><Time value={reference.record.recordedAt}/></dd></div>
      <div><dt>Cited receipt</dt><dd>{reference.receipt?.id || 'not recorded'}{reference.receipt && <span className="hs-detail">Inspection: {reference.receipt.inspection?.status || 'not recorded'} · <Time value={reference.receipt.inspection?.at}/></span>}</dd></div>
    </dl></details>}
    {!reference.entryId && !reference.href && <p className="hs-label">Earlier record link not recorded. The retained typed values are shown in this entry.</p>}
  </li>;
}

function EntryDetails({entry, entryLink, actionLink, onClose, paths}) {
  return <div className="hs-proof" aria-label={`${entry.kindLabel} detail`}>
    <div className="hs-proof-heading"><div><span className="hs-kicker">RETAINED RECORD · {entry.identityRecorded ? entry.id : 'record ID not recorded'}</span><p className="hs-label">{entry.derived ? 'Derived row. The cause remains a separate recording.' : 'Original recording kept in append-only history.'}</p></div><button type="button" className="hs-icon-button" onClick={onClose} aria-label="Close selected history entry"><X size={19}/></button></div>
    {entry.appliedCorrections.length > 0 && <aside className="hs-correction-note"><strong>As effective: corrected projection</strong><p>This is not the original claim. Applied corrections:</p><ul>{entry.appliedCorrections.map(correction => <li key={correction.id}>{entryLink(correction.id, 'Inspect correction', `applied:${entry.id}:${correction.id}`)}<span className="hs-detail">Recorded <Time value={correction.recordedAt}/> · {correction.actor}</span></li>)}</ul></aside>}
    {entry.supersededBy.length > 0 && !entry.appliedCorrections.length && <aside className="hs-correction-note"><strong>Original entry, not rewritten</strong><p>A later correction is known at this point.</p>{entry.supersededBy.map(correction => entryLink(correction.id, 'Inspect the later correction', `superseded:${entry.id}:${correction.id}`))}</aside>}
    {entry.type === 'correction' && <aside className="hs-correction-note"><strong>A new entry, not an edit to the past</strong><p>Supersedes: {entry.supersedes ? entryLink(entry.supersedes, entry.supersedes, `supersedes:${entry.id}`) : 'not recorded'}.</p>{entry.correctionError && <p role="status">{entry.correctionError}</p>}</aside>}
    <dl className="hs-clocks" aria-label="Three distinct clocks">
      <div data-clock="event"><dt>Source event time</dt><dd><Time value={entry.eventAt}/></dd><span className="hs-label">When it happened in the source, if supplied.{entry.appliedCorrections.length > 0 && ' Corrected projection.'}</span></div>
      <div data-clock="recorded"><dt>Recorded time</dt><dd><Time value={entry.recordedAt}/></dd><span className="hs-label">When the walkthrough recorded this entry.</span></div>
      <div data-clock="outcome"><dt>Later outcome recorded</dt><dd>{entry.outcomes.length ? entry.outcomes.map(outcome => <span key={outcome.id}><Time value={outcome.recordedAt}/>{entryLink(outcome.id, outcome.title, `outcome:${entry.id}:${outcome.id}`)}</span>) : 'not recorded'}</dd><span className="hs-label">Later recorded results, not validation of a corrected claim or the commercial renewal.</span></div>
    </dl>
    <dl className="hs-facts hs-accountability"><div><dt>Actor recorded for this event</dt><dd>{entry.actor}</dd></div>{entry.authorityRelevant && <div><dt>Customer decision authority · evidence check</dt><dd>{entry.impacts.authorityLabel || 'unidentified'}</dd></div>}<div><dt>{entry.reasonLabel}{entry.reasonRequired ? ' · required for this change' : ''}</dt><dd>{entry.reason || 'not recorded'}{entry.reasonRequired && !entry.reason && <span className="hs-detail hs-warning">The old workflow did not retain a separate reason here. A computed effect is not the reviewer’s reason.</span>}</dd></div></dl>
    {entry.detail && <aside className="hs-event-note"><span className="hs-kicker">WALKTHROUGH EVENT TEXT, NOT SOURCE TEXT</span><p>{entry.detail}</p></aside>}
    {entry.parentId && <p>{entryLink(entry.parentId, 'Inspect the recorded cause', `cause:${entry.id}`)}</p>}
    {entry.upstreamCorrections?.length > 0 && <aside className="hs-correction-note"><strong>The underlying decision has a correction.</strong><p>This original output is retained, not treated as current evidence of approval.</p>{entry.upstreamCorrections.map(correction => entryLink(correction.id, 'Inspect the correction', `output-correction:${entry.id}:${correction.id}`))}</aside>}
    {entry.output && <p className="hs-notice">{entry.output.completionRule} This row reports the original work output, not a separate completion command or a commercial outcome.</p>}
    {entry.type === 'opening_snapshot' ? <section className="hs-section"><h4>Opening plan, not a new decision</h4><ValueList values={entry.values}/></section> : <Comparison entry={entry} entryLink={entryLink}/>}
    {entry.appliedCorrections.length > 0 && <details className="hs-disclosure hs-original"><summary>Original recording, unchanged</summary><p className="hs-label">Original source event time: <Time value={entry.recordedEventAt}/>. Recording time and actor are unchanged.</p><ValueList values={entry.recordedValues}/><h4>Effects at the original recording</h4><ul className="hs-check-changes">{entry.recordedImpacts.effects.map(effect => <li key={effect.id}>{effect.description}</li>)}</ul><h4>Evidence checks at the original recording</h4><ul className="hs-check-changes">{entry.recordedImpacts.evidenceChecks.map(check => <li key={check.id}><strong>{check.title}</strong><span className="hs-detail">{check.resultLabel} · {check.explanation}</span></li>)}</ul></details>}
    {!entry.derived && entry.type !== 'opening_snapshot' && <Impacts entry={entry} actionLink={actionLink} paths={paths}/>}
    <section className="hs-section"><h4>Trace the evidence and records</h4><p className="hs-muted">Retained values belong to this recording. Work plan and Readiness links open their current records, which may have changed.</p>
      <ul className="hs-references">{entry.references.map(reference => <Reference key={`${reference.kind}:${reference.id}`} reference={reference} entry={entry} entryLink={entryLink} actionLink={actionLink}/>)}</ul>
      <div className="hs-inspection-links">{entry.inspectionLinks.map(link => actionLink(link, `inspect:${entry.id}:${link.id}`, true))}</div>
    </section>
    <button type="button" className="hs-inline-link" onClick={onClose}>Close entry and return to its link<X size={16} aria-hidden="true"/></button>
  </div>;
}

function TimelineEntry({entry, selected, outside, entryLink, actionLink, onClose, paths}) {
  return <li className={`hs-timeline-entry ${selected ? 'is-selected' : ''} ${entry.derived ? 'is-derived' : ''}`} data-history-id={entry.id} data-history-type={entry.type}>
    <div className="hs-event-rail"><span className="hs-node" aria-hidden="true">{entry.type === 'correction' ? <RotateCcw size={16}/> : entry.derived ? <Check size={15}/> : <HistoryIcon size={16}/>}</span><span className="hs-label">{entry.sequence === '0' ? 'Opening' : `Recording ${entry.sequence}`}</span></div>
    <div className="hs-event-content">
      {outside && <p className="hs-notice">Selected entry outside the category filter. The filter and its counts are unchanged.</p>}
      <div className="hs-entry-meta"><span className="hs-kicker">{entry.kindLabel}</span>{entry.supersededBy.length > 0 && <span className="hs-tag">Correction known</span>}{entry.derived && <span className="hs-tag">Derived row</span>}</div>
      <h3>{entryLink(entry.id, entry.title, `entry:${entry.id}`, true)}</h3>
      <div className="hs-recording-line"><span><Clock size={15} aria-hidden="true"/>Recorded: <Time value={entry.recordedAt}/></span><span>Actor: {entry.actor}</span></div>
      {!entry.complete && !entry.derived && entry.type !== 'correction' && <p className="hs-label hs-warning">Partial older record. Missing inputs stay unknown.</p>}
      {selected && <EntryDetails entry={entry} entryLink={entryLink} actionLink={actionLink} onClose={onClose} paths={paths}/>}
    </div>
  </li>;
}

function Intervals({requirements, knownBy, entryLink}) {
  return <section className="hs-interval-section" aria-labelledby="history-intervals">
    <div className="hs-section-heading"><div><span className="hs-kicker">FROM REQUIREMENT TRANSITIONS</span><h2 id="history-intervals" tabIndex={-1}>When the requirement was open</h2></div><Clock size={23} aria-hidden="true"/></div>
    <p className="hs-muted">Intervals use the walkthrough recording clock, not source event dates. An open interval stops at the known-by point, never at the device clock.</p>
    {requirements === null ? <p className="hs-notice">Requirements are not recorded at this point. Unknown is not zero.</p> : !requirements.length ? <p>No requirement records are available in this history.</p> : requirements.map(requirement => <article className="hs-interval-card hs-paper" key={requirement.id} data-interval-requirement={requirement.id}>
      <div className="hs-section-title"><h3>{requirement.title}</h3><span className="hs-tag">{requirement.status === 'resolved' ? 'Resolved in walkthrough' : requirement.status === 'open' ? 'Open in walkthrough' : 'State not recorded'}</span></div>
      <p className="hs-label">{requirement.id} · Through <Time value={knownBy.recordedAt}/></p>
      {requirement.incomplete && <p className="hs-notice">Earlier events lack transition snapshots. These intervals include only recorded transitions; an unrecorded automatic reopening has not been inferred.</p>}
      <ol className="hs-intervals">{requirement.intervals.map((interval, index) => <li key={interval.id} data-interval-id={interval.id} data-interval-status={interval.status}>
        <div className="hs-interval-title"><h4>Open interval {index + 1}</h4><strong>{interval.durationLabel}</strong></div>
        <dl className="hs-facts">
          <div><dt>{interval.initial ? 'Opened' : 'Reopened'}</dt><dd><Time value={interval.openedAt}/>{interval.initial && <span className="hs-detail">First observed open: <Time value={interval.observedAt}/></span>}<span className="hs-detail">Actor: {interval.openingActor}</span></dd></div>
          <div><dt>{interval.status === 'closed' ? 'Resolved' : 'Still open through'}</dt><dd><Time value={interval.status === 'closed' ? interval.resolvedAt : interval.measuredThrough}/>{interval.status === 'closed' && <span className="hs-detail">Actor: {interval.resolvingActor || 'not recorded'}</span>}</dd></div>
          {!interval.initial && <div><dt>Reason for reopening</dt><dd>{interval.reason || 'not recorded'}{interval.cause && <span className="hs-detail">Computed workflow effect: {interval.cause}</span>}</dd></div>}
        </dl>
        <div className="hs-interval-links">{interval.startEntryId && entryLink(interval.startEntryId, interval.initial ? 'Inspect opening snapshot' : 'Inspect reopening', `interval:${interval.id}:start`)}{interval.endEntryId && entryLink(interval.endEntryId, 'Inspect resolution', `interval:${interval.id}:end`)}</div>
      </li>)}</ol>
      {!requirement.intervals.length && <p>No open interval can be reconstructed from the recorded transitions at this point.</p>}
    </article>)}
  </section>;
}

export default function History({state, onExport}) {
  const [params, setParams] = useSearchParams(), navigation = useLocation(), id = useId();
  const view = useMemo(() => selectHistory(state, params), [state, params.toString()]);
  const root = useRef(null), heading = useRef(null), previous = useRef(null), recovery = useRef(null), closing = useRef(false);
  const key = view.location.entry === null ? '' : `entry:${view.location.entry}`;
  const candidate = navigation.state?.historyOpener;
  const opener = candidate?.selection === view.location.entry ? candidate : null;
  useLayoutEffect(() => {
    const prior = previous.current;
    if (!prior && navigation.state?.historyRestoreFocus) restoreHistoryFocus(root.current, navigation.state.historyRestoreFocus, key, heading.current);
    else if (closing.current || !key && prior?.key) restoreHistoryFocus(root.current, prior?.opener, prior?.key, heading.current);
    else if (key && (prior?.key !== key || prior?.fromEntry !== opener?.fromEntry || prior?.revision !== view.revision || prior?.example !== view.location.example)) focusHistoryElement(historyFocusTarget(root.current, key));
    if (recovery.current) {restoreHistoryFocus(root.current, recovery.current, null, heading.current); recovery.current = null;}
    closing.current = false;
    previous.current = {key, opener: opener?.focusKey, fromEntry: opener?.fromEntry, revision: view.revision, example: view.location.example};
  }, [key, navigation.key, opener?.fromEntry, view.revision, view.location.example]);

  function update(changes, metadata = navigation.state) {
    setParams(historySearch(params, changes), {preventScrollReset: true, state: {...metadata, historyRestoreFocus: null}});
  }
  function closeSelection() {
    closing.current = true;
    update({entry: null}, {...navigation.state, historyOpener: null});
  }
  function entryLink(entryId, label, focusKey, primary = false) {
    return <Link key={focusKey} className={primary ? 'hs-entry-link' : 'hs-inline-link'} to={historyHref(params, {entry: entryId}, view.paths)}
      data-history-focus={focusKey} data-history-target={primary ? `entry:${entryId}` : undefined} data-history-entry={primary || undefined}
      aria-current={view.location.entry === entryId ? 'true' : undefined}
      state={{...navigation.state, historyRestoreFocus: null, historyOpener: {selection: entryId, focusKey, fromEntry: navigation.key}}}>{label}</Link>;
  }
  function actionLink(link, focusKey, button = false) {
    return <Link key={focusKey} className={button ? 'hs-button' : 'hs-inline-link'} to={historyActionHref(link.href, params, view.paths)} data-history-focus={focusKey}
      state={{...navigation.state, historyRestoreFocus: null, historyActionOpener: focusKey}}>{link.label}<ArrowUpRight size={16} aria-hidden="true"/></Link>;
  }
  function recover(changes, focusKey) {recovery.current = focusKey; update(changes);}
  const entry = (record, outside = false) => <TimelineEntry key={record.id} entry={record} selected={view.selection?.status === 'ready' && view.selection.id === record.id} outside={outside} entryLink={entryLink} actionLink={actionLink} onClose={closeSelection} paths={view.paths}/>;
  const valid = view.status === 'ready';

  return <div className="hs-page" ref={root} onKeyDown={event => {if (view.location.entry !== null) closeHistoryOnEscape(event, closeSelection);}}>
    <div className="hs-intro"><div><span className="hs-kicker">DECISION HISTORY · SYNTHETIC, BROWSER ONLY</span><h2 ref={heading} tabIndex={-1}>What changed. And why.</h2><p>Follow the decisions, their evidence, and the work they reopened. Keep what was known then distinct from what was corrected later.</p></div>{onExport && <button type="button" className="hs-button" disabled={!valid} onClick={() => {const data = historyExport(view); if (data) onExport(data, view.filename);}}><Download size={18} aria-hidden="true"/>Export this view</button>}</div>
    <details className="hs-guide"><summary id="history-guide" data-history-focus="guide"><Info size={18} aria-hidden="true"/>Reading guide and keyboard help</summary><div>
      <p>{view.policyBoundary}</p>
      <dl className="hs-guide-clocks"><div><dt>Source event time</dt><dd>When the source says it happened. It may precede recording, lack a time zone, or be not recorded.</dd></div><div><dt>Recorded time</dt><dd>When the walkthrough recorded the entry. Timeline order and known-by points use this append-only record, not the source date.</dd></div><div><dt>Outcome time</dt><dd>When a later inspection, decision, or resolution was recorded. It appears only once it is known at the selected point.</dd></div></dl>
      <p>As recorded preserves each original entry. As effective applies only corrections known by the selected point, with the original beside the projection. A correction is a new entry that links to what it supersedes.</p>
      <p>Tab to an entry. Up and Down move between entries; Home and End go to the edges. Enter selects an entry. Escape closes it and returns focus. The view, known-by point, filter, order, and selection stay in the URL.</p>
    </div></details>
    <section className="hs-controls" aria-label="History view controls">
      <fieldset className="hs-view-choice"><legend>View of the record</legend><div>{[{id: 'recorded', label: 'As recorded'}, {id: 'effective', label: 'As effective'}].map(mode => <label key={mode.id}><input type="radio" name="view" value={mode.id} checked={view.location.view === mode.id} onChange={() => update({view: mode.id})}/><span>{mode.label}</span></label>)}</div></fieldset>
      <label htmlFor={`${id}-point`}>Known by (recording point)<select id={`${id}-point`} name="as_of" data-history-focus="point-control" value={view.location.asOf} onChange={event => update({as_of: event.target.value})}><option value="latest">Latest recorded point</option>{view.points.map(point => <option key={point.id} value={point.id}>{point.label} · {formatHistoryTime(point.recordedAt)}</option>)}{view.location.asOf !== 'latest' && !view.points.some(point => point.id === view.location.asOf) && <option value={view.location.asOf}>{valid ? formatHistoryTime(view.knownBy.recordedAt) : 'Unavailable recording point'}</option>}</select></label>
      <label htmlFor={`${id}-category`}>Entries<select id={`${id}-category`} name="category" data-history-focus="category-control" value={view.location.category} onChange={event => update({category: event.target.value})}>{historyCategories.map(category => <option key={category.id} value={category.id}>{category.label}{valid ? ` (${view.counts[category.id]})` : ''}</option>)}</select></label>
      <label htmlFor={`${id}-order`}>Recording order<select id={`${id}-order`} name="order" value={view.location.order} onChange={event => update({order: event.target.value})}><option value="oldest">Oldest first</option><option value="newest">Newest first</option></select></label>
    </section>
    <div className="hs-view-context" role="status"><div><strong>{view.viewLabel}</strong><p>{view.viewDescription}</p>{valid && <p className="hs-label">{view.knownBy.label} · Recorded time: <Time value={view.knownBy.recordedAt}/></p>}</div>{valid && <button type="button" className="hs-inline-link" onClick={() => focusHistoryElement(root.current?.querySelector('#history-intervals'))}>Requirement intervals<Clock size={17} aria-hidden="true"/></button>}</div>
    {view.example.mode === 'example' && <aside className="hs-example-note"><span className="hs-kicker">SEPARATE, INSPECT-ONLY EXAMPLE</span><h3>{view.example.title}</h3><p>{view.example.description}</p><button type="button" className="hs-inline-link" onClick={() => recover({example: null, entry: null, as_of: null}, 'example-summary')}>Return to your walkthrough</button></aside>}
    {view.location.invalid && <div className="hs-notice" role="status"><p>Some URL settings are unavailable. Labelled defaults are showing; no entry has been substituted.</p><button type="button" className="hs-inline-link" onClick={() => recover(Object.fromEntries(view.location.invalidFields.map(field => [field, null])), 'category-control')}>Clear invalid settings</button></div>}
    {!valid ? <section className="hs-error hs-paper" role="alert"><h2 tabIndex={-1} data-history-target={key || undefined}>History {view.status === 'invalid_point' ? 'at that point is not available.' : 'cannot be read.'}</h2><p>{view.status === 'invalid_point' ? 'The URL does not identify one known recording point or a time with an offset. Later history is withheld, not substituted.' : 'The local log has missing or duplicate record identities. No partial history or inferred outcomes are shown. Your saved walkthrough has not been changed.'}</p>{view.status === 'invalid_point' && <button type="button" className="hs-button" onClick={() => recover({as_of: 'latest'}, 'point-control')}>Show latest recorded history</button>}</section> : <>
      <section className="hs-outcomes" aria-label="Three separate outcomes">{view.outcomes.map(outcome => <div key={outcome.id} data-history-outcome={outcome.id}><span className="hs-label">{outcome.label}</span><strong>{outcome.value}</strong><p>{outcome.note}</p>{outcome.id === 'commercial' && <p className="hs-label">Outcome recorded: <Time value={outcome.recordedAt}/></p>}{outcome.entryIds.length > 0 && entryLink(outcome.entryIds.at(-1), 'Inspect latest recording', `summary:${outcome.id}`)}</div>)}</section>
      {view.unrecordedStateChange && <p className="hs-notice">The loaded walkthrough differs from its last retained inputs. Current values have not been inserted into earlier entries. This is a gap in the local recording, not a historical correction.</p>}
      {view.legacyCount > 0 && <p className="hs-notice">{view.legacyCount} older {view.legacyCount === 1 ? 'entry lacks' : 'entries lack'} full input snapshots. Missing actors, times, reasons, and fields remain not recorded. New walkthrough events preserve their before and after inputs.</p>}
      {view.omittedUnknownTimes > 0 && <p className="hs-notice">Entries with unknown recording times are withheld from this time-bounded view. Choose a recording point to inspect them by append order.</p>}
      {view.emptyWalkthrough && <section className="hs-fresh hs-paper"><FileText size={26} aria-hidden="true"/><div><span className="hs-kicker">A NEW WALKTHROUGH</span><h3>The starting point is here. Decisions come next.</h3><p>No workflow decisions have been recorded. Inspect the opening snapshot, then request the plan’s review in Work plan. New events will retain their inputs, comparisons, and requirement intervals.</p><div className="hs-inspection-links">{view.startLinks.map(link => actionLink(link, `start:${link.id}`, true))}</div></div></section>}
      {view.selection?.status === 'not_available' && <section className="hs-selection-error hs-paper"><h2 tabIndex={-1} data-history-target={key}>That entry is not available at this point.</h2><p>The link may be unknown, ambiguous, or beyond the known-by point. No later text or substitute entry has been opened.</p><button type="button" className="hs-button" onClick={closeSelection}>Clear this selection</button></section>}
      <section className="hs-timeline-section" aria-labelledby={`${id}-timeline`}>
        <div className="hs-section-heading"><div><span className="hs-kicker">APPEND-ONLY RECORD</span><h2 id={`${id}-timeline`}>The decision trail</h2></div><HistoryIcon size={23} aria-hidden="true"/></div>
        <p className="hs-result-count" role="status">{view.visibleEntries.length} of {view.counts.all} entries at this point. {view.recordedCount} recorded {view.recordedCount === 1 ? 'event' : 'events'}; {view.derivedCount} derived {view.derivedCount === 1 ? 'row' : 'rows'}{view.entries.some(entry => entry.type === 'opening_snapshot') ? '; one opening snapshot' : ''}. Counts are entries, not requirements.</p>
        {!view.visibleEntries.length && <section className="hs-empty hs-paper"><h3>No entries in this view.</h3><p>{view.recordedCount ? 'Other categories may contain the decision or its recorded cause.' : 'No decisions were known at this point. The absence of a recording is not approval.'}</p><button type="button" className="hs-button" onClick={() => recover({category: 'all'}, 'category-control')}>Show all categories</button></section>}
        <ol className="hs-timeline" onKeyDown={event => moveHistoryFocus(event, event.currentTarget)}>{view.visibleEntries.map(record => entry(record))}{view.selectedOutside && entry(view.selectedOutside, true)}</ol>
      </section>
      <Intervals requirements={view.intervals} knownBy={view.knownBy} entryLink={entryLink}/>
    </>}
    <details className="hs-examples"><summary data-history-focus="example-summary">Local history example</summary><p>Inspect a separate correction history to compare clocks and attribution. Switching examples never saves a decision, applies a correction to your walkthrough, or changes real access.</p><label htmlFor={`${id}-example`}>History to inspect<select id={`${id}-example`} name="example" value={view.location.example} onChange={event => update({example: event.target.value === 'walkthrough' ? null : event.target.value, entry: null, as_of: null})}>{view.examples.map(example => <option value={example.id} key={example.id}>{example.label}</option>)}</select></label></details>
    <p className="hs-footnote"><Info size={17} aria-hidden="true"/>{view.scope}</p>
  </div>;
}
