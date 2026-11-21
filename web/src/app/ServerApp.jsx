import React, {useEffect, useLayoutEffect, useRef, useState} from 'react';
import {BrowserRouter, Navigate, NavLink, Route, Routes, useLocation, useNavigate} from 'react-router';
import {Menu, Moon, Search, X} from 'lucide-react';
import Dialog from '../WorkspaceDialog.jsx';
import CommandPalette from '../CommandPalette.jsx';
import RouteContent from '../RouteContent.jsx';
import {focusElement, holdPendingFocus} from '../focusElement.js';
import {ApiCrumbs, NoPage, PrincipalMark, ServerContext, ServerNav, apiCommandItems, serverHome, serverPageAt} from '../ServerSwitch.jsx';
// The styles the server pages use. Later files override earlier ones, so the order matters.
import '../readiness.css';
import '../styles.css';
import '../refinements.css';
import '../modern.css';
import '../atelier.css';
import '../workplan.css';
import '../chrome.css';
import '../api.css';

const compactQuery = '(max-width:650px)';
const navigationLinks = '#workspace-navigation button:not(:disabled),#workspace-navigation a[href]';

function storedTheme() {
  try {return localStorage.getItem('gtm-workflow-theme') === 'light' ? 'light' : 'dark';} catch {return 'dark';}
}

