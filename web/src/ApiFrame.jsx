import React, {useEffect, useId, useRef, useState} from 'react';
import {Link, NavLink, useSearchParams} from 'react-router';
import {AlertCircle, GitBranch, Info, LogOut, Server, ShieldCheck} from 'lucide-react';
import {formatUtc, sessionExpired} from './api.js';
import {checkCard, resolveAccount, resolveCard} from './selection.js';
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
    case 'redirect': return {title: 'The server redirected this request.', detail: 'The redirect was not followed, and nothing was read.'};
    case 'mismatch': return {title: 'The server returned another card.', detail: 'It is not the card of this account that was asked for, so nothing from it is shown.'};
    case 'origin': case 'csrf': return {title: 'The server refused this request.', detail: 'It was refused even with a fresh sign-in token. Reload the page and try again.'};
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

// Reads once per key, and again when the key or the session generation
// changes. An answer for a key that is no longer asked for is dropped, and a
// read refused because the session is gone ends the session here.
export function useApiRead(key, load) {
  const {store, state} = useServer();
  const full = key === null ? null : `${state.generation}|${key}`;
  const [result, setResult] = useState({key: null, status: 'idle'});
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (full === null) return undefined;
    let live = true;
    load(store.api).then(data => {if (live) setResult({key: full, status: 'ready', data});}, error => {
      if (!live) return;
      setResult({key: full, status: 'failed', error});
      if (error?.kind === 'signed_out') store.expire();
    });
    return () => {live = false;};
  }, [full, attempt]);
  const retry = () => {setResult({key: null, status: 'idle'}); setAttempt(value => value + 1);};
  if (full === null) return {status: 'idle', retry};
  return result.key === full ? {...result, retry} : {key: full, status: 'loading', retry};
}

function SignInForm({store, ended, elsewhere}) {
  const id = useId();
  const password = useRef(null);
  const [login, setLogin] = useState(''), [secret, setSecret] = useState('');
  const [pending, setPending] = useState(false), [error, setError] = useState(null);
  // A refused sign-in returns focus to the password, which was cleared.
  useEffect(() => {if (error && error.kind !== 'already_signed_in') password.current?.focus();}, [error]);
  async function submit(event) {
    event.preventDefault();
    if (pending) return;
    setPending(true); setError(null);
    try {
      await store.signIn(login, secret);
    } catch (failure) {
      setError(failure);
      setSecret(''); setPending(false);
    }
  }
  return <section className="api-signin" aria-labelledby="api-signin-title">
    <div className="section-kicker">SIGN IN · DEMO WORKSPACE</div>
    <h2 id="api-signin-title" tabIndex={-1}>Sign in to read from the server.</h2>
    {ended && <p className="api-notice">Your session ended or was revoked. You are signed out.</p>}
    {elsewhere?.principal && <div className="api-elsewhere api-problem" role="alert"><AlertCircle size={20} aria-hidden="true"/><div>
      <strong>This browser is already signed in as {elsewhere.principal.display_name}, from another tab.</strong>
      <p>The login you typed was not checked. Continue as that principal, or sign in again to check yours.</p>
      <button type="button" className="button" onClick={() => store.adoptElsewhere()}>Continue as {elsewhere.principal.display_name}</button></div></div>}
    <p>Use this install’s demo sign-in. It signs in a demo principal, which reads only the accounts it holds a grant for.</p>
    <form onSubmit={submit} className="api-signin-form">
      <label className="field" htmlFor={`${id}-login`}>Login<input id={`${id}-login`} name="login" autoComplete="username" autoCapitalize="none" spellCheck={false} required value={login} onChange={event => setLogin(event.target.value)}/></label>
      <label className="field" htmlFor={`${id}-password`}>Password<input ref={password} id={`${id}-password`} name="password" type="password" autoComplete="current-password" required value={secret} onChange={event => setSecret(event.target.value)}/></label>
      {error && error.kind !== 'already_signed_in' && (error.kind === 'sign_in_refused'
        ? <p className="api-problem" role="alert"><AlertCircle size={20} aria-hidden="true"/><span>The login or password is not right.</span></p>
        : <Refusal error={error}/>)}
      <button type="submit" className="button primary" aria-disabled={pending || undefined}>{pending ? 'Signing in…' : 'Sign in'}</button>
    </form>
  </section>;
}

