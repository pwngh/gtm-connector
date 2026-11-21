import React from 'react';
import {RouteLoading} from '../RouteContent.jsx';
import ServerApp from './ServerApp.jsx';

// Asks the server once which app this is, showing only the loading state
// while it asks. A server's answer, or none, mounts the server's app. A
// static host's answer means there is no server behind the page, so it says so.
export function boot({store, show}) {
  show(<RouteLoading label="page"/>);
  return store.start().then(({mode}) => {
    if (mode === 'static') {
      show(<main className="main-content"><section className="route-loading route-error"><p role="alert">This page needs its server. Open the address your gtm-connector server prints.</p></section></main>);
      return 'static';
    }
    show(<ServerApp store={store}/>);
    return 'server';
  });
}
