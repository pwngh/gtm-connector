import React, {createContext, use, useContext, useSyncExternalStore} from 'react';
import {Link, NavLink, Navigate, useLocation} from 'react-router';
import {ChevronRight, FileText, GitBranch, UserRound} from 'lucide-react';
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

// The pages that read from the server, in the order the navigation lists
// them. The first is home. Every other path belongs to the walkthrough.
export const serverPages = [
  {id: 'evidence', label: 'Evidence', path: renewalFixture.paths.evidence, icon: FileText, sub: 'The chosen account’s cards, units, receipts and gaps', Page: ApiEvidence},
  {id: 'readiness', label: 'Readiness', path: `${renewalFixture.paths.root}/readiness`, icon: GitBranch, sub: 'The open card’s gaps; the server has no workflow rules yet', Page: ApiReadiness},
];
export const serverHome = serverPages[0].path;
const serverPageAt = pathname => serverPages.find(page => page.path === pathname);

// Each server page with the chosen account and open card kept in its link.
function serverLinks(search) {
  const params = new URLSearchParams(search), keep = new URLSearchParams();
  for (const key of ['account', 'card']) if (params.get(key)) keep.set(key, params.get(key));
  const query = String(keep) ? `?${keep}` : '';
  return serverPages.map(page => ({...page, to: page.path + query}));
}

// The page at the current path. The walkthrough shows its own page. When the
// server serves the app, a server page reads from it and every other path
// says it is not available here. While the server is being asked, the page
// waits under the route's loading state; it never shows walkthrough data first.
export default function ServerSwitch({children}) {
  const {store, state} = useServer();
  const {pathname} = useLocation();
  if (state.mode === 'probing') use(store.start());
  if (store.getSnapshot().mode === 'walkthrough') return children;
  const {Page} = serverPageAt(pathname) || {Page: NotAvailable};
  return <Page/>;
}

// Shell content that comes from walkthrough data. It shows only in walkthrough
// mode. When the server serves the app, `api` shows instead. Nothing shows
// while the server is being asked, so walkthrough data never flashes first.
export function ByMode({api = null, children = null}) {
  const {state} = useServer();
  if (state.mode === 'walkthrough') return children;
  if (state.mode === 'probing') return null;
  return api;
}

// "/" opens the first page of whichever app this is, once the server has been asked.
export function Home({walkthrough}) {
  const {store, state} = useServer();
  if (state.mode === 'probing') use(store.start());
  return <Navigate to={store.getSnapshot().mode === 'walkthrough' ? walkthrough : serverHome} replace/>;
}

// A link to the walkthrough page `to`, or to home when the server serves the app.
export function HomeLink({to, ...props}) {
  const {state} = useServer();
  return <NavLink to={{walkthrough: to, probing: '/'}[state.mode] || serverHome} {...props}/>;
}

// The navigation when the server serves the app: the server pages only.
export function ServerNav({onNavigate}) {
  const {search} = useLocation();
  return serverLinks(search).map(({id, label, icon: Icon, to}) => <NavLink key={id} to={to} onClick={onNavigate} className={({isActive}) => `nav-item ${isActive ? 'active' : ''}`}><Icon size={19}/><span>{label}</span></NavLink>);
}

// A walkthrough path, or any unknown one, when the server serves the app or
// did not say which app this is. It shows nothing of the walkthrough.
export function NotAvailable() {
  const {state} = useServer();
  return <div className="page-heading secondary-heading api-unavailable"><div><div className="eyebrow">READ FROM THE SERVER</div>
    <h1>Not available here<span className="company-dot">.</span></h1>
    <p className="page-description">{state.mode === 'unreachable' ? 'The server did not answer, so nothing is shown. Reload the page to ask it again.'
      : 'The server does not provide this page, so nothing is shown. It provides Evidence and Readiness.'}</p>
    <Link className="button primary" to={serverHome}>Go to Evidence</Link></div></div>;
}

// The breadcrumb when the server serves the app. It names no account: the
// link's text is not trusted, and the page heading shows the verified one.
export function ApiCrumbs() {
  const {pathname} = useLocation();
  if (!serverPageAt(pathname)) return <span className="api-crumb-alone">Not available here</span>;
  return <><span>Renewal</span><ChevronRight size={14}/><span>Read from the server</span></>;
}

// The signed-in principal's mark; signed out, a plain figure.
export function PrincipalMark() {
  const {state} = useServer();
  const name = state.session?.signed_in ? state.session.principal?.display_name : null;
  return <span className="profile" role="img" aria-label={name ? `Signed in as ${name}` : 'Not signed in'}>
    {name ? <Avatar name={name}/> : <span className="avatar" aria-hidden="true"><UserRound size={16}/></span>}</span>;
}

// The palette when the server serves the app: the server pages, with the
// chosen account and open card kept. No walkthrough record, page or action.
export function apiCommandItems(go, search) {
  return serverLinks(search).map(({label, icon, sub, to}) => ({group: 'Read from the server', icon, label, sub, onSelect: () => go(to)}));
}
