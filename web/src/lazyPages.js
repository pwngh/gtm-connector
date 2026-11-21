import {lazyPage} from './app/lazyPage.js';

// The server's pages. Each loads on its own, after the app's shell.
export const pageLoaders = {
  ApiEvidence: () => import('./ApiEvidence.jsx'),
  ApiReadiness: () => import('./ApiReadiness.jsx'),
};

export const ApiEvidence = lazyPage(pageLoaders.ApiEvidence);
export const ApiReadiness = lazyPage(pageLoaders.ApiReadiness);
