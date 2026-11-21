import React, {createContext, useContext, useSyncExternalStore} from 'react';
import {Link, NavLink, useLocation} from 'react-router';
import {ChevronRight, FileText, GitBranch, UserRound} from 'lucide-react';
import {serverPaths} from './app/routes.js';
import {lazyPage} from './app/lazyPage.js';
import {Avatar} from './WorkspaceControls.jsx';

// The server's store: the signed-in session and how the server answered.
// Without a provider, as when one view renders alone, nothing is signed in
// and nothing is read.
const detachedState = Object.freeze({mode: 'detached', session: null, error: null, ended: false, generation: 0, elsewhere: null});
export const ServerContext = createContext({subscribe: () => () => {}, getSnapshot: () => detachedState});

export function useServer() {
  const store = useContext(ServerContext);
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
  return {store, state};
}

const ApiEvidence = lazyPage(() => import('./ApiEvidence.jsx'));
const ApiReadiness = lazyPage(() => import('./ApiReadiness.jsx'));

// The pages that read from the server, in the order the navigation lists
// them. The first is home. Every other path is not available here.
export const serverPages = [
  {id: 'evidence', label: 'Evidence', path: serverPaths.evidence, icon: FileText, sub: 'The chosen account’s cards, units, receipts and gaps', Page: ApiEvidence},
  {id: 'readiness', label: 'Readiness', path: serverPaths.readiness, icon: GitBranch, sub: 'The open card’s gaps; the server has no workflow rules yet', Page: ApiReadiness},
];
export const serverHome = serverPages[0].path;
export const serverPageAt = pathname => serverPages.find(page => page.path === pathname);

// Each server page with the chosen account and open card kept in its link.
function serverLinks(search) {
  const params = new URLSearchParams(search), keep = new URLSearchParams();
  for (const key of ['account', 'card']) if (params.get(key)) keep.set(key, params.get(key));
  const query = String(keep) ? `?${keep}` : '';
  return serverPages.map(page => ({...page, to: page.path + query}));
}

// The navigation: the server pages only.
export function ServerNav({onNavigate}) {
  const {search} = useLocation();
  return serverLinks(search).map(({id, label, icon: Icon, to}) => <NavLink key={id} to={to} onClick={onNavigate} className={({isActive}) => `nav-item ${isActive ? 'active' : ''}`}><Icon size={19}/><span>{label}</span></NavLink>);
}

// Any path that is not a server page.
export function NotAvailable() {
  const {state} = useServer();
  return <div className="page-heading secondary-heading api-unavailable"><div><div className="eyebrow">READ FROM THE SERVER</div>
    <h1>Not available here<span className="company-dot">.</span></h1>
    <p className="page-description">{state.mode === 'unreachable' ? 'The server did not answer, so nothing is shown. Reload the page to ask it again.'
      : 'The server does not provide this page, so nothing is shown. It provides Evidence and Readiness.'}</p>
    <Link className="button primary" to={serverHome}>Go to Evidence</Link></div></div>;
}

// The breadcrumb. It names no account: the link's text is not trusted, and
// the page heading shows the verified one.
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

// The palette: the server pages, with the chosen account and open card kept.
export function apiCommandItems(go, search) {
  return serverLinks(search).map(({label, icon, sub, to}) => ({group: 'Read from the server', icon, label, sub, onSelect: () => go(to)}));
}