// The shell of the app the server serves: the brand, the two server pages,
// the palette, the theme, the signed-in mark, and the page at the current path.
export function ServerShell() {
  const location = useLocation();
  const [theme, setTheme] = useState(storedTheme);
  const [compact, setCompact] = useState(() => typeof window !== 'undefined' && Boolean(window.matchMedia?.(compactQuery).matches));
  const [mobile, setMobile] = useState(false), [palette, setPalette] = useState(false);
  const [loadedPath, setLoadedPath] = useState(null);
  const menuRef = useRef(null), opener = useRef(null), previousPath = useRef(location.pathname), firstKey = useRef(location.key), navigated = useRef(false);
  const overlayOpen = palette || (compact && mobile);

  useEffect(() => {document.title = 'gtm-connector';}, []);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#0a0f15' : '#f3f5f8');
    try {localStorage.setItem('gtm-workflow-theme', theme);} catch {}
  }, [theme]);
  useEffect(() => {
    const media = window.matchMedia(compactQuery);
    const update = () => {setCompact(media.matches); setMobile(false);};
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, []);

  function openPalette() {
    if (!palette) opener.current = compact && mobile ? menuRef.current : document.activeElement;
    setMobile(false);
    setPalette(true);
  }
  useEffect(() => {
    const handle = event => {if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {event.preventDefault(); openPalette();}};
    window.addEventListener('keydown', handle);
    return () => window.removeEventListener('keydown', handle);
  }, [palette, compact, mobile]);

  // The open navigation on a phone is a dialog: Escape closes it, and Tab stays in it.
  useEffect(() => {
    if (!compact || !mobile || palette) return undefined;
    const handle = event => {
      if (event.key === 'Escape') {event.preventDefault(); setMobile(false); menuRef.current?.focus({preventScroll: true}); return;}
      if (event.key !== 'Tab') return;
      const nodes = [...document.querySelectorAll(navigationLinks)].filter(node => node.tabIndex >= 0 && node.getClientRects().length > 0);
      const first = nodes[0], last = nodes.at(-1);
      if (!nodes.includes(document.activeElement)) {event.preventDefault(); (event.shiftKey ? last : first)?.focus();}
      else if (event.shiftKey && document.activeElement === first) {event.preventDefault(); last?.focus();}
      else if (!event.shiftKey && document.activeElement === last) {event.preventDefault(); first?.focus();}
    };
    document.addEventListener('keydown', handle);
    return () => document.removeEventListener('keydown', handle);
  }, [compact, mobile, palette]);
  useEffect(() => {
    if (compact && mobile && !palette) document.querySelector('.mobile-nav-close')?.focus();
  }, [compact, mobile, palette]);
  useEffect(() => {
    if (!overlayOpen) return undefined;
    const previous = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {document.body.style.overflow = previous;};
  }, [overlayOpen]);

  // A new page starts at the top, with focus on its heading.
  useEffect(() => {
    setMobile(false);
    if (previousPath.current === location.pathname) return undefined;
    previousPath.current = location.pathname;
    window.scrollTo({top: 0, behavior: 'instant'});
    const frame = requestAnimationFrame(() => {
      const heading = document.querySelector('main h1');
      if (heading) {heading.tabIndex = -1; heading.focus({preventScroll: true});}
    });
    return () => cancelAnimationFrame(frame);
  }, [location.pathname]);
  // While a page loads, focus lost from a removed element waits on the page heading.
  useLayoutEffect(() => {
    if (location.key !== firstKey.current) navigated.current = true;
    if (navigated.current && loadedPath !== location.pathname && !overlayOpen) holdPendingFocus(document);
  });

  const closeNavigation = () => {setMobile(false); menuRef.current?.focus();};
  const {Page, label} = serverPageAt(location.pathname) || {Page: NoPage, label: 'page'};
  return <div className="app">
    <a className="skip" inert={overlayOpen} href="#main" onClick={event => {event.preventDefault(); focusElement(document.getElementById('main'), {block: 'start'});}}>Skip to content</a>
    <aside id="workspace-navigation" role={compact && mobile ? 'dialog' : undefined} aria-modal={compact && mobile ? true : undefined} aria-label={compact && mobile ? 'Workspace navigation' : undefined}
      inert={palette || (compact && !mobile)} aria-hidden={palette || (compact && !mobile)} className={`sidebar ${mobile ? 'is-open' : ''}`}>
      <button className="icon-button mobile-nav-close" aria-label="Close navigation" onClick={closeNavigation}><X size={22}/></button>
      <NavLink to={serverHome} className="brand" onClick={() => setMobile(false)}><span className="brand-symbol"><svg width="28" height="28" viewBox="0 0 28 28" fill="none" aria-hidden="true"><path d="M3 8 14 2l11 6-11 6Z" fill="currentColor" opacity=".9"/><path d="m3 14 11 6 11-6M3 20l11 6 11-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"/></svg></span><span>gtm<span className="brand-dash">-</span>connector</span></NavLink>
      <div className="nav-label">READ FROM THE SERVER</div>
      <nav aria-label="Workspace"><ServerNav onNavigate={() => setMobile(false)}/></nav>
    </aside>
    {compact && mobile && <button className="navigation-backdrop" tabIndex={-1} aria-label="Close navigation backdrop" onClick={closeNavigation}/>}
    <div className="main-shell" inert={overlayOpen}>
      <header className="topbar">
        <div className="breadcrumbs"><button ref={menuRef} className="icon-button mobile-menu" onClick={() => setMobile(!mobile)} aria-expanded={mobile} aria-controls="workspace-navigation" aria-label="Toggle navigation"><Menu size={21}/></button><ApiCrumbs/></div>
        <div className="workspace-toolbar">
          <button aria-label="Go to a page" aria-haspopup="dialog" aria-expanded={palette} aria-keyshortcuts="Control+K Meta+K" className="search-trigger" onClick={openPalette}><Search size={17}/><span>Go to a page…</span><kbd className="keycap">⌘ K</kbd></button>
          <button className="icon-button theme-toggle" aria-label="Dark theme" aria-pressed={theme === 'dark'} title="Dark theme" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}><Moon size={19} fill={theme === 'dark' ? 'currentColor' : 'none'}/></button>
          <PrincipalMark/>
        </div>
      </header>
      <main id="main" className="main-content" tabIndex={-1}>
        <RouteContent path={location.pathname} label={label} onReady={setLoadedPath}><Page/></RouteContent>
        <footer className="page-footer"><span>gtm-connector</span><span>Read from the server</span></footer>
      </main>
    </div>
    {palette && <ServerPalette opener={opener} onClose={() => setPalette(false)}/>}
  </div>;
}

// The palette: the two server pages, with the chosen account and open card kept.
export function ServerPalette({opener, onClose}) {
  const {search} = useLocation(), navigate = useNavigate();
  return <Dialog command wide title="Go to" returnFocusRef={opener} onClose={onClose}>
    <CommandPalette placeholder="Find a page" inputLabel="Find a page" empty={<h3>No page matches.</h3>}
      items={apiCommandItems(to => {onClose(); navigate(to);}, search)}/>
  </Dialog>;
}

// The app the server serves. "/" and /ops open the first server page.
export default function ServerApp({store}) {
  return <ServerContext value={store}><BrowserRouter><Routes>
    <Route path="/" element={<Navigate to={serverHome} replace/>}/>
    <Route path="/ops" element={<Navigate to={serverHome} replace/>}/>
    <Route path="*" element={<ServerShell/>}/>
  </Routes></BrowserRouter></ServerContext>;
}
