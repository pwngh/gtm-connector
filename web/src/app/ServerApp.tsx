import {BrowserRouter, Navigate, Route, Routes} from 'react-router';
import {ServerContext} from '../api/reads.ts';
import type {ServerStore} from '../api/session.ts';
// The styles of every page, in one sheet the app starts with: the base
// rules and tokens first, then the components and pages.
import '../ui/base.css';
import '../ui/tokens.css';
import './deals/deals.css';
import './deals/card/card.css';
import './deals/card/proof.css';
import './deals/card/text.css';
import './deals/card/communications.css';
import './deals/card/lines.css';
import '../ui/help.css';
import './ops/coverage.css';
import './ops/activity.css';
import {Frame} from './frame/Frame.tsx';
import {home, opsHome} from './frame/pages.ts';

// The app the server serves. "/" opens Deals, and /ops its first page.
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
                to={opsHome}
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
