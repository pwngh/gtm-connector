import React, {useEffect, useId, useLayoutEffect, useRef, useState} from 'react';
import {Link, useLocation, useSearchParams} from 'react-router';
import {ArrowDown, ArrowUp, ArrowUpRight, ChevronRight, Info, ListFilter, Search, X} from 'lucide-react';
import {clearWorklistSearch, selectWorklist, updateWorklistSearch, worklistActionHref, worklistCount, worklistHref, worklistNoun} from './worklist.js';
import {activateWorklistRow, focusWorklistElement, moveWorklistFocus, restoreWorklistFocus, worklistFocusKey} from './worklistBrowser.js';
import './worklists.css';
export {default as WorklistStepEntry} from './WorklistStepEntry.jsx';

function ScopeLabel({row}) {
  return <span className={`wl-scope-label ${row.mode}`}>{row.modeLabel}</span>;
}

function ResponseState({response, onRecover}) {
  return <section className="wl-state" role={response.status === 'error' ? 'alert' : 'status'} aria-busy={response.status === 'loading'}>
    <Info size={26} aria-hidden="true"/>
    <span className="wl-kicker">SIMULATED RESPONSE{response.code ? ` · ${response.code}` : ''}</span>
    <h2>{response.title}</h2><p>{response.message}</p>
    <button type="button" className="wl-button" onClick={onRecover}>{response.status === 'error' ? 'Retry with local records' : 'Show available records'}</button>
  </section>;
}

function ChangeFact({item}) {
  return item.mode === 'illustrative' ? <div><dt>Snapshot captured</dt><dd>{item.snapshotLabel}<span className="wl-detail-line">No change history recorded.</span></dd></div>
    : <div><dt>Last change</dt><dd>{item.lastChangedLabel}<span className="wl-detail-line">{item.lastChangedTime}</span></dd></div>;
}


