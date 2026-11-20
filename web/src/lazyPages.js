import {lazy} from 'react';

export const pageLoaders = {
  WorkflowDialogContent: () => import('./WorkflowDialogContent.jsx'),
  CommandPalette: () => import('./CommandPalette.jsx'),
  Worklists: () => import('./Worklists.jsx'),
  Playbooks: () => import('./Playbooks.jsx'),
  Readiness: () => import('./Readiness.jsx'),
  WorkPlan: () => import('./WorkPlan.jsx'),
  Evidence: () => import('./Evidence.jsx'),
  History: () => import('./History.jsx'),
  FollowUpReport: () => import('./FollowUpReport.jsx'),
};

export const Worklists = lazy(pageLoaders.Worklists);
export const Playbooks = lazy(pageLoaders.Playbooks);
export const Readiness = lazy(pageLoaders.Readiness);
export const WorkPlan = lazy(pageLoaders.WorkPlan);
export const Evidence = lazy(pageLoaders.Evidence);
export const History = lazy(pageLoaders.History);
export const FollowUpReport = lazy(pageLoaders.FollowUpReport);

export const WorkflowDialogContent = lazy(pageLoaders.WorkflowDialogContent);
export const CommandPalette = lazy(pageLoaders.CommandPalette);
