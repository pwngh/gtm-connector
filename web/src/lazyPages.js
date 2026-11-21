import {lazyPage} from './app/lazyPage.ts';

// The server's pages. Each loads on its own, after the app's shell.
export const pageLoaders = {
  Deals: () => import('./app/deals/Deals.tsx'),
  Coverage: () => import('./app/ops/Coverage.tsx'),
  ApiEvidence: () => import('./ApiEvidence.jsx'),
  ApiReadiness: () => import('./ApiReadiness.jsx'),
};

export const Deals = lazyPage(pageLoaders.Deals);
export const Coverage = lazyPage(pageLoaders.Coverage);
export const ApiEvidence = lazyPage(pageLoaders.ApiEvidence);
export const ApiReadiness = lazyPage(pageLoaders.ApiReadiness);
