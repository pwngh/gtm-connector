import React, {useEffect, useId, useState} from 'react';
import {Link, NavLink, useSearchParams} from 'react-router';
import {AlertCircle, GitBranch, Info, LogOut, Server, ShieldCheck} from 'lucide-react';
import {demoAccount, formatUtc} from './api.js';
import {useServer} from './ServerSwitch.jsx';
import {focusElement} from './focusElement.js';
import {renewalFixture} from './renewalFixtures.js';
import {Pill} from './WorkspaceControls.jsx';

// Shared by the Evidence and Readiness pages when the Go server serves the
// app: the signed-in principal, sign-in, refusals, and which card is open.

export function Time({value}) {
  return <time dateTime={value || undefined}>{formatUtc(value)}</time>;
}

// What a refusal or failure means for the person reading the page. A refusal
// is never shown as an empty result.
export function refusalText(error) {
  switch (error?.kind) {
    case 'not_permitted': return {title: 'Not permitted.', detail: 'The signed-in principal holds no grant for this, or nothing by this ID is visible to it. This is a refusal, not an empty result.'};
    case 'signed_out': return {title: 'Your session ended.', detail: 'Sign in again to read from the server.'};
    case 'not_found': return {title: 'Not found.', detail: 'The server has nothing at this address.'};
    case 'unreachable': return {title: 'The server did not answer.', detail: 'Nothing was read. No walkthrough data is shown in its place.'};
    case 'server': return {title: 'The server could not answer.', detail: 'Nothing was read. Try again in a moment.'};
    case 'host': return {title: 'This address is not served.', detail: 'Open the app at the server’s configured address.'};
    case 'origin': case 'csrf': return {title: 'The server refused this request.', detail: 'Reload the page and try again.'};
    default: return {title: 'The server’s answer could not be read.', detail: 'Nothing is shown in its place.'};
  }
}

export function Refusal({error, retry, className = ''}) {
  const {title, detail} = refusalText(error);
  return <div className={`api-problem ${className}`} role="alert" data-refusal={error?.kind || 'unexpected'}>
    <AlertCircle size={20} aria-hidden="true"/>
    <div><strong>{title}</strong><p>{detail}</p>{error?.code && <p className="api-label">Server code: {error.code}</p>}
      {retry && <button type="button" className="button" onClick={retry}>Try again</button>}</div>
  </div>;
}

// Reads once per key, and again when the key changes. A read refused because
// the session is gone marks the session ended.
export function useApiRead(key, load) {
  const {store} = useServer();
  const [result, setResult] = useState({key: null, status: 'idle'});
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (key === null) return undefined;
    let live = true;
    load(store.api).then(data => {if (live) setResult({key, status: 'ready', data});}, error => {
      if (!live) return;
      setResult({key, status: 'failed', error});
      if (error?.kind === 'signed_out') store.expire();
    });
    return () => {live = false;};
  }, [key, attempt]);
  const retry = () => {setResult({key: null, status: 'idle'}); setAttempt(value => value + 1);};
  if (key === null) return {status: 'idle', retry};
  return result.key === key ? {...result, retry} : {key, status: 'loading', retry};
}

function SignInForm({store, ended}) {
  const id = useId();
  const [login, setLogin] = useState(''), [password, setPassword] = useState('');
  const [pending, setPending] = useState(false), [error, setError] = useState(null);
  async function submit(event) {
    event.preventDefault();
    setPending(true); setError(null);
    try {
      await store.signIn(login, password);
    } catch (failure) {
      setError(failure);
    } finally {
      setPassword(''); setPending(false);
    }
  }
  return <section className="api-signin" aria-labelledby={`${id}-title`}>
    <div className="section-kicker">SIGN IN · DEMO WORKSPACE</div>
    <h2 id={`${id}-title`}>Sign in to read from the server.</h2>
    {ended && <p className="api-notice" role="status">Your session ended or was revoked. You are signed out.</p>}
    <p>Use this install’s demo sign-in. It signs in a demo principal, which reads only the accounts it holds a grant for.</p>
    <form onSubmit={submit} className="api-signin-form">
      <label className="field" htmlFor={`${id}-login`}>Login<input id={`${id}-login`} name="login" autoComplete="username" autoCapitalize="none" spellCheck={false} required value={login} onChange={event => setLogin(event.target.value)}/></label>
      <label className="field" htmlFor={`${id}-password`}>Password<input id={`${id}-password`} name="password" type="password" autoComplete="current-password" required value={password} onChange={event => setPassword(event.target.value)}/></label>
      {error && (error.kind === 'sign_in_refused'
        ? <p className="api-problem" role="alert"><AlertCircle size={20} aria-hidden="true"/><span>The login or password is not right.</span></p>
        : <Refusal error={error}/>)}
      <button type="submit" className="button primary" disabled={pending}>{pending ? 'Signing in…' : 'Sign in'}</button>
    </form>
  </section>;
}

