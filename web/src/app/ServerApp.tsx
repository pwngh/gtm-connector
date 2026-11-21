import {BrowserRouter, Navigate, Route, Routes} from 'react-router';
import {ServerContext} from '../api/reads.ts';
import type {ServerStore} from '../api/session.ts';
// The styles the two existing pages use. Later files override earlier ones,
// so the order matters, and the app's own tokens and components come after.
import '../readiness.css';
import '../styles.css';
import '../refinements.css';
import '../modern.css';
import '../atelier.css';
import '../workplan.css';
import '../chrome.css';
import '../api.css';
import '../ui/tokens.css';
import {Frame} from './frame/Frame.tsx';
import {home} from './frame/pages.ts';

// The app the server serves. "/" and /ops open the first page.
export default function ServerApp({store}: {store: ServerStore}) {
  return (
    <ServerContext value={store}>
      <BrowserRouter>
        <Routes>
          <Route
            path="/"
            element={
              <Navigate
                to={home}
                replace
              />
            }
          />
          <Route
            path="/ops"
            element={
              <Navigate
                to={home}
                replace
              />
            }
          />
          <Route
            path="*"
            element={<Frame />}
          />
        </Routes>
      </BrowserRouter>
    </ServerContext>
  );
}
