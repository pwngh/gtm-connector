import React, {useEffect, useId, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {GitBranch, Info, Server} from 'lucide-react';
import {formatUtc} from './api/client.ts';
import {useRead, useServer} from './api/reads.ts';
import {checkCard, resolveAccount, resolveCard} from './api/selection.ts';
import {Refusal} from './ui/Refusal.tsx';
import {focusElement} from './focusElement.js';
import {Pill} from './WorkspaceControls.jsx';

// Shared by the Evidence and Readiness pages, which the frame shows only to a
// signed-in principal: refusals, and which account and card are open.

export function Time({value}) {
  return <time dateTime={value || undefined}>{formatUtc(value)}</time>;
}

export {Refusal, refusalText} from './ui/Refusal.tsx';

// The page's own area under the frame, which shows it only to a signed-in
// principal: a session refresh that failed, then the page.
export function ApiFrame({children}) {
  const {store, state} = useServer();
  return <div className="api-frame">
    {state.error && <Refusal error={state.error} retry={() => store.refresh()}/>}
    {children(state.session)}
  </div>;
}

export function selectionHref(params, changes) {
  const next = new URLSearchParams(params);
  for (const [key, value] of Object.entries(changes)) {
    if (value === null || value === undefined || value === '') next.delete(key); else next.set(key, String(value));
  }
  const search = next.toString();
  return search ? `?${search}` : '?';
}

// The principal's accounts, the chosen one, its cards, and the open card.
// The account is the one in ?account=; without one, the demo's account when
// it is granted, else the first, written back to the URL. The card is the one
// in ?card= when it is in that account's list, else the latest published.
// Every read is keyed by the account and the session generation, so an answer
// for an account or card no longer chosen is dropped.
export function useCardSelection(session) {
  const [params, setParams] = useSearchParams();
  const {state} = useServer();
  const signedIn = Boolean(session?.principal?.id);
  const accounts = useRead(signedIn ? 'accounts' : null, (api, signal) => api.accounts({signal}));
  const granted = accounts.status === 'ready' && Array.isArray(accounts.data?.accounts) ? accounts.data.accounts : null;
  const asked = params.get('account')?.trim() || null;
  const {account, verified, invalid, name} = resolveAccount(asked, granted);
  useEffect(() => {
    if (!asked && account) setParams(current => new URLSearchParams(selectionHref(current, {account}).slice(1)), {replace: true, preventScrollReset: true});
  }, [asked, account]);
  const readable = signedIn && account && !(granted && !granted.length);
  const cards = useRead(readable ? `cards:${account}` : null, (api, signal) => api.accountCards(account, {signal}));
  const listed = cards.status === 'ready' && Array.isArray(cards.data?.cards) ? cards.data.cards : null;
  const {cardId, mismatch} = resolveCard(params.get('card'), listed);
  const read = useRead(cardId ? `card:${account}:${cardId}` : null, (api, signal) => api.card(cardId, {signal}));
  const card = cardId ? checkCard(read, account, cardId) : read;
  return {params, accounts, granted, account, verified, invalid, accountName: verified ? name : null, cards, listed, cardId, cardMismatch: mismatch, card, generation: state.generation};
}

const plural = (count, noun) => `${count} ${noun}${count === 1 ? '' : 's'}`;

// The principal's accounts by name and ID. A choice is committed with the
// button, which replaces the URL's account; changing the list alone reads
// nothing. An account in the URL that is not on the list stays visible as
// such; reading it is refused by the server, and that refusal shows below.
export function AccountPicker({selection}) {
  const id = useId();
  const [, setParams] = useSearchParams();
  const {accounts, granted, account, params} = selection;
  const [choice, setChoice] = useState(account || '');
  useEffect(() => setChoice(account || ''), [account]);
  if (accounts.status === 'failed') return <Refusal error={accounts.error} retry={accounts.retry}/>;
  if (!granted) return accounts.status === 'ready' ? <Refusal error={{kind: 'unexpected'}}/> : <p className="api-loading" role="status">Reading your accounts…</p>;
  if (!granted.length) return <p className="api-notice api-no-accounts" role="status">No accounts are granted to you.</p>;
  const listed = granted.some(item => item.account === account);
  return <form className="api-account" onSubmit={event => {
    event.preventDefault();
    if (choice && choice !== account) setParams(new URLSearchParams(selectionHref(params, {account: choice, card: null, unit: null}).slice(1)), {replace: true});
  }}>
    <label className="field" htmlFor={`${id}-account`}>Account<select id={`${id}-account`} name="account" value={choice} onChange={event => setChoice(event.target.value)}>
      {account && !listed && <option value={account}>{account} · not in your accounts</option>}
      {granted.map(item => <option key={item.account} value={item.account}>{item.name ? `${item.name} · ${item.account}` : item.account}</option>)}
    </select></label>
    <button type="submit" className="button">Open account</button>
    <p className="api-label">{plural(granted.length, 'account')} granted to you</p>
  </form>;
}

export function CardList({selection}) {
  const {cards, listed, cardId, params, account, cardMismatch} = selection;
  if (!account || (selection.granted && !selection.granted.length)) return null;
  if (cards.status === 'loading' || cards.status === 'idle') return <p className="api-loading" role="status">Reading cards for {account}…</p>;
  if (cards.status === 'failed') return <Refusal error={cards.error} retry={cards.retry}/>;
  if (!listed) return <Refusal error={{kind: 'unexpected'}}/>;
  const mismatch = cardMismatch && <p className="api-notice api-card-mismatch" role="status">The card in this link is not one of {account}’s published cards. Nothing from it is shown.</p>;
  if (!listed.length) return <>{mismatch}<p className="api-notice" role="status">No card is published for {account}. Nothing about its sources is inferred from that.</p></>;
  return <div className="api-cards">{mismatch}<h3 className="api-label">Published cards for {account}, latest first</h3>
    <ul className="api-card-list">{listed.map(card => <li key={card.id}>
      <Link to={selectionHref(params, {card: card.id, unit: null})} aria-current={card.id === cardId ? 'true' : undefined} data-card-id={card.id}>
        <span>Card published <Time value={card.published_at}/></span><span className="api-label">Brief {card.brief_id}</span>
      </Link></li>)}</ul></div>;
}

export function CardFacts({card}) {
  return <dl className="api-facts">
    <div><dt>Card</dt><dd>{card.id}</dd></div>
    <div><dt>Account</dt><dd>{card.account}</dd></div>
    <div><dt>Brief</dt><dd>{card.brief_id}</dd></div>
    <div><dt>Published</dt><dd><Time value={card.published_at}/></dd></div>
    <div><dt>Schema and rules</dt><dd>Schema {card.schema_version ?? 'not recorded'} · rules {card.rules_version ?? 'not recorded'}</dd></div>
    <div><dt>Card bytes, SHA-256</dt><dd className="api-digest">{card.sha256}</dd></div>
    <div><dt>Seal receipt</dt><dd>{card.seal_receipt_id}</dd></div>
  </dl>;
}

export const gapsTarget = {evidence: 'api-evidence-gaps', readiness: 'api-readiness-gaps'};

// The query that keeps the chosen account and open card across the two server pages.
export function keepQuery(account, cardId) {
  const keep = new URLSearchParams(account ? {account} : {});
  if (cardId) keep.set('card', cardId);
  return String(keep) ? `?${keep}` : '';
}

// The renewal header on a page that reads from the server. It shows only what
// the API supports: a granted account by its server name, an account from the
// link marked as not verified, and the open card's ID and publish time. The
// frame links the pages.
export function ApiRenewalHeader({page, selection}) {
  const {account, verified, invalid, accountName} = selection;
  const card = selection.card.status === 'ready' ? selection.card.data : null;
  const heading = verified ? accountName || account : account ? 'Account not verified' : 'No account chosen';
  return <header className="api-renewal-header">
    <div className="page-heading api-heading"><div><div className="eyebrow">RENEWAL WORKSPACE <span> / </span> READ FROM THE SERVER</div>
      <h1>{heading}<span className="company-dot">.</span></h1>
      <div className="account-meta">
        {invalid ? <><span>The account in this link is not an account ID. It was not read.</span><i/></>
          : account && <><span>Account ID {account}{!verified && ', not verified'}</span><i/></>}
        {card ? <span data-card-id={card.id}>Card {card.id} · published <Time value={card.published_at}/></span> : <span>No card open</span>}<i/><span>Renewal date, agreement and owner: not available from the server yet</span></div></div>
      <div className="heading-actions">{page === 'readiness' ? <Pill icon={GitBranch}>Readiness by transition</Pill> : <Pill icon={Server}>Read from the server</Pill>}
        {card && <button type="button" className="text-button" data-target={gapsTarget[page]} onClick={() => focusElement(document.getElementById(gapsTarget[page]), {block: 'start'})}><Info size={16}/>Sources and gaps</button>}</div></div>
  </header>;
}
