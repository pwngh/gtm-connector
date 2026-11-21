import {createRoot} from 'react-dom/client';
import {boot} from './app/boot.jsx';
import {createServerStore} from './serverSession.js';
import {createApiClient} from './api.js';

const root = createRoot(document.getElementById('root'));
boot({store: createServerStore(createApiClient()), show: view => root.render(view)});
