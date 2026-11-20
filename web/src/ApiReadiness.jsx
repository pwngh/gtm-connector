import React, {useId} from 'react';
import {Link} from 'react-router';
import {ArrowUpRight, CircleDashed, Clock, GitBranch, ShieldCheck} from 'lucide-react';
import {gapDispositions, unitFacts} from './api.js';
import {ApiFrame, ApiRenewalHeader, AccountPicker, CardList, Refusal, Time, gapsTarget, useCardSelection} from './ApiFrame.jsx';
import {useServer} from './ServerSwitch.jsx';
import {readinessFixture} from './renewalFixtures.js';
import './readiness.css';

// Readiness when the Go server serves the app, from one card's gaps and units
// only. The server evaluates no workflow rule yet, so every rule is shown as
// not evaluated: no result, and no blocker count, is inferred.

const plural = (count, noun) => `${count} ${noun}${count === 1 ? '' : 's'}`;
export const workflowRules = readinessFixture.checks.map(({id, kind, title}) => ({id, kind, title}));

function NotEvaluated() {
  return <span className="rd-result not_evaluated" data-check-result="not_evaluated"><CircleDashed size={16} aria-hidden="true"/>Not evaluated</span>;
}

function RuleList({rules}) {
  return <ol className="rd-checks">{rules.map(rule => <li className="rd-check" key={rule.id} data-check-id={rule.id}>
    <div className="rd-check-head"><h3>{rule.title}</h3><NotEvaluated/></div>
    <p className="rd-check-explanation">The server does not evaluate this rule yet. No result is known, and the blocker count is unknown, not zero.</p>
  </li>)}</ol>;
}

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
  const counts = unitCounts(Array.isArray(card.units) ? card.units : []);
  const transitions = workflowRules.filter(rule => rule.kind === 'transition');
  const evidenceRules = workflowRules.filter(rule => rule.kind === 'evidence');
  return <>
    <dl className="rd-position" aria-label="Three separate renewal facts">
      <div data-fact="crm"><dt>CRM stage</dt><dd>Not read here<span className="rd-detail">The card view does not carry CRM fields.</span></dd></div>
      <div data-fact="readiness"><dt>Requirement readiness</dt><dd>Not evaluated<span className="rd-detail">{plural(gaps.length, 'stated gap')} open on this card.</span><span className="rd-label">No requirement is recorded on the server yet.</span></dd></div>
      <div data-fact="commercial"><dt>Commercial outcome</dt><dd>Not recorded<span className="rd-detail">Not inferred from the card.</span></dd></div>
    </dl>
    <div className="rd-evaluation-line"><span><Clock size={17} aria-hidden="true"/>Card published: <Time value={card.published_at}/></span><Link className="rd-inline-link" to={evidenceHref}>Open this card’s evidence<ArrowUpRight size={16} aria-hidden="true"/></Link></div>
    <div className="rd-workbench">
      <section className="rd-requirements" aria-labelledby={gapsTarget.readiness}>
        <div className="rd-section-heading"><div><span className="rd-kicker">STATED BY THE CARD</span><h2 id={gapsTarget.readiness} tabIndex={-1}>Open items: sources and gaps</h2></div><ShieldCheck size={22} aria-hidden="true"/></div>
        <p className="rd-result-count" role="status">{gaps.length ? `${plural(gaps.length, 'open item')}, one per stated gap. Gaps are what the card says it did not read or show, not a complete inventory.` : 'The card states no gaps. That is not evidence that nothing is open.'}</p>
        <div className="rd-requirement-list">{gaps.map(gap => <article className="rd-requirement rd-paper" key={gap.code} data-gap-code={gap.code}>
          <div className="rd-item-head"><div><span className="rd-kicker">{gapDispositions[gap.disposition] || gap.disposition}</span><h3>{gap.text}</h3></div></div>
          <div className="rd-record-line"><span className="rd-requirement-state open">Open</span><span className="rd-label">{gap.code}{gap.count != null && ` · count ${gap.count}`}</span></div>
          <p>What closes it is not recorded. The server records no resolution for a gap yet.</p>
        </article>)}</div>
        <section className="rd-paper api-unit-summary" aria-labelledby={`${id}-units`}>
          <h3 id={`${id}-units`}>Units on this card</h3>
          <p>{plural(counts.total, 'numbered unit')}: {plural(counts.sentences, 'sentence')} and {plural(counts.records, 'record')}. {plural(counts.customer, 'sentence')} by a speaker in the customer role. {plural(counts.notShown, 'unit')} not shown on the card, {plural(counts.partial, 'unit')} cut.</p>
          <p className="rd-label">A customer role is not customer decision authority. Who decides is unidentified.</p>
        </section>
      </section>
      <div className="rd-check-column">
        <section className="rd-transition-section" aria-labelledby={`${id}-transitions`}>
          <div className="rd-section-heading"><div><span className="rd-kicker">WORKFLOW RULES</span><h2 id={`${id}-transitions`}>Readiness by transition</h2></div><GitBranch size={22} aria-hidden="true"/></div>
          <p className="rd-result-count">{plural(transitions.length, 'transition check')}, none evaluated by the server yet.</p>
          <RuleList rules={transitions}/>
        </section>
        <section className="rd-evidence-checks" aria-labelledby={`${id}-evidence`}><span className="rd-kicker">EVIDENCE RULES</span><h2 id={`${id}-evidence`}>Evidence checks</h2>
          <p>None is evaluated by the server yet. A unit’s role or text does not establish any of them.</p>
          <RuleList rules={evidenceRules}/>
        </section>
      </div>
    </div>
  </>;
}

function ReadinessFromServer({selection}) {
  const {account, card} = selection;
  const evidence = new URLSearchParams({account});
  if (selection.cardId) evidence.set('card', selection.cardId);
  return <>
    <div className="api-picker"><AccountPicker selection={selection}/><CardList selection={selection}/></div>
    {selection.cardId && selection.listed && (card.status === 'failed' ? <Refusal error={card.error} retry={card.retry}/>
      : card.status !== 'ready' ? <p className="api-loading" role="status">Reading the card…</p>
        : <CardReadiness card={card.data} evidenceHref={`${readinessFixture.paths.evidence}?${evidence}`}/>)}
  </>;
}

export default function ApiReadiness() {
  const {state} = useServer();
  const selection = useCardSelection(state.session?.signed_in ? state.session : null);
  return <>
  <ApiRenewalHeader page="readiness" selection={selection}/>
  <div className="rd-page api-readiness">
    <div className="rd-intro"><div><span className="rd-kicker">READINESS · FROM THE SERVER’S CARD</span><h2 tabIndex={-1}>Know what can move next.</h2>
      <p>Open items come from the card’s stated gaps. The server does not evaluate workflow rules yet, so each one is shown as not evaluated.</p></div></div>
    <ApiFrame>{() => <ReadinessFromServer selection={selection}/>}</ApiFrame>
    <p className="rd-footnote">Read from the server as the signed-in principal. Nothing here is a walkthrough record.</p>
  </div>
  </>;
}
