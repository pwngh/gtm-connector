import React, {createContext, use, useContext, useSyncExternalStore} from 'react';
import {ChevronRight, Compass, FileText, GitBranch, Layers, UserRound} from 'lucide-react';
import {renewalFixture} from './renewalFixtures.js';
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

// The breadcrumb on a page that reads from the server. It names no account:
// the link's text is not trusted, and the page heading shows the verified one.
export function ApiCrumbs() {
  return <><span>Renewal</span><ChevronRight size={14}/><span>Read from the server</span></>;
}

// The signed-in principal's mark; signed out, a plain figure.
export function PrincipalMark() {
  const {state} = useServer();
  const name = state.session?.signed_in ? state.session.principal?.display_name : null;
  return name ? <Avatar name={name}/> : <span className="avatar" aria-hidden="true"><UserRound size={16}/></span>;
}

// Commands and records from the walkthrough say so when the server serves the app.
export const walkthroughItems = items => items.map(item => ({...item, sub: `Walkthrough · ${item.sub}`}));

// The palette on a page that reads from the server: the two server pages, with
// the chosen account and open card kept, and explicit ways into the walkthrough.
// No walkthrough record or action opens over server data.
export function apiCommandItems(go, search) {
  const params = new URLSearchParams(search), keep = new URLSearchParams();
  for (const key of ['account', 'card']) if (params.get(key)) keep.set(key, params.get(key));
  const query = String(keep) ? `?${keep}` : '';
  return [
    {group: 'Read from the server', icon: FileText, label: 'Evidence', sub: 'The chosen account’s cards, units, receipts and gaps', onSelect: () => go(renewalFixture.paths.evidence + query)},
    {group: 'Read from the server', icon: GitBranch, label: 'Readiness', sub: 'The open card’s gaps; the server has no workflow rules yet', onSelect: () => go(`${renewalFixture.paths.root}/readiness${query}`)},
    {group: 'Walkthrough', icon: Compass, label: 'Open the walkthrough work plan', sub: 'Invented records, saved in this browser', onSelect: () => go(renewalFixture.paths.workplan)},
    {group: 'Walkthrough', icon: Layers, label: 'Open the walkthrough renewals', sub: 'Invented records, saved in this browser', onSelect: () => go('/renewals')},
  ];
}
