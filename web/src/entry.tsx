import {createRoot} from 'react-dom/client';
import {createApiClient} from './api/client.ts';
import {createServerStore} from './api/session.ts';
import {boot} from './app/boot.tsx';

const element = document.getElementById('root');
if (!element) {
  throw new Error('index.html has no #root');
}
const root = createRoot(element);
boot({store: createServerStore(createApiClient()), show: view => root.render(view)});
