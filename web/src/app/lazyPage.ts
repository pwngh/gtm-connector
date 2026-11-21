import {type ComponentType, createElement, type LazyExoticComponent, lazy} from 'react';

// React.lazy keeps a failed import forever. A page made here starts a fresh
// import on its next render after forgetFailedPages(), so no reload is needed.
const failedPages = new Set<() => void>();

export function forgetFailedPages() {
  for (const reset of failedPages) {
    reset();
  }
  failedPages.clear();
}

type Module = {default: ComponentType};

export interface LazyPage {
  (props: object): ReturnType<typeof createElement>;
  preload: () => Promise<Module>;
}

export function lazyPage(load: () => Promise<Module>): LazyPage {
  let loaded: Module | null = null;
  let pending: Promise<Module> | null = null;
  // A module that is already here resolves in the same render, with no fallback:
  // React reads a thenable that calls back at once synchronously.
  // biome-ignore lint/suspicious/noThenProperty: the thenable is what lets React skip the fallback.
  const fresh = () => lazy(() => (loaded ? ({then: (resolve: (module: Module) => void) => resolve(loaded as Module)} as Promise<Module>) : preload()));
  let Lazy: LazyExoticComponent<ComponentType> = fresh();
  function preload() {
    pending ??= load().then(
      module => {
        loaded = module;
        return module;
      },
      error => {
        pending = null;
        failedPages.add(() => {
          Lazy = fresh();
        });
        throw error;
      },
    );
    return pending;
  }
  const Page = ((props: object) => createElement(Lazy, props)) as LazyPage;
  Page.preload = preload;
  return Page;
}
