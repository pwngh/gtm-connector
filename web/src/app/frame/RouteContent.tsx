import {Component, createRef, Fragment, type ReactNode, Suspense, useEffect, useLayoutEffect, useRef} from 'react';
import {Button} from '../../ui/Button.tsx';
import {forgetFailedPages} from '../lazyPage.ts';
import {focusHoldingSpot, focusIsUnplaced} from './focus.ts';

/** While a page loads: one live line, holding the rest of the viewport so the page does not jump when it arrives. */
export function RouteLoading({label}: {label: string}) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const node = ref.current;
    if (!node || node.closest('[role="dialog"]')) {
      return;
    }
    node.style.setProperty('--route-top', `${Math.max(0, node.getBoundingClientRect().top + window.scrollY)}px`);
  }, []);
  return (
    <div
      ref={ref}
      className="fr-loading"
      role="status"
      aria-live="polite"
      aria-atomic="true"
    >
      <p>Loading {label}…</p>
    </div>
  );
}

function Ready({path, onReady, children}: {path: string; onReady: ((path: string) => void) | undefined; children: ReactNode}) {
  useEffect(() => {
    onReady?.(path);
  }, [path, onReady]);
  return children;
}

interface ErrorState {
  failed: boolean;
  attempt: number;
}

/** A page that could not load says so and offers to try again; nothing else on the page is lost. */
export class RouteError extends Component<{children: ReactNode}, ErrorState> {
  override state: ErrorState = {failed: false, attempt: 0};
  panel = createRef<HTMLElement>();
  static getDerivedStateFromError(): Partial<ErrorState> {
    return {failed: true};
  }
  override componentDidCatch() {
    // The message is showing, so the next visit or Try again can import afresh.
    forgetFailedPages();
    // Focus the message if focus has nowhere useful to be.
    const panel = this.panel.current;
    if (panel && focusIsUnplaced(panel.ownerDocument, panel.closest('[role="dialog"]'))) {
      panel.focus({preventScroll: true});
    }
  }
  retry = () => {
    // The button is about to go. Keep focus on a spot that stays.
    focusHoldingSpot(document, this.panel.current?.closest('[role="dialog"]') ?? null);
    this.setState(state => ({failed: false, attempt: state.attempt + 1}));
  };
  override render() {
    if (this.state.failed) {
      return (
        <section
          ref={this.panel}
          tabIndex={-1}
          className="fr-loading fr-loading--failed"
          aria-label="Page unavailable"
        >
          <p role="alert">This page could not load. Nothing was lost.</p>
          <Button onClick={this.retry}>Try again</Button>
        </section>
      );
    }
    // A new key remounts the content, so the import starts again.
    return <Fragment key={this.state.attempt}>{this.props.children}</Fragment>;
  }
}

interface RouteContentProps {
  path: string;
  /** A page stays mounted while its address changes below it, as the deal list does when a deal opens; a new page starts afresh. */
  pageKey?: string;
  label: string;
  onReady?: (path: string) => void;
  children: ReactNode;
}

/** A page of the app: loaded on its own, with a loading line while it comes and a message if it cannot. */
export default function RouteContent({path, pageKey = path, label, onReady, children}: RouteContentProps) {
  return (
    <RouteError key={pageKey}>
      <Suspense fallback={<RouteLoading label={label} />}>
        <Ready
          path={path}
          onReady={onReady}
        >
          {children}
        </Ready>
      </Suspense>
    </RouteError>
  );
}
