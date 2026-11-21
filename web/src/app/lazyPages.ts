import {lazyPage} from './lazyPage.ts';

// The server's pages. Each loads on its own, after the app's shell.
export const pageLoaders = {
  Deals: () => import('./deals/Deals.tsx'),
  Coverage: () => import('./ops/Coverage.tsx'),
  Activity: () => import('./ops/Activity.tsx'),
};

export const Deals = lazyPage(pageLoaders.Deals);
export const Coverage = lazyPage(pageLoaders.Coverage);
export const Activity = lazyPage(pageLoaders.Activity);
