import React, {createContext, use, useContext, useSyncExternalStore} from 'react';
import {useSearchParams} from 'react-router';
import {ChevronRight, UserRound} from 'lucide-react';
import {walkthroughStore} from './serverSession.js';
import {ApiEvidence, ApiReadiness} from './lazyPages.js';
import {Avatar} from './WorkspaceControls.jsx';

// Without a provider the app is the walkthrough: tests and the static build.
export const ServerContext = createContext(walkthroughStore);

export function useServer() {
  const store = useContext(ServerContext);
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
  return {store, state};
}

const pages = {evidence: ApiEvidence, readiness: ApiReadiness};

// Evidence and Readiness read from the server when it serves the app, and
// show the walkthrough otherwise. While the server is being asked, the page
// waits under the route's loading state; it never shows walkthrough data first.
export default function ServerSwitch({page, children}) {
  const {store, state} = useServer();
  if (state.mode === 'probing') use(store.start());
  const current = store.getSnapshot();
  if (current.mode === 'walkthrough') return children;
  const Page = pages[page];
  return <Page/>;
}

// Shell content that comes from walkthrough data. It shows only in walkthrough
// mode. When the server serves the app, `api` shows instead: everywhere, or,
// with `page`, only on the pages that read from the server. Nothing shows
// while the server is being asked, so walkthrough data never flashes first.
export function ByMode({page, api = null, children = null}) {
  const {state} = useServer();
  if (page !== undefined && !pages[page]) return children;
  if (state.mode === 'walkthrough') return children;
  if (state.mode === 'probing') return null;
  return api;
}

// The breadcrumb on a page that reads from the server: the account being read, no links.
export function ApiCrumbs() {
  const [params] = useSearchParams();
  return <><span>Renewal</span><ChevronRight size={14}/><span>{params.get('account')?.trim() || 'No account chosen'}</span></>;
}

// The signed-in principal's mark; signed out, a plain figure.
export function PrincipalMark() {
  const {state} = useServer();
  const name = state.session?.signed_in ? state.session.principal?.display_name : null;
  return name ? <Avatar name={name}/> : <span className="avatar" aria-hidden="true"><UserRound size={16}/></span>;
}

// Commands and records from the walkthrough say so when the server serves the app.
export const walkthroughItems = items => items.map(item => ({...item, sub: `Walkthrough · ${item.sub}`}));
