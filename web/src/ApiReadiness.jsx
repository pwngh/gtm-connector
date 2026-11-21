import React, {useId} from 'react';
import {Link} from 'react-router';
import {ArrowUpRight, CircleDashed, Clock, GitBranch, ShieldCheck} from 'lucide-react';
import {gapDispositions, unitFacts} from './api.js';
import {ApiFrame, ApiRenewalHeader, AccountPicker, CardList, Refusal, Time, gapsTarget, keepQuery, useCardSelection} from './ApiFrame.jsx';
import {useServer} from './ServerSwitch.jsx';
import {serverPaths} from './app/routes.js';

// Readiness when the Go server serves the app, from one card's gaps and units
// only. The server has no workflow rules yet, so none is listed or evaluated:
// no result, and no blocker count, is inferred. A gap the card states as not
// read or not shown is an open item; a note is the card's note, not an item.

const plural = (count, noun) => `${count} ${noun}${count === 1 ? '' : 's'}`;
const opens = gap => gap.disposition === 'not_read' || gap.disposition === 'read_not_shown';

export function unitCounts(units) {
  const facts = units.map(unitFacts);
  return {
    total: facts.length,
    sentences: facts.filter(unit => unit.kind === 'sentence').length,
    records: facts.filter(unit => unit.kind === 'record').length,
    notShown: facts.filter(unit => unit.notShown).length,
    partial: facts.filter(unit => unit.partial).length,
    customer: facts.filter(unit => unit.role === 'customer').length,
  };
}

export function CardReadiness({card, evidenceHref}) {
  const id = useId();
  const gaps = Array.isArray(card.gaps) ? card.gaps : [];
  const open = gaps.filter(opens), notes = gaps.filter(gap => !opens(gap));
  const counts = unitCounts(Array.isArray(card.units) ? card.units : []);
  return <>
    <dl className="rd-position" aria-label="Three separate renewal facts">
      <div data-fact="crm"><dt>CRM stage</dt><dd>Not read here<span className="rd-detail">The card view carries no CRM fields.</span></dd></div>
      <div data-fact="readiness"><dt>Requirement readiness</dt><dd>Not evaluated<span className="rd-detail">{plural(open.length, 'open item')} from the card’s gaps.</span><span className="rd-label">This page reads no requirement records.</span></dd></div>
      <div data-fact="commercial"><dt>Commercial outcome</dt><dd>Not read here<span className="rd-detail">This page reads only the card.</span></dd></div>
    </dl>
    <div className="rd-evaluation-line"><span><Clock size={17} aria-hidden="true"/>Card published: <Time value={card.published_at}/></span><Link className="rd-inline-link" to={evidenceHref}>Open this card’s evidence<ArrowUpRight size={16} aria-hidden="true"/></Link></div>
    <div className="rd-workbench">
      <section className="rd-requirements" aria-labelledby={gapsTarget.readiness}>
        <div className="rd-section-heading"><div><span className="rd-kicker">STATED BY THE CARD</span><h2 id={gapsTarget.readiness} tabIndex={-1}>Open items: sources and gaps</h2></div><ShieldCheck size={22} aria-hidden="true"/></div>
        <p className="rd-result-count" role="status">{open.length ? `${plural(open.length, 'open item')}, one per source the card says it did not read or show. This is not a complete inventory.` : 'The card states nothing it did not read or show. That is not evidence that nothing is open.'}</p>
        <div className="rd-requirement-list">{open.map(gap => <article className="rd-requirement rd-paper" key={gap.code} data-open-gap={gap.code}>
          <div className="rd-item-head"><div><span className="rd-kicker">{gapDispositions[gap.disposition]}</span><h3>{gap.text}</h3></div></div>
          <div className="rd-record-line"><span className="rd-requirement-state open">Open</span><span className="rd-label">{gap.code}{gap.count != null && ` · count ${gap.count}`}</span></div>
          <p>What closes it is not recorded. The server records no resolution for a gap yet.</p>
        </article>)}</div>
        {notes.length > 0 && <section className="rd-paper api-card-notes" aria-labelledby={`${id}-notes`}>
          <h3 id={`${id}-notes`}>Notes on this card</h3>
          <ul>{notes.map(gap => <li key={gap.code} data-note-gap={gap.code}><p>{gap.text}</p><p className="rd-label">{gapDispositions[gap.disposition] || gap.disposition} · {gap.code}{gap.count != null && ` · count ${gap.count}`}</p></li>)}</ul>
        </section>}
        <section className="rd-paper api-unit-summary" aria-labelledby={`${id}-units`}>
          <h3 id={`${id}-units`}>Units on this card</h3>
          <p>{plural(counts.total, 'numbered unit')}: {plural(counts.sentences, 'sentence')} and {plural(counts.records, 'record')}. {plural(counts.customer, 'sentence')} by a speaker in the customer role. {plural(counts.notShown, 'unit')} not shown on the card, {plural(counts.partial, 'unit')} cut.</p>
          <p className="rd-label">A customer role is not customer decision authority. Who decides is unidentified.</p>
        </section>
      </section>
      <div className="rd-check-column">
        <section className="rd-transition-section" aria-labelledby={`${id}-rules`}>
          <div className="rd-section-heading"><div><span className="rd-kicker">WORKFLOW RULES</span><h2 id={`${id}-rules`}>Readiness by transition</h2></div><GitBranch size={22} aria-hidden="true"/></div>
          <div className="rd-check api-no-rules"><div className="rd-check-head"><h3>Workflow rules for this account</h3><span className="rd-result not_evaluated" data-check-result="not_evaluated"><CircleDashed size={16} aria-hidden="true"/>Not evaluated</span></div>
            <p className="rd-check-explanation">The server has no workflow rules yet, so none is listed or evaluated. No result is known, and a blocker count is unknown, which is not zero. A unit’s role or text establishes no rule.</p></div>
        </section>
      </div>
    </div>
  </>;
}

export function ReadinessFromServer({selection}) {
  const {account, card, cardId} = selection;
  return <>
    <div className="api-picker"><AccountPicker selection={selection}/><CardList selection={selection}/></div>
    {selection.cardId && selection.listed && (card.status === 'failed' ? <Refusal error={card.error} retry={card.retry}/>
      : card.status !== 'ready' ? <p className="api-loading" role="status">Reading the card…</p>
        : <CardReadiness card={card.data} evidenceHref={serverPaths.evidence + keepQuery(account, cardId)}/>)}
  </>;
}

export default function ApiReadiness() {
  const {state} = useServer();
  const selection = useCardSelection(state.session?.signed_in ? state.session : null);
  return <>
  <ApiRenewalHeader page="readiness" selection={selection}/>
  <div className="rd-page api-readiness">
    <div className="rd-intro"><div><span className="rd-kicker">READINESS · FROM THE SERVER’S CARD</span><h2 tabIndex={-1}>Know what can move next.</h2>
      <p>Open items come from the sources the card says it did not read or show. The server has no workflow rules yet, so none is evaluated.</p></div></div>
    <ApiFrame>{() => <ReadinessFromServer selection={selection}/>}</ApiFrame>
    <p className="rd-footnote">Read from the server as the signed-in principal.</p>
  </div>
  </>;
}
