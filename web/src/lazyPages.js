import {lazyPage} from './app/lazyPage.js';

export {forgetFailedPages, lazyPage} from './app/lazyPage.js';

// The walkthrough's pages. Only the walkthrough imports this module, so its
// pages load after it and share its modules and styles.
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

export const Worklists = lazyPage(pageLoaders.Worklists);
export const Playbooks = lazyPage(pageLoaders.Playbooks);
export const Readiness = lazyPage(pageLoaders.Readiness);
export const WorkPlan = lazyPage(pageLoaders.WorkPlan);
export const Evidence = lazyPage(pageLoaders.Evidence);
export const History = lazyPage(pageLoaders.History);
export const FollowUpReport = lazyPage(pageLoaders.FollowUpReport);

export const WorkflowDialogContent = lazyPage(pageLoaders.WorkflowDialogContent);
