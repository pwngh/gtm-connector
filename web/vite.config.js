import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';

// The revision the guide links open at: GTM_REVISION from the build's
// environment (make sets it to the commit), without the mark make adds for a
// changed tree, or the default branch when it is unset or not a revision name.
const named = (process.env.GTM_REVISION ?? '').replace(/-modified$/, '');
const revision = /^[0-9A-Za-z][0-9A-Za-z._-]{0,99}$/.test(named) ? named : 'main';

export default defineConfig({
  plugins: [react()],
  define: {__GTM_REVISION__: JSON.stringify(revision)},
  build: {outDir: 'dist'},
  server: {port: 4176, strictPort: true},
});