function SessionBar({store, session}) {
  const [pending, setPending] = useState(false), [error, setError] = useState(null);
  const principal = session.principal;
  async function signOut() {
    setPending(true); setError(null);
    try {await store.signOut();} catch (failure) {setError(failure); setPending(false);}
  }
  return <div className="api-session" data-principal-kind={principal.demo ? 'demo' : principal.kind}>
    <ShieldCheck size={20} aria-hidden="true"/>
    <div className="api-session-who"><span className="api-label">Signed in{principal.demo ? ' as a demo principal' : ''}</span>
      <strong>{principal.display_name}</strong>
      <span className="api-label">{principal.demo ? 'Demo principal · demo workspace · invented records' : `Principal · ${principal.workspace_kind} workspace`} · session until <Time value={session.expires_at}/></span></div>
    <button type="button" className="button" onClick={signOut} disabled={pending}><LogOut size={16} aria-hidden="true"/>Sign out</button>
    {error && <Refusal error={error}/>}
  </div>;
}

// The page under a live, signed-in session, or what stands in the way of one.
export function ApiFrame({children}) {
  const {store, state} = useServer();
  if (state.mode === 'unreachable' || (!state.session && state.error)) {
    return <div className="api-frame"><Refusal error={state.error} retry={() => store.refresh()}/></div>;
  }
  if (!state.session?.signed_in) return <div className="api-frame"><SignInForm store={store} ended={state.ended}/></div>;
  return <div className="api-frame">
    <SessionBar store={store} session={state.session}/>
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

// The account in the URL, its cards, and the open card: the one in the URL,
// else the latest published. Nothing is read without a signed-in session.
export function useCardSelection(session) {
  const [params] = useSearchParams();
  const account = params.get('account')?.trim() || demoAccount;
  const who = session?.principal?.id || null;
  const cards = useApiRead(who ? `cards:${who}:${account}` : null, api => api.accountCards(account));
  const listed = cards.status === 'ready' && Array.isArray(cards.data?.cards) ? cards.data.cards : null;
  const cardId = params.get('card') || listed?.[0]?.id || null;
  const card = useApiRead(cardId && listed ? `card:${who}:${cardId}` : null, api => api.card(cardId));
  return {params, account, cards, listed, cardId, card, who};
}

export function AccountForm({account, params}) {
  const id = useId();
  const [value, setValue] = useState(account);
  const [, setParams] = useSearchParams();
  useEffect(() => setValue(account), [account]);
  return <form className="api-account" onSubmit={event => {event.preventDefault(); setParams(new URLSearchParams(selectionHref(params, {account: value.trim(), card: null, unit: null}).slice(1)));}}>
    <label className="field" htmlFor={`${id}-account`}>Account ID<input id={`${id}-account`} name="account" autoCapitalize="none" spellCheck={false} required value={value} onChange={event => setValue(event.target.value)}/></label>
    <button type="submit" className="button">Show cards</button>
  </form>;
}

export function CardList({selection}) {
  const {cards, listed, cardId, params, account} = selection;
  if (cards.status === 'loading' || cards.status === 'idle') return <p className="api-loading" role="status">Reading cards for {account}…</p>;
  if (cards.status === 'failed') return <Refusal error={cards.error} retry={cards.retry}/>;
  if (!listed) return <Refusal error={{kind: 'unexpected'}}/>;
  if (!listed.length) return <p className="api-notice" role="status">No card is published for {account}. Nothing about its sources is inferred from that.</p>;
  return <div className="api-cards"><h3 className="api-label">Published cards for {account}, latest first</h3>
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
const readinessPath = `${renewalFixture.paths.root}/readiness`;

// The renewal header on a page that reads from the server. It shows only what
// the API supports: the account being read, and the open card's ID and
// publish time. It links only the pages that read from the server; the
// walkthrough pages stay in the sidebar, marked as such.
export function ApiRenewalHeader({page, selection}) {
  const {account, cardId} = selection;
  const card = selection.card.status === 'ready' ? selection.card.data : null;
  const keep = new URLSearchParams({account});
  if (cardId) keep.set('card', cardId);
  const tabs = [['readiness', 'Readiness', `${readinessPath}?${keep}`], ['evidence', 'Evidence', `${renewalFixture.paths.evidence}?${keep}`]];
  return <header className="api-renewal-header">
    <div className="page-heading api-heading"><div><div className="eyebrow">RENEWAL WORKSPACE <span> / </span> READ FROM THE SERVER</div>
      <h1>{account}<span className="company-dot">.</span></h1>
      <div className="account-meta"><span>Account ID</span><i/>{card ? <span data-card-id={card.id}>Card {card.id} · published <Time value={card.published_at}/></span> : <span>No card open</span>}<i/><span>Renewal date, agreement and owner: not available from the server yet</span></div></div>
      <div className="heading-actions">{page === 'readiness' ? <Pill icon={GitBranch}>Readiness by transition</Pill> : <Pill icon={Server}>Read from the server</Pill>}
        {card && <button type="button" className="text-button" data-target={gapsTarget[page]} onClick={() => focusElement(document.getElementById(gapsTarget[page]), {block: 'start'})}><Info size={16}/>Sources and gaps</button>}</div></div>
    <div className="detail-nav"><nav aria-label="Renewal sections">{tabs.map(([id, label, to]) => <NavLink key={id} to={to} className={id === page ? 'selected' : ''} aria-current={id === page ? 'page' : undefined}>{label}</NavLink>)}</nav>
      <span className="save-indicator">Nothing on this page is saved in the browser</span></div>
  </header>;
}
