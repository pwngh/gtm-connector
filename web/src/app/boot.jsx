import React from 'react';
import {RouteLoading} from '../RouteContent.jsx';
import ServerApp from './ServerApp.jsx';

// Asks the server once which app this is, showing only the loading state
// while it asks. A server's answer, or none, mounts the server's app. Only a
// static host's answer loads the walkthrough, with `loadWalkthrough`.
export function boot({store, show, loadWalkthrough}) {
  show(<RouteLoading label="page"/>);
  return store.start().then(({mode}) => {
    if (mode !== 'walkthrough') {
      show(<ServerApp store={store}/>);
      return 'server';
    }
    return loadWalkthrough().then(({default: Walkthrough}) => {
      show(<Walkthrough/>);
      return 'walkthrough';
    }, () => {
      show(<section className="route-loading route-error"><p role="alert">This page could not load. Reload the page to try again.</p></section>);
      return 'failed';
    });
  });
}
