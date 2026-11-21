import {Menu, Moon, Search} from 'lucide-react';
import {type RefObject, useCallback, useEffect, useLayoutEffect, useRef, useState} from 'react';
import {Link, useLocation, useNavigate} from 'react-router';
import {ApiError, sessionExpired} from '../../api/client.ts';
import {useRead, useServer} from '../../api/reads.ts';
import type {PrincipalView} from '../../api/types.ts';
import {Button} from '../../ui/Button.tsx';
import {Dialog} from '../../ui/Dialog.tsx';
import {Palette} from '../../ui/Palette.tsx';
import {Refusal, refusalText} from '../../ui/Refusal.tsx';
import {type Theme, useTheme} from '../../ui/theme.ts';
import {byName} from '../deals/deals.ts';
import {focusElement, holdPendingFocus} from './focus.ts';
import {NoPage} from './NoPage.tsx';
import {type Area, areaAt, areaHome, dealItems, keptQuery, pageAt, pageItems, shownAreas} from './pages.ts';
import RouteContent from './RouteContent.tsx';
import {SignIn} from './SignIn.tsx';
import './frame.css';

const phoneQuery = '(max-width: 719.98px)';

function Mark() {
  return (
    <span className="fr-mark">
      <svg
        className="fr-mark__symbol"
        width="28"
        height="28"
        viewBox="0 0 28 28"
        fill="none"
        aria-hidden="true"
      >
        <path
          d="M3 8 14 2l11 6-11 6Z"
          fill="currentColor"
        />
        <path
          d="m3 14 11 6 11-6M3 20l11 6 11-6"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
      <span className="fr-mark__name">gtm-connector</span>
    </span>
  );
}

interface ThemeProps {
  theme: Theme;
  choose: (theme: Theme) => void;
  labelled?: boolean;
}

function ThemeButton({theme, choose, labelled = false}: ThemeProps) {
  const dark = theme === 'dark';
  return (
    <Button
      variant="quiet"
      className={labelled ? 'fr-menu__item' : 'fr-icon-button'}
      aria-label={labelled ? undefined : 'Dark theme'}
      aria-pressed={dark}
      onClick={() => choose(dark ? 'light' : 'dark')}
    >
      <Moon
        size={20}
        fill={dark ? 'currentColor' : 'none'}
        aria-hidden="true"
      />
      {labelled && 'Dark theme'}
    </Button>
  );
}

interface LinksProps {
  search: string;
  current: Area | undefined;
  pathname: string;
  className: string;
  onNavigate?: () => void;
}

// An area with listed pages keeps the chosen account and open card; Deals starts afresh.
function AreaLinks({search, current, className, onNavigate}: Omit<LinksProps, 'pathname'>) {
  return shownAreas.map(area => (
    <Link
      key={area.id}
      to={areaHome(area) + (area.subnav ? keptQuery(search) : '')}
      className={className}
      aria-current={current?.id === area.id ? 'page' : undefined}
      onClick={onNavigate}
    >
      {area.label}
    </Link>
  ));
}

function PageLinks({search, current, pathname, className, onNavigate}: LinksProps) {
  return (current?.pages ?? []).map(page => (
    <Link
      key={page.id}
      to={page.path + keptQuery(search)}
      className={className}
      aria-current={page.path === pathname ? 'page' : undefined}
      onClick={onNavigate}
    >
      {page.label}
    </Link>
  ));
}

const asError = (error: unknown) => (error instanceof ApiError ? error : new ApiError());

// The palette: the granted deals by name, then the Ops pages. The deals are
// the account list the server answers; until it answers only the pages show,
// and a refusal is said, never shown as no deals.
function Finder({go, search}: {go: (to: string) => void; search: string}) {
  const accounts = useRead('accounts', (api, signal) => api.accounts({signal}));
  const granted = accounts.status === 'ready' && Array.isArray(accounts.data?.accounts) ? byName(accounts.data.accounts) : [];
  const notice =
    accounts.status === 'failed' ? `Deals not read: ${refusalText(accounts.error).title}` : accounts.status === 'loading' ? 'Reading your deals' : undefined;
  return (
    <Palette
      inputLabel="Deal or page name"
      placeholder="A deal’s name, or Activity"
      empty="No deal or page matches."
      notice={notice}
      items={[...dealItems(go, granted), ...pageItems(go, search)]}
    />
  );
}