function SelectedItem({selection, headingRef, onClose, entryLink, returnTo, returnState}) {
  if (selection.status !== 'ready') return <section className="wl-inspector wl-state">
    <h2 tabIndex={-1} ref={headingRef}>That item is not available.</h2>
    <p>The selection in this link does not match a local record. No substitute item has been opened.</p>
    <button type="button" className="wl-button" onClick={onClose}>Return to the list</button>
  </section>;
  const {kind, resource: item, renewal} = selection;
  const relatedWork = renewal.workItems.filter(work => work.requirementId === item.id);
  const blockedReasons = item.blockedReasons.filter(reason => reason.code !== 'illustrative_only');
  return <section className="wl-inspector" aria-labelledby="worklist-selection-title">
    <div className="wl-inspector-head"><div><span className="wl-kicker">{kind === 'renewal' ? 'RENEWAL READINESS' : kind === 'requirement' ? 'EXACT REQUIREMENT' : 'EXACT WORK ITEM'}</span>
      <h2 id="worklist-selection-title" ref={headingRef} tabIndex={-1}>{item.title}</h2>
      <p>{kind !== 'renewal' && <>{renewal.accountName} · </>}<span className="wl-record-id">{item.id}</span> · {renewal.mode === 'illustrative' ? 'Snapshot revision' : 'Revision'} {item.revision}</p>
    </div><button type="button" className="wl-icon-button" aria-label="Close item details" onClick={onClose}><X size={20}/></button></div>
    <ScopeLabel row={renewal}/>
    {!selection.visible && <p className="wl-notice" role="status">This selection is outside the displayed results. Your filters and sort are unchanged.</p>}
    {renewal.mode === 'illustrative' ? <p className="wl-boundary">Illustrative snapshot only, captured {renewal.snapshotLabel}. You can inspect these records, but there is no editable workflow or change history for this account. Nothing can be assigned, sent, approved, or completed here.</p>
      : <p className="wl-boundary">Local walkthrough only. Actions open the existing simulated workflow; they do not grant real authority or contact anyone.</p>}
    {kind === 'renewal' && <>
      <dl className="wl-facts">
        <div><dt>CRM stage</dt><dd>{item.crm.stageLabel}<span className="wl-detail-line">Read {item.crm.readLabel}</span><span className="wl-detail-line">{item.crm.source}</span></dd></div>
        <div><dt>Readiness</dt><dd>{item.readinessLabel}<span className="wl-detail-line">{item.openBlockerCount === null ? 'Blocker count unknown' : worklistCount(item.openBlockerCount, 'open blocking requirement')}</span></dd></div>
        <div><dt>Commercial outcome</dt><dd>{item.commercial.label}<span className="wl-detail-line">Not inferred from readiness or CRM stage.</span></dd></div>
        <div><dt>Renewal date</dt><dd>{item.renewalDateLabel}<span className="wl-detail-line">{item.daysLabel}</span></dd></div>
        <div><dt>Renewal owner</dt><dd>{item.ownerLabel}</dd></div><ChangeFact item={item}/>
      </dl>
      <div className="wl-detail-columns"><section><h3>Requirements</h3>
        {item.requirements === null ? <p>Requirements are not recorded. Unknown is not zero.</p> : item.requirements.length ? <ul className="wl-related">{item.requirements.map(requirement => <li key={requirement.id}>
          {entryLink(requirement.selectionKey, requirement.title)}<span className="wl-detail-line">{requirement.status === 'resolved' ? 'Resolved' : 'Open'} · {requirement.kindLabel}</span>
        </li>)}</ul> : <p>No requirements are open in this snapshot. This does not confirm a signed renewal.</p>}
      </section><section><h3>Work items</h3><ul className="wl-related">{item.workItems.map(work => <li key={work.id}>{entryLink(work.selectionKey, work.title)}<span className="wl-detail-line">{work.statusLabel} · {work.ownerLabel}</span></li>)}</ul></section></div>
    </>}
    {kind === 'requirement' && <>
      <p className="wl-lead">{item.description}</p>
      <dl className="wl-facts"><div><dt>Requirement state</dt><dd>{item.status === 'resolved' ? 'Resolved' : 'Open'} · {item.kindLabel}</dd></div><div><dt>Requirement owner</dt><dd>{item.ownerLabel}</dd></div><div><dt>Commercial outcome</dt><dd>{renewal.commercial.label}<span className="wl-detail-line">A separate outcome.</span></dd></div></dl>
      {item.evidence ? <Link className="wl-inline-link" to={worklistActionHref(item.evidence, returnTo)} state={returnState}>Inspect supporting evidence: {item.evidence.label}<ArrowUpRight size={16}/></Link> : <p className="wl-notice">Supporting evidence is not recorded in this snapshot.</p>}
      <section className="wl-related-section"><h3>Related work, not a substitute for resolution</h3>{relatedWork.length ? <ul className="wl-related">{relatedWork.map(work => <li key={work.id}>{entryLink(work.selectionKey, work.title)}<span className="wl-detail-line">{work.statusLabel}</span></li>)}</ul> : <p>No related work is recorded for this requirement.</p>}</section>
    </>}
    {kind === 'work-item' && <>
      <dl className="wl-facts"><div><dt>Action owner</dt><dd>{item.ownerLabel}</dd></div><div><dt>Waiting on</dt><dd>{item.waitingLabel}{item.waitingReason && <span className="wl-detail-line">{item.waitingReason}</span>}{item.waitingOn && <span className="wl-detail-line">{item.waitingOn.basis}</span>}</dd></div><div><dt>Due date</dt><dd>{item.dueLabel}{item.overdue && <span className="wl-detail-line wl-urgent">Overdue on the walkthrough clock</span>}</dd></div><div><dt>Work state</dt><dd>{item.statusLabel}</dd></div><div><dt>Next permitted action</dt><dd>{item.nextActionLabel}</dd></div><ChangeFact item={item}/></dl>
      <section className="wl-completion"><h3>What completes this item</h3><p>{item.completionRule}</p>{item.output && <p><strong>Recorded output: </strong>{item.output}</p>}</section>
      {item.readinessAction && <Link className="wl-inline-link" to={worklistActionHref(item.readinessAction, returnTo)} state={returnState}>{item.readinessAction.label}<ArrowUpRight size={16}/></Link>}
      {item.requirementId && <div className="wl-context-link">{entryLink(`requirement:${item.requirementId}`, 'Inspect the linked requirement')}<p>Work-item completion does not resolve the requirement.</p></div>}
    </>}
    {blockedReasons.length > 0 && <section className="wl-blocked"><h3>Blocked, because</h3><ul>{blockedReasons.map(reason => <li key={`${reason.action}-${reason.code}`}>{reason.reason}</li>)}</ul></section>}
    <div className="wl-detail-actions"><button type="button" className="wl-button" onClick={onClose}>Back to list</button>{item.allowedActions.map(action => <Link key={action.id} className="wl-button wl-primary" to={worklistActionHref(action, returnTo)} state={returnState}>{action.label}<ArrowUpRight size={17}/></Link>)}</div>
  </section>;
}

