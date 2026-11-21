import type {ReactNode} from 'react';
import type {ServerStore} from '../api/session.ts';
import {Placeholder} from '../ui/Placeholder.tsx';
import ServerApp from './ServerApp.tsx';

interface BootOptions {
  store: Pick<ServerStore, 'start'> & Partial<ServerStore>;
  show: (view: ReactNode) => void;
}

// Asks the server once which app this is, showing only the loading state
// while it asks. A server's answer, or none, mounts the server's app. A
// static host's answer means there is no server behind the page, so it says so.
export function boot({store, show}: BootOptions): Promise<'static' | 'server'> {
  show(<Placeholder status="Loading the page" />);
  return store.start().then(({mode}) => {
    if (mode === 'static') {
      show(
        <main className="fr">
          <section className="fr-signin">
            <p
              className="fr-signin__lead"
              role="alert"
            >
              This page needs its server. Open the address your gtm-connector server prints.
            </p>
          </section>
        </main>,
      );
      return 'static';
    }
    show(<ServerApp store={store as ServerStore} />);
    return 'server';
  });
}
