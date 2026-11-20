import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    rolldownOptions: {output: {codeSplitting: {groups: [{
      name: 'animation',
      test: id => ['motion', 'motion-dom', 'motion-utils', 'framer-motion'].some(name => id.includes('/node_modules/' + name + '/')),
    }]}}},
  },
  server: {port: 4176, strictPort: true},
});