export function SessionBar({store, session}) {
  const [pending, setPending] = useState(false), [error, setError] = useState(null);
  const principal = session.principal;
  async function signOut() {
    if (pending) return;
    setPending(true); setError(null);
    try {await store.signOut();} catch (failure) {setError(failure); setPending(false);}
  }
  return <div className="api-session" data-principal-kind={principal.demo ? 'demo' : principal.kind}>
    <ShieldCheck size={20} aria-hidden="true"/>
    <div className="api-session-who"><span className="api-label">Signed in{principal.demo ? ' as a demo principal' : ''}</span>
      <strong id="api-session-name" tabIndex={-1}>{principal.display_name}</strong>
      <span className="api-label">{principal.demo ? 'Demo principal · demo workspace · invented records' : `Principal · ${principal.workspace_kind} workspace`} · session until <Time value={session.expires_at}/></span></div>
    <button type="button" className="button" onClick={signOut} aria-disabled={pending || undefined}><LogOut size={16} aria-hidden="true"/>Sign out</button>
    {error && <Refusal error={error}/>}
  </div>;
}

// The page under a live, signed-in session, or what stands in the way of one.
// A session past its expiry is never shown as signed in. When the session
// starts or ends here, focus goes to the session or to the sign-in heading.
export function ApiFrame({children}) {
  const {store, state} = useServer();
  const expired = sessionExpired(state.session, Date.now());
  const signedIn = Boolean(state.session?.signed_in) && !expired;
  useEffect(() => {if (expired) store.check();}, [expired]);
  const previous = useRef(signedIn);
  useEffect(() => {
    const was = previous.current;
    previous.current = signedIn;
    if (was === signedIn) return;
    const active = document.activeElement;
    if (active && active !== document.body && active.isConnected) return;
    focusElement(document.getElementById(signedIn ? 'api-session-name' : 'api-signin-title'));
  }, [signedIn]);
  const announce = signedIn ? `Signed in as ${state.session.principal?.display_name}.` : state.ended || expired ? 'Your session ended. You are signed out.' : '';
  const announcer = <p className="api-announce" role="status">{announce}</p>;
  if (state.mode === 'unreachable' || (!state.session && state.error)) {
    return <div className="api-frame">{announcer}<Refusal error={state.error} retry={() => store.refresh()}/></div>;
  }
  if (!signedIn) return <div className="api-frame">{announcer}{state.error && <Refusal error={state.error} retry={() => store.refresh()}/>}<SignInForm store={store} ended={state.ended || expired} elsewhere={state.elsewhere}/></div>;
  return <div className="api-frame">
    {announcer}
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
  const accounts = useApiRead(signedIn ? 'accounts' : null, api => api.accounts());
  const granted = accounts.status === 'ready' && Array.isArray(accounts.data?.accounts) ? accounts.data.accounts : null;
  const asked = params.get('account')?.trim() || null;
  const {account, verified, invalid, name} = resolveAccount(asked, granted);
  useEffect(() => {
    if (!asked && account) setParams(current => new URLSearchParams(selectionHref(current, {account}).slice(1)), {replace: true, preventScrollReset: true});
  }, [asked, account]);
  const readable = signedIn && account && !(granted && !granted.length);
  const cards = useApiRead(readable ? `cards:${account}` : null, api => api.accountCards(account));
  const listed = cards.status === 'ready' && Array.isArray(cards.data?.cards) ? cards.data.cards : null;
  const {cardId, mismatch} = resolveCard(params.get('card'), listed);
  const read = useApiRead(cardId ? `card:${account}:${cardId}` : null, api => api.card(cardId));
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
const readinessPath = `${renewalFixture.paths.root}/readiness`;

// The query that keeps the chosen account and open card across the two server pages.
export function keepQuery(account, cardId) {
  const keep = new URLSearchParams(account ? {account} : {});
  if (cardId) keep.set('card', cardId);
  return String(keep) ? `?${keep}` : '';
}

// The renewal header on a page that reads from the server. It shows only what
// the API supports: a granted account by its server name, an account from the
// link marked as not verified, and the open card's ID and publish time. It
// links only the pages that read from the server.
export function ApiRenewalHeader({page, selection}) {
  const {account, verified, invalid, accountName, cardId} = selection;
  const card = selection.card.status === 'ready' ? selection.card.data : null;
  const query = keepQuery(account, cardId);
  const tabs = [['readiness', 'Readiness', readinessPath + query], ['evidence', 'Evidence', renewalFixture.paths.evidence + query]];
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
    <div className="detail-nav"><nav aria-label="Renewal sections">{tabs.map(([id, label, to]) => <NavLink key={id} to={to} className={id === page ? 'selected' : ''} aria-current={id === page ? 'page' : undefined}>{label}</NavLink>)}</nav>
      <span className="save-indicator">Nothing on this page is saved in the browser</span></div>
  </header>;
}
