import {lazyPage} from './app/lazyPage.ts';

// The server's pages. Each loads on its own, after the app's shell.
export const pageLoaders = {
  Deals: () => import('./app/deals/Deals.tsx'),
  ApiEvidence: () => import('./ApiEvidence.jsx'),
};

export const Deals = lazyPage(pageLoaders.Deals);
export const ApiEvidence = lazyPage(pageLoaders.ApiEvidence);