export default function Worklists({data, view}) {
  const [params, setParams] = useSearchParams();
  const navigation = useLocation();
  const result = selectWorklist(data, view, params);
  const location = result.location;
  const [query, setQuery] = useState(location.q);
  const id = useId();
  const pageRef = useRef(null), searchRef = useRef(null), headingRef = useRef(null), listRef = useRef(null);
  const previousSelection = useRef(null), previousStatus = useRef(result.status), restoreSearch = useRef(false), revealIndex = useRef(null);
  const selectionKey = result.selection ? location.selection : '';
  const opener = navigation.state?.worklistOpener?.selection === selectionKey ? navigation.state.worklistOpener : null;
  useEffect(() => {setQuery(location.q);}, [location.q]);
  useLayoutEffect(() => {
    const previous = previousSelection.current;
    if (selectionKey !== previous?.key || opener?.fromEntry !== previous?.fromEntry) {
      if (selectionKey) focusWorklistElement(headingRef.current);
      else if (previous?.key) restoreWorklistFocus(pageRef.current, previous.opener, previous.fallback, searchRef.current);
    }
    previousSelection.current = {
      key: selectionKey, entryKey: navigation.key, fromEntry: opener?.fromEntry, opener: opener?.focusKey,
      fallback: result.selection?.rowKey ? worklistFocusKey(result.selection.rowKey, 'entry') : null,
    };
    if (revealIndex.current !== null) {
      focusWorklistElement(listRef.current?.querySelectorAll('[data-row-entry]')[revealIndex.current]);
      revealIndex.current = null;
    }
    if (restoreSearch.current) {focusWorklistElement(searchRef.current); restoreSearch.current = false;}
    if (previousStatus.current !== result.status) {
      const target = result.status === 'ready' ? searchRef.current : pageRef.current?.querySelector('.wl-state h2');
      if (target) {if (result.status !== 'ready') target.tabIndex = -1; focusWorklistElement(target);}
      previousStatus.current = result.status;
    }
  }, [selectionKey, navigation.key, params.toString(), result.status, view]);
  function update(changes, options = {}) {setParams(updateWorklistSearch(params, changes), {preventScrollReset: true, state: navigation.state, ...options});}
  function clearFilters() {restoreSearch.current = true; setQuery(''); setParams(clearWorklistSearch(params, view), {preventScrollReset: true, state: navigation.state});}
  function closeSelection() {update({selection: null}, {state: {...navigation.state, worklistOpener: null}});}
  function selectSort(sort) {update({sort, direction: result.sorts.find(item => item.id === sort).initialDirection});}
  function sortColumn(sort) {update({sort, direction: sort === location.sort ? location.direction === 'asc' ? 'desc' : 'asc' : result.sorts.find(item => item.id === sort).initialDirection});}
  function entryLink(key, label, options = {}) {
    const focusKey = worklistFocusKey(options.rowKey || `details-${selectionKey}`, options.purpose || `related-${key}`);
    const focusReturn = options.rowKey ? focusKey : opener?.focusKey;
    return <Link to={worklistHref(view, params, {selection: key})} className={options.className || 'wl-inline-link'}
      state={{...navigation.state, worklistOpener: {selection: key, focusKey: focusReturn, fromEntry: navigation.key}}}
      data-row-entry={options.primary ? true : undefined} data-worklist-focus={focusKey}
      aria-label={options.label} aria-current={location.selection === key ? 'true' : undefined}>
      {label}{options.arrow && <ChevronRight size={16} aria-hidden="true"/>}
    </Link>;
  }
  const sortHeading = (sort, title) => <th scope="col" role="columnheader" aria-sort={location.sort === sort ? location.direction === 'asc' ? 'ascending' : 'descending' : 'none'}><span className="wl-static-heading">{title}</span><button type="button" onClick={() => sortColumn(sort)}>{title}{location.sort === sort ? location.direction === 'asc' ? <ArrowUp size={15}/> : <ArrowDown size={15}/> : <span aria-hidden="true">↕</span>}</button></th>;
  const selectedRenewal = result.selection?.status === 'ready' ? result.selection.renewal.id : null;
  return <div className="wl-page" ref={pageRef} onKeyDown={event => {
    if (event.key === 'Escape' && result.selection && !event.defaultPrevented) {event.preventDefault(); event.stopPropagation(); closeSelection();}
  }}>
    <div className="wl-scope"><div><span className="wl-kicker">SYNTHETIC PORTFOLIO · BROWSER ONLY</span><p>One interactive walkthrough. Other accounts are inspect-only snapshots.</p></div>
      <details className="wl-help"><summary><Info size={17} aria-hidden="true"/>How to use this list</summary><div>
        <p>CRM stage, requirement readiness, and the commercial outcome are separate. Unknown values stay unknown.</p>
        <p>Most urgent means open blocking requirements first, then requirement kind and ID as stable tie-breakers.</p>
        <p>Facet counts overlap. Selecting several matches any selected facet, within the other filters. Counts are never added together. Due facets apply only to walkthrough records and include overdue work.</p>
        <p>Only the walkthrough clock advances with local events. Illustrative dates stay at their captured snapshot, without overdue flags or change ages. Neither is a live feed. Work items are separate from the Operations report.</p>
        <p>Tab to an entry. Up and Down move between entries; Enter opens that exact item. Escape closes details and restores focus. Filters, sorting, and selection are kept in the URL.</p>
      </div></details>
    </div>
    {result.status !== 'ready' ? <ResponseState response={result.response} onRecover={() => update({response: null})}/> : <>
      <section className="wl-controls" aria-label="Worklist controls">
        <fieldset className="wl-scopes"><legend>Record scope</legend><div>{result.scopes.map(scope => <label key={scope.id}>
          <input type="radio" name="scope" value={scope.id} checked={location.scope === scope.id} onChange={() => update({scope: scope.id})}/>
          <span>{scope.label}<span className="wl-label">{worklistCount(scope.count, result.singularUnit)}</span></span>
        </label>)}</div><p>Scope counts use search, owner, stage{view === 'queue' ? ', and work status' : ''}, before scope and attention filters.</p></fieldset>
        <form className="wl-filter-form" onSubmit={event => {event.preventDefault(); update({q: query.trim()});}} role="search">
          <div className="wl-search-field"><label htmlFor={`${id}-search`}>{view === 'renewals' ? 'Find a renewal' : 'Find a work item or account'}</label><div className="wl-search-input"><Search size={18} aria-hidden="true"/><input id={`${id}-search`} name="q" ref={searchRef} type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder="Search the local records"/><button type="submit" className="wl-button">Search</button></div></div>
          <label className="wl-field" htmlFor={`${id}-owner`}>{view === 'renewals' ? 'Renewal owner' : 'Action owner'}<select id={`${id}-owner`} name="owner" value={location.owner} onChange={event => update({owner: event.target.value})}><option value="">All owners</option><option value="unassigned">Unassigned</option>{result.ownerOptions.map(owner => <option key={owner.id} value={owner.id}>{owner.displayName}</option>)}{location.owner && location.owner !== 'unassigned' && !result.ownerOptions.some(owner => owner.id === location.owner) && <option value={location.owner}>Unavailable owner</option>}</select></label>
          <label className="wl-field" htmlFor={`${id}-stage`}>CRM stage<select id={`${id}-stage`} name="stage" value={location.stage} onChange={event => update({stage: event.target.value})}><option value="">All stages</option>{result.stageOptions.map(stage => <option key={stage.id} value={stage.id}>{stage.label}</option>)}{location.stage && !result.stageOptions.some(stage => stage.id === location.stage) && <option value={location.stage}>Unavailable stage</option>}</select></label>
        </form>
        <fieldset className="wl-facets"><legend><ListFilter size={16} aria-hidden="true"/>Needs attention <span>· match any selected</span></legend><div>{result.facets.map(facet => <button key={facet.id} type="button" aria-pressed={location.attention.includes(facet.id)} onClick={() => update({attention: location.attention.includes(facet.id) ? location.attention.filter(value => value !== facet.id) : [...location.attention, facet.id]})}>
          {facet.label}<span>{worklistCount(facet.count, result.singularUnit)}</span>
        </button>)}</div><p>Counts: {result.unit} within record scope, search, owner{view === 'queue' ? ', work status,' : ''} and stage filters, before attention filters. Facets overlap.</p></fieldset>
        <div className="wl-sort-tools">
          <label className="wl-field" htmlFor={`${id}-sort`}>Sort by<select id={`${id}-sort`} name="sort" value={location.sort} onChange={event => selectSort(event.target.value)}>{result.sorts.map(sort => <option key={sort.id} value={sort.id}>{sort.label}</option>)}</select></label>
          <button type="button" className="wl-button wl-direction" aria-label={`${location.direction === 'asc' ? 'Ascending. Sort descending' : 'Descending. Sort ascending'}`} onClick={() => update({direction: location.direction === 'asc' ? 'desc' : 'asc'})}>{location.direction === 'asc' ? <ArrowUp size={16} aria-hidden="true"/> : <ArrowDown size={16} aria-hidden="true"/>} {location.direction === 'asc' ? 'Ascending' : 'Descending'}</button>
          {view === 'queue' && <label className="wl-field" htmlFor={`${id}-status`}>Work status<select id={`${id}-status`} name="status" value={location.status} onChange={event => update({status: event.target.value})}><option value="open">Open work items</option><option value="all">All work items</option><option value="complete">Output recorded</option></select></label>}
          <button type="button" className="wl-clear" onClick={clearFilters}>Clear filters</button>
        </div>
      </section>
      {result.invalid && <p className="wl-notice" role="status">This link contains an unavailable filter or list setting. Unsupported values use the defaults; unavailable owners or stages match no records. Clear filters to remove invalid settings.</p>}
      <div className="wl-results-heading"><h2>{view === 'renewals' ? 'Renewal worklist' : 'Work items across renewals'}</h2><p role="status">{result.rows.length} of {result.total} matching {worklistNoun(result.total, result.singularUnit)}<span> · {result.available} in the local records</span></p></div>
      <p className="wl-as-of">Walkthrough clock: {result.asOfLabel}. Illustrative snapshots stay fixed at {result.snapshotAsOfLabel}. Unknown sort values appear last.{view === 'queue' && ' Ties use ready, waiting, blocked, then planned work, followed by step and ID. Blocker sorting uses the linked renewal’s open requirements.'}</p>
      {result.rows.length ? <div className="wl-table-wrap" ref={listRef} onKeyDown={event => moveWorklistFocus(event, listRef.current)}>
        {view === 'renewals' ? <table className="wl-table wl-renewals-table" role="table"><caption className="sr-only">Renewals. Open an account for readiness, or an exact requirement or work item from its link.</caption><thead role="rowgroup"><tr role="row"><th scope="col" role="columnheader">Account</th>{sortHeading('days', 'Renewal')}<th scope="col" role="columnheader">CRM stage</th>{sortHeading('blockers', 'Open requirements')}<th scope="col" role="columnheader">Renewal owner</th><th scope="col" role="columnheader">Next step</th>{sortHeading('changed', 'Last change')}</tr></thead>
          <tbody role="rowgroup">{result.rows.map(row => <tr role="row" key={row.id} className={`wl-row ${row.id === selectedRenewal ? 'is-selected' : ''}`} data-record-id={row.id} onClick={activateWorklistRow}>
            <td role="cell" className="wl-account" data-label="Account">{entryLink(row.selectionKey, row.accountName, {rowKey: row.selectionKey, purpose: 'entry', primary: true, className: 'wl-entry', label: `Open readiness: ${row.accountName}`})}<ScopeLabel row={row}/></td>
            <td role="cell" data-label="Renewal"><time dateTime={row.renewalDate || undefined}>{row.renewalDateLabel}</time><span className={`wl-detail-line ${row.urgentDate ? 'wl-urgent' : ''}`}>{row.daysLabel}</span></td>
            <td role="cell" data-label="CRM stage"><strong className="wl-value">{row.crm.stageLabel}</strong><span className="wl-detail-line"><span className="wl-label">Read </span><time dateTime={row.crm.readAt || undefined}>{row.crm.readLabel}</time></span></td>
            <td role="cell" data-label="Open requirements"><span>{row.readinessLabel}</span><span className="wl-label">{row.blockerLabel}</span>{row.urgentRequirement ? entryLink(row.urgentRequirement.selectionKey, <span><span className="wl-label">Most urgent: </span>{row.urgentRequirement.kindLabel}</span>, {rowKey: row.selectionKey, purpose: 'urgent-requirement', label: `Most urgent: ${row.urgentRequirement.kindLabel}. Open requirement: ${row.urgentRequirement.title}`}) : <span className="wl-detail-line">{row.openRequirementCount === null ? 'Most urgent kind unknown' : 'No open blocker'}</span>}</td>
            <td role="cell" data-label="Renewal owner">{row.ownerLabel}</td>
            <td role="cell" data-label="Next step">{entryLink(row.nextStep.selectionKey, row.nextStep.label, {rowKey: row.selectionKey, purpose: 'next-step', arrow: true, label: `${row.nextStep.label}: ${row.nextStep.title}`})}<span className="wl-detail-line"><span className="wl-label">Action owner: </span>{row.nextStep.ownerLabel}</span>{row.nextStep.waitingOn && <span className="wl-detail-line"><span className="wl-label">Waiting on: </span>{row.nextStep.waitingLabel}</span>}</td>
            <td role="cell" data-label="Last change">{row.mode === 'illustrative' ? <span title={row.lastChangedTime}>{row.lastChangedLabel}</span> : <time dateTime={row.lastChangedAt || undefined} title={row.lastChangedTime}>{row.lastChangedLabel}</time>}</td>
          </tr>)}</tbody></table>
          : <table className="wl-table wl-queue-table" role="table"><caption className="sr-only">Work items. Action owners and awaited parties are separate. Open an item for its exact scope and permitted next step.</caption><thead role="rowgroup"><tr role="row"><th scope="col" role="columnheader">Work item</th><th scope="col" role="columnheader">Action owner</th><th scope="col" role="columnheader">Waiting on</th>{sortHeading('due', 'Due date')}<th scope="col" role="columnheader">Blocked, because</th><th scope="col" role="columnheader">Next permitted action</th></tr></thead><tbody role="rowgroup">{result.rows.map(row => <tr role="row" key={row.id} className={`wl-row ${row.selectionKey === location.selection ? 'is-selected' : ''}`} data-record-id={row.id} onClick={activateWorklistRow}>
            <td role="cell" className="wl-account" data-label="Work item">{entryLink(row.selectionKey, row.title, {rowKey: row.selectionKey, purpose: 'entry', primary: true, className: 'wl-entry', label: `Open work item: ${row.title}`})}{entryLink(row.renewalSelectionKey, row.accountName, {rowKey: row.selectionKey, purpose: 'readiness', className: 'wl-account-link', label: `Open readiness: ${row.accountName}`})}<span className={`wl-work-status ${row.status}`}>{row.statusLabel}</span><ScopeLabel row={row}/></td>
            <td role="cell" data-label="Action owner">{row.ownerLabel}</td><td role="cell" data-label="Waiting on">{row.waitingLabel}{row.waitingReason && <span className="wl-detail-line">{row.waitingReason}</span>}</td>
            <td role="cell" data-label="Due date"><time dateTime={row.dueAt || undefined}>{row.dueLabel}</time>{row.overdue && <span className="wl-detail-line wl-urgent">Overdue</span>}</td>
            <td role="cell" data-label="Blocked, because">{row.blockedLabel}</td><td role="cell" data-label="Next permitted action">{entryLink(row.selectionKey, row.nextActionLabel, {rowKey: row.selectionKey, purpose: 'next-step', arrow: true})}</td>
          </tr>)}</tbody></table>}
      </div> : <section className="wl-state wl-empty"><Search size={26} aria-hidden="true"/><h2>No {result.unit} match this view.</h2><p>{location.status === 'complete' ? 'No recorded outputs match these filters. Completing work and resolving a requirement are separate events.' : 'Try another record scope, owner, stage, or attention facet. Nothing has been removed or changed.'}</p><button type="button" className="wl-button" onClick={clearFilters}>Clear filters and show records</button></section>}
      {result.truncated && <div className="wl-truncated" role="status"><div><strong>Truncated list · {result.rows.length} of {worklistCount(result.total, result.singularUnit)} shown</strong><p>Facet counts cover the full filter scope before attention filters, not just these rows. {result.remaining} more {worklistNoun(result.remaining, result.singularUnit)} {result.remaining === 1 ? 'is' : 'are'} available.</p></div><button type="button" className="wl-button" onClick={() => {revealIndex.current = result.rows.length; update({limit: 'all'});}}>Show remaining records</button></div>}
      {result.selection && <SelectedItem selection={result.selection} headingRef={headingRef} onClose={closeSelection} entryLink={entryLink} returnTo={worklistHref(view, params)} returnState={navigation.state}/>}
    </>}
    <details className="wl-response-examples"><summary>Local response examples</summary><p>Preview unavailable, loading, or truncated responses. This changes presentation only, never access or workflow records.</p>
      <label className="wl-field" htmlFor={`${id}-response`}>Simulated list response<select id={`${id}-response`} name="response" value={location.response} onChange={event => update({response: event.target.value === 'normal' ? null : event.target.value, limit: null})}>{result.examples.map(example => <option key={example.id} value={example.id}>{example.label}</option>)}</select></label>
    </details>
  </div>;
}
