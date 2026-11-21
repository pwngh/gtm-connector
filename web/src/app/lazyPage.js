import {createElement, lazy} from 'react';

// React.lazy keeps a failed import forever. A page made here starts a fresh
// import on its next render after forgetFailedPages(), so no reload is needed.
const failedPages = new Set();

export function forgetFailedPages() {
  for (const reset of failedPages) reset();
  failedPages.clear();
}

export function lazyPage(load) {
  let loaded = null, pending = null;
  // A module that is already here resolves in the same render, with no fallback.
  const fresh = () => lazy(() => loaded ? {then: resolve => resolve(loaded)} : preload());
  let Lazy = fresh();
  function preload() {
    pending ??= load().then(module => {loaded = module; return module;}, error => {
      pending = null;
      failedPages.add(() => {Lazy = fresh();});
      throw error;
    });
    return pending;
  }
  function Page(props) {return createElement(Lazy, props);}
  Page.preload = preload;
  return Page;
}
