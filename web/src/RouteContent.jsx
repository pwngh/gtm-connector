import React, {Component, Suspense, useEffect} from 'react';

export function RouteLoading({label}) {
  return <div className='route-loading' role='status' aria-live='polite' aria-atomic='true'>
    <p>Loading {label}…</p>
  </div>;
}

function Ready({path, onReady, children}) {
  useEffect(() => {onReady?.(path);}, [path, onReady]);
  return children;
}

class RouteError extends Component {
  state = {failed: false};
  static getDerivedStateFromError() {return {failed: true};}
  render() {
    if (this.state.failed) return <section className='route-loading' aria-label='Page unavailable'>
      <p role='alert'>This page could not load. Reload to try again. Your saved walkthrough has not been reset.</p>
      <button type='button' className='button' onClick={() => window.location.reload()}>Reload page</button>
    </section>;
    return this.props.children;
  }
}

export default function RouteContent({path, label, onReady, children}) {
  return <RouteError key={path}>
    <Suspense fallback={<RouteLoading label={label}/>}>
      <Ready path={path} onReady={onReady}>{children}</Ready>
    </Suspense>
  </RouteError>;
}