// The frame of the app the server serves: signed out, the mark and the
// sign-in screen at whatever address was opened; signed in, the top bar with
// the areas, the palette, the theme and the signed-in name, the area's pages
// under it, and the page at the address.
export function Frame() {
  const location = useLocation();
  const navigate = useNavigate();
  const {store, state} = useServer();
  const [theme, chooseTheme] = useTheme();
  const expired = sessionExpired(state.session, Date.now());
  const principal: PrincipalView | null = state.session?.signed_in && !expired ? state.session.principal : null;
  const signedIn = principal !== null;
  const [palette, setPalette] = useState(false);
  const [menu, setMenu] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [signOutError, setSignOutError] = useState<ApiError | null>(null);
  const [loadedPath, setLoadedPath] = useState<string | null>(null);
  const menuButton = useRef<HTMLButtonElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const previousPath = useRef(location.pathname);
  const firstKey = useRef(location.key);
  const navigated = useRef(false);
  const wasSignedIn = useRef(signedIn);
  // The palette and the menu exist only while signed in; signing out takes them, and the inert page, away at once.
  const overlay = signedIn && (palette || menu);
  const area = areaAt(location.pathname);
  const page = pageAt(location.pathname);

  useEffect(() => {
    if (expired) {
      store.check();
    }
  }, [expired, store]);

  const openPalette = useCallback(() => {
    opener.current = menu ? menuButton.current : document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setMenu(false);
    setPalette(true);
  }, [menu]);

  useEffect(() => {
    if (!signedIn) {
      return undefined;
    }
    const handle = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        openPalette();
      }
    };
    window.addEventListener('keydown', handle);
    return () => window.removeEventListener('keydown', handle);
  }, [signedIn, openPalette]);

  // The phone menu belongs to the phone layout: a wider window closes it.
  useEffect(() => {
    const media = window.matchMedia(phoneQuery);
    const close = () => {
      if (!media.matches) {
        setMenu(false);
      }
    };
    media.addEventListener('change', close);
    return () => media.removeEventListener('change', close);
  }, []);

  // Signing out or ending a session closes what was open over the page.
  useEffect(() => {
    if (!signedIn) {
      setPalette(false);
      setMenu(false);
      setSigningOut(false);
    }
  }, [signedIn]);

  // When the session starts, focus goes to the page heading; when it ends,
  // to the sign-in heading, unless focus is somewhere that still stands.
  useEffect(() => {
    if (wasSignedIn.current === signedIn) {
      return;
    }
    wasSignedIn.current = signedIn;
    const active = document.activeElement;
    if (signedIn) {
      setSignOutError(null);
      focusHeading();
      return;
    }
    if (!active || active === document.body || !active.isConnected) {
      focusElement(document.getElementById('fr-signin-title'));
    }
  }, [signedIn]);

  // A new page starts at the top, with focus on its heading.
  useEffect(() => {
    if (previousPath.current === location.pathname) {
      return undefined;
    }
    previousPath.current = location.pathname;
    window.scrollTo({top: 0, behavior: 'instant'});
    const frame = requestAnimationFrame(focusHeading);
    return () => cancelAnimationFrame(frame);
  }, [location.pathname]);

  // While a page loads, focus lost from a removed element waits on the page heading.
  useLayoutEffect(() => {
    if (location.key !== firstKey.current) {
      navigated.current = true;
    }
    if (navigated.current && loadedPath !== location.pathname && !overlay) {
      holdPendingFocus(document);
    }
  });

  const pageReady = useCallback((path: string) => {
    setLoadedPath(path);
    // Focus that waited on the main region moves to the heading that has now arrived.
    if (document.activeElement === document.getElementById('main')) {
      focusHeading();
    }
  }, []);

  async function signOut() {
    if (signingOut) {
      return;
    }
    setSigningOut(true);
    setSignOutError(null);
    try {
      await store.signOut();
    } catch (failure) {
      setSignOutError(asError(failure));
      setSigningOut(false);
    }
  }

  const announce = signedIn ? `Signed in as ${principal.display_name}.` : state.ended || expired ? 'Your session ended. You are signed out.' : '';
  const noSession = state.mode === 'unreachable' || (!state.session && state.error !== null);
  const closeMenu = () => setMenu(false);
  const {Page, label, key} = page ? {Page: page.Page, label: page.label, key: page.path} : {Page: NoPage, label: 'page', key: location.pathname};

  return (
    <div className="fr">
      {/* biome-ignore lint/a11y/useValidAnchor: a skip link moves focus without adding #main to the address. */}
      <a
        className="fr-skip"
        href="#main"
        inert={overlay}
        onClick={event => {
          event.preventDefault();
          focusElement(document.getElementById('main'), {block: 'start'});
        }}
      >
        Skip to content
      </a>
      <p
        className="fr-announce"
        role="status"
      >
        {announce}
      </p>
      <div
        className="fr-chrome"
        inert={overlay}
      >
        <header className="fr-bar">
          <Mark />
          {signedIn && (
            <>
              <nav
                className="fr-areas"
                aria-label="Areas"
              >
                <AreaLinks
                  search={location.search}
                  current={area}
                  className="fr-link"
                />
              </nav>
              {area && <span className="fr-area-name">{area.label}</span>}
              {principal.demo && <span className="fr-demo fr-demo--bar">Demo, invented records</span>}
              <div className="fr-tools">
                <Button
                  variant="quiet"
                  className="fr-find"
                  aria-haspopup="dialog"
                  aria-keyshortcuts="Control+K Meta+K"
                  onClick={openPalette}
                >
                  <Search
                    size={20}
                    aria-hidden="true"
                  />
                  Find a deal
                </Button>
                <ThemeButton
                  theme={theme}
                  choose={chooseTheme}
                />
                <span className="fr-who">{principal.display_name}</span>
                <Button
                  variant="quiet"
                  pending={signingOut}
                  onClick={signOut}
                >
                  Sign out
                </Button>
              </div>
              <button
                ref={menuButton}
                type="button"
                className="fr-menu-button"
                aria-label="Menu"
                aria-haspopup="dialog"
                aria-expanded={menu}
                onClick={() => setMenu(true)}
              >
                <Menu
                  size={22}
                  aria-hidden="true"
                />
              </button>
            </>
          )}
        </header>
        {principal?.demo && (
          <p className="fr-demo-row">
            <span className="fr-demo">Demo, invented records</span>
          </p>
        )}
        {signedIn && area?.subnav && area.pages.length > 0 && (
          <nav
            className="fr-pages"
            aria-label={`${area.label} pages`}
          >
            <PageLinks
              search={location.search}
              current={area}
              pathname={location.pathname}
              className="fr-link"
            />
          </nav>
        )}
      </div>
      <main
        id="main"
        className="fr-main"
        tabIndex={-1}
        inert={overlay}
      >
        {!signedIn ? (
          <SignIn
            store={store}
            ended={state.ended || expired}
            elsewhere={state.elsewhere}
            problem={noSession ? (state.error ?? new ApiError({kind: 'unreachable'})) : null}
          />
        ) : (
          <div className="fr-page">
            {(signOutError ?? state.error) && (
              <Refusal
                error={signOutError ?? state.error}
                retry={signOutError ? signOut : () => store.refresh()}
              />
            )}
            <RouteContent
              path={location.pathname}
              pageKey={key}
              label={label}
              onReady={pageReady}
            >
              <Page />
            </RouteContent>
          </div>
        )}
      </main>
      {overlay && palette && (
        <Dialog
          title="Find a deal"
          returnFocus={opener}
          focusInput
          onClose={() => setPalette(false)}
        >
          <Finder
            go={to => {
              setPalette(false);
              navigate(to);
            }}
            search={location.search}
          />
        </Dialog>
      )}
      {menu && principal && (
        <PhoneMenu
          returnFocus={menuButton}
          principal={principal}
          area={area}
          search={location.search}
          pathname={location.pathname}
          theme={theme}
          choose={chooseTheme}
          signingOut={signingOut}
          onSignOut={signOut}
          onClose={closeMenu}
        />
      )}
    </div>
  );
}

