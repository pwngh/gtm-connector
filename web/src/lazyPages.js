import {createElement, lazy} from 'react';

export const pageLoaders = {
  WorkflowDialogContent: () => import('./WorkflowDialogContent.jsx'),
  Worklists: () => import('./Worklists.jsx'),
  Playbooks: () => import('./Playbooks.jsx'),
  Readiness: () => import('./Readiness.jsx'),
  WorkPlan: () => import('./WorkPlan.jsx'),
  Evidence: () => import('./Evidence.jsx'),
  History: () => import('./History.jsx'),
  FollowUpReport: () => import('./FollowUpReport.jsx'),
};

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

export const Worklists = lazyPage(pageLoaders.Worklists);
export const Playbooks = lazyPage(pageLoaders.Playbooks);
export const Readiness = lazyPage(pageLoaders.Readiness);
export const WorkPlan = lazyPage(pageLoaders.WorkPlan);
export const Evidence = lazyPage(pageLoaders.Evidence);
export const History = lazyPage(pageLoaders.History);
export const FollowUpReport = lazyPage(pageLoaders.FollowUpReport);

export const WorkflowDialogContent = lazyPage(pageLoaders.WorkflowDialogContent);
