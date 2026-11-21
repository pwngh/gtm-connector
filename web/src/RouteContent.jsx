import React, {Component, Suspense, createRef, useEffect, useLayoutEffect, useRef} from 'react';
import {focusHoldingSpot, focusIsUnplaced} from './focusElement.js';
import {forgetFailedPages} from './app/lazyPage.ts';

export function RouteLoading({label}) {
  const ref = useRef(null);
  // Hold the rest of the viewport for the page, so the footer does not jump when it arrives.
  useLayoutEffect(() => {
    const node = ref.current;
    if (!node || node.closest('[role="dialog"]')) return;
    node.style.setProperty('--route-top', Math.max(0, node.getBoundingClientRect().top + window.scrollY) + 'px');
  }, []);
  return <div ref={ref} className='route-loading' role='status' aria-live='polite' aria-atomic='true'>
    <p>Loading {label}…</p>
  </div>;
}

function Ready({path, onReady, children}) {
  useEffect(() => {onReady?.(path);}, [path, onReady]);
  return children;
}

export class RouteError extends Component {
  state = {failed: false, attempt: 0};
  panel = createRef();
  static getDerivedStateFromError() {return {failed: true};}
  componentDidCatch() {
    // The message is showing, so the next visit or Try again can import afresh.
    forgetFailedPages();
    // Focus the message if focus has nowhere useful to be.
    const panel = this.panel.current;
    if (panel && focusIsUnplaced(panel.ownerDocument, panel.closest('[role="dialog"]'))) panel.focus({preventScroll: true});
  }
  retry = () => {
    // The button is about to go. Keep focus on a spot that stays.
    focusHoldingSpot(document, this.panel.current?.closest('[role="dialog"]'));
    this.setState(state => ({failed: false, attempt: state.attempt + 1}));
  };
  render() {
    if (this.state.failed) return <section ref={this.panel} tabIndex={-1} className='route-loading route-error' aria-label='Page unavailable'>
      <p role='alert'>This page could not load. Nothing was lost.</p>
      <button type='button' className='button' onClick={this.retry}>Try again</button>
    </section>;
    // A new key remounts the content, so the import starts again.
    return <React.Fragment key={this.state.attempt}>{this.props.children}</React.Fragment>;
  }
}

export default function RouteContent({path, label, onReady, children}) {
  return <RouteError key={path}>
    <Suspense fallback={<RouteLoading label={label}/>}>
      <Ready path={path} onReady={onReady}>{children}</Ready>
    </Suspense>
  </RouteError>;
}