interface PhoneMenuProps {
  returnFocus: RefObject<HTMLButtonElement | null>;
  principal: PrincipalView;
  area: Area | undefined;
  search: string;
  pathname: string;
  theme: Theme;
  choose: (theme: Theme) => void;
  signingOut: boolean;
  onSignOut: () => void;
  onClose: () => void;
}

// The menu under 720 wide: a full-screen dialog with the areas, the Ops
// pages, the theme and sign out.
function PhoneMenu({returnFocus, principal, area, search, pathname, theme, choose, signingOut, onSignOut, onClose}: PhoneMenuProps) {
  const ops = shownAreas.find(item => item.id === 'ops');
  return (
    <Dialog
      title="Menu"
      variant="sheet"
      returnFocus={returnFocus}
      onClose={onClose}
    >
      <div className="fr-menu">
        <nav
          className="fr-menu__group"
          aria-label="Areas"
        >
          <AreaLinks
            search={search}
            current={area}
            className="fr-menu__item"
            onNavigate={onClose}
          />
        </nav>
        {ops && (
          <nav
            className="fr-menu__group"
            aria-label="Ops pages"
          >
            <p className="fr-menu__label">Ops pages</p>
            <PageLinks
              search={search}
              current={ops}
              pathname={pathname}
              className="fr-menu__item"
              onNavigate={onClose}
            />
          </nav>
        )}
        <div className="fr-menu__group">
          <ThemeButton
            theme={theme}
            choose={choose}
            labelled
          />
        </div>
        <div className="fr-menu__group">
          <p className="fr-menu__label">Signed in as</p>
          <p className="fr-menu__who">{principal.display_name}</p>
          <Button
            variant="quiet"
            className="fr-menu__item"
            pending={signingOut}
            onClick={onSignOut}
          >
            Sign out
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

function focusHeading() {
  const heading = document.querySelector<HTMLElement>('main h1');
  if (heading) {
    heading.tabIndex = -1;
    heading.focus({preventScroll: true});
    return;
  }
  document.getElementById('main')?.focus({preventScroll: true});
}
