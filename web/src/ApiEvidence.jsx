import React, {useEffect, useRef} from 'react';
import {Link} from 'react-router';
import {CircleDashed, Database, Info, MessageSquare, Phone, X} from 'lucide-react';
import {gapDispositions, unitFacts} from './api.js';
import {ApiFrame, ApiRenewalHeader, AccountPicker, CardFacts, CardList, Refusal, Time, gapsTarget, selectionHref, useApiRead, useCardSelection} from './ApiFrame.jsx';
import {useServer} from './ServerSwitch.jsx';
import {Pill} from './WorkspaceControls.jsx';

// Evidence when the Go server serves the app: the signed-in principal's
// cards for an account, a card's units with their receipts, and its gaps,
// read from /api/v1. Text is the server's neutralized text, shown exactly.

const sourceIcons = {calls: Phone, slack: MessageSquare, crm: Database};
const sourceLabels = {calls: 'Call', slack: 'Slack', crm: 'CRM'};

export function ReceiptSummary({receipt}) {
  return <dl className="api-receipt" data-receipt-id={receipt.id}>
    <div><dt>Read by</dt><dd>{receipt.tool}</dd></div>
    <div><dt>Status</dt><dd>{receipt.status}{receipt.http_status != null && ` · HTTP ${receipt.http_status}`}</dd></div>
    <div><dt>Started</dt><dd><Time value={receipt.started_at}/></dd></div>
    <div><dt>Finished</dt><dd>{receipt.finished_at ? <Time value={receipt.finished_at}/> : 'Not finished'}</dd></div>
    <div><dt>Rows</dt><dd>{receipt.row_count}</dd></div>
  </dl>;
}

function ReceiptInFull({receipt}) {
  return <dl className="api-receipt api-receipt-full" data-receipt-id={receipt.id}>
    <div><dt>Receipt</dt><dd>{receipt.id}</dd></div>
    <div><dt>Operation</dt><dd>{receipt.operation_id}</dd></div>
    <div><dt>Parent receipt</dt><dd>{receipt.parent_id || 'None'}{receipt.ordinal != null && ` · read ${receipt.ordinal}`}</dd></div>
    <div><dt>Read by</dt><dd>{receipt.tool}{receipt.scope && ` · scope ${receipt.scope}`}</dd></div>
    <div><dt>Account</dt><dd>{receipt.account}</dd></div>
    <div><dt>Status</dt><dd>{receipt.status}{receipt.http_status != null && ` · HTTP ${receipt.http_status}`}{receipt.error && ` · ${receipt.error}`}</dd></div>
    <div><dt>Started</dt><dd><Time value={receipt.started_at}/></dd></div>
    <div><dt>Finished</dt><dd>{receipt.finished_at ? <Time value={receipt.finished_at}/> : 'Not finished'}</dd></div>
    <div><dt>Rows ({receipt.row_count})</dt><dd>{receipt.rows?.length ? <ul className="api-rows">{receipt.rows.map(row => <li key={row}>{row}</li>)}</ul> : 'None'}</dd></div>
  </dl>;
}

// One unit as the card holds it: a sentence is a quotation, shown as data
// with its speaker, role and the server's basis; a record is its reference.
export function UnitBody({unit}) {
  const facts = unitFacts(unit);
  return <>
    {facts.kind === 'sentence'
      ? <blockquote className="library-quote api-quote">{facts.text ?? 'Text not recorded'}</blockquote>
      : <p className="api-record">Record <span className="api-mono">{facts.recordRef}</span>. A record unit carries its reference, not field values.</p>}
    {facts.partial && <p className="evidence-cut">Partial excerpt · cut, not a whole quotation</p>}
    {facts.notShown && <p className="evidence-cut">Not shown on the card · {facts.notShown}</p>}
    {facts.kind === 'sentence' && <dl className="speaker-role">
      <div><dt>Speaker</dt><dd>{facts.speaker || 'Not recorded'}</dd></div>
      <div><dt>Role</dt><dd>{facts.role || 'Not recorded'}</dd></div>
      <div><dt>Role basis</dt><dd>{facts.basis || 'Not recorded'}</dd></div>
      <div><dt>Customer decision authority</dt><dd>Unidentified. A role is not authority.</dd></div>
    </dl>}
  </>;
}

function UnitDetail({cardId, n, who, params}) {
  const heading = useRef(null);
  const unit = useApiRead(`unit:${who}:${cardId}:${n}`, api => api.unit(cardId, n));
  useEffect(() => {heading.current?.focus({preventScroll: true});}, [cardId, n]);
  return <section className="api-unit-detail" aria-labelledby={`api-unit-${n}-detail`}>
    <div className="api-unit-detail-head"><h4 id={`api-unit-${n}-detail`} ref={heading} tabIndex={-1}>Unit {n} in full</h4>
      <Link className="button ghost" to={selectionHref(params, {unit: null})} aria-label={`Close unit ${n} in full`} preventScrollReset><X size={16} aria-hidden="true"/>Close</Link></div>
    {unit.status === 'failed' ? <Refusal error={unit.error} retry={unit.retry}/> : unit.status !== 'ready' ? <p className="api-loading" role="status">Reading unit {n}…</p> : <>
      <dl className="api-facts">
        <div><dt>Card and number</dt><dd>{unit.data.card} · unit {unit.data.n}</dd></div>
        <div><dt>Kind</dt><dd>{unit.data.kind === 'record' ? 'Record unit' : 'Sentence unit'}</dd></div>
        <div><dt>Record</dt><dd className="api-mono">{unit.data.record_ref}</dd></div>
        <div><dt>Record version</dt><dd>{unit.data.record_version_id || 'Not bound'}</dd></div>
      </dl>
      <h5>Read receipt</h5>
      {unit.data.receipt ? <ReceiptInFull receipt={unit.data.receipt}/> : <p>No receipt is bound to this unit by ID.</p>}
    </>}
  </section>;
}

export function UnitRow({unit, receipt, open, params, who, cardId}) {
  const facts = unitFacts(unit);
  const Icon = facts.kind === 'record' ? Database : sourceIcons[facts.source] || MessageSquare;
  return <article className={`library-row api-unit ${open ? 'is-open' : ''}`} data-unit-n={facts.n} id={`api-unit-${facts.n}`}>
    <span className="source-icon"><Icon size={22}/></span>
    <div>
      <div className="library-meta"><Pill>Unit {facts.n}</Pill><span>{facts.kind === 'record' ? 'Record unit' : 'Sentence unit'} · {sourceLabels[facts.source] || facts.source || 'Source not recorded'}{facts.at && <> · <Time value={facts.at}/></>}</span></div>
      <UnitBody unit={unit}/>
      <div className="api-unit-receipt"><span className="api-label">Read receipt</span>
        {!facts.receiptId ? <p>No receipt is bound to this unit by ID.</p>
          : !receipt || receipt.status === 'loading' ? <p className="api-loading" role="status">Reading receipt…</p>
            : receipt.status === 'failed' ? <Refusal error={receipt.error}/> : <ReceiptSummary receipt={receipt.data}/>}
      </div>
      {open && <UnitDetail cardId={cardId} n={facts.n} who={who} params={params}/>}
    </div>
    {!open && <Link className="button ghost" id={`api-unit-${facts.n}-inspect`} to={selectionHref(params, {unit: facts.n})} preventScrollReset>Inspect unit {facts.n}</Link>}
  </article>;
}

// Each distinct receipt once, read separately so one refusal stays with its units.
function useReceipts(card, who) {
  const ids = card ? [...new Set(card.units?.map(unit => unit.receipt_id).filter(Boolean))] : [];
  const read = useApiRead(card ? `receipts:${who}:${card.id}:${ids.join(',')}` : null, async api => {
    const results = await Promise.all(ids.map(id => api.receipt(id).then(data => [id, {status: 'ready', data}], error => [id, {status: 'failed', error}])));
    const ended = results.find(([, result]) => result.error?.kind === 'signed_out');
    if (ended) throw ended[1].error;
    return Object.fromEntries(results);
  });
  return id => read.status === 'ready' ? read.data[id] : read.status === 'failed' ? {status: 'failed', error: read.error} : {status: 'loading'};
}

export function CardEvidence({card, receiptFor, params, who}) {
  const openUnit = Number(params.get('unit')) || null;
  const units = Array.isArray(card.units) ? card.units : [];
  const gaps = Array.isArray(card.gaps) ? card.gaps : [];
  // Closing a unit returns focus to the control that opened it.
  const previous = useRef(openUnit);
  useEffect(() => {
    const prior = previous.current;
    previous.current = openUnit;
    if (prior && !openUnit) document.getElementById(`api-unit-${prior}-inspect`)?.focus();
  }, [openUnit]);
  return <>
    <CardFacts card={card}/>
    <h3 className="api-section-title">{units.length} numbered units</h3>
    {units.map(unit => <UnitRow key={unit.n} unit={unit} receipt={receiptFor(unit.receipt_id)} open={unit.n === openUnit} params={params} who={who} cardId={card.id}/>)}
    {openUnit && !units.some(unit => unit.n === openUnit) && <p className="api-notice" role="status">This card has no unit {openUnit}. No other unit is shown in its place.</p>}
    <section className="library-gaps" aria-labelledby={gapsTarget.evidence}>
      <h3 id={gapsTarget.evidence} tabIndex={-1}>What was not read or not shown</h3>
      {gaps.length ? <ul className="coverage-list coverage-gaps">{gaps.map(gap => <li className="coverage-row" key={gap.code} data-gap-code={gap.code}>
        <CircleDashed size={20} aria-hidden="true"/><div><strong>{gap.text}</strong><small>{gapDispositions[gap.disposition] || gap.disposition} · {gap.code}{gap.count != null && ` · count ${gap.count}`}</small></div>
      </li>)}</ul> : <p>The card states no gaps. That is the card’s statement, not a search of every source.</p>}
    </section>
  </>;
}

function EvidenceFromServer({selection}) {
  const {params, card, who} = selection;
  const receiptFor = useReceipts(card.status === 'ready' ? card.data : null, who);
  return <section className="plain-panel evidence-library api-evidence">
    <div className="panel-header"><div><div className="section-kicker">SOURCE LIBRARY · FROM THE SERVER</div><h2>The sealed card, unit by unit.</h2></div></div>
    <AccountPicker selection={selection}/>
    <CardList selection={selection}/>
    {selection.cardId && selection.listed && (card.status === 'failed' ? <Refusal error={card.error} retry={card.retry}/>
      : card.status !== 'ready' ? <p className="api-loading" role="status">Reading the card…</p>
        : <CardEvidence card={card.data} receiptFor={receiptFor} params={params} who={who}/>)}
    <div className="library-notice"><Info size={17}/><p>Read from the server as the signed-in principal. Quotations are the server’s neutralized text, shown exactly. Times are UTC.</p></div>
  </section>;
}

export default function ApiEvidence() {
  const {state} = useServer();
  const selection = useCardSelection(state.session?.signed_in ? state.session : null);
  return <>
    <ApiRenewalHeader page="evidence" selection={selection}/>
    <ApiFrame>{() => <EvidenceFromServer selection={selection}/>}</ApiFrame>
  </>;
}
