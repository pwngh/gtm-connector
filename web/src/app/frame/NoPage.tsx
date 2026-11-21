import {Link} from 'react-router';
import {home} from './pages.ts';

/** Any address inside the app that is not a page. */
export function NoPage() {
  return (
    <section className="fr-nopage">
      <h1 className="fr-nopage__title">There is no page at this address.</h1>
      <p className="fr-nopage__text">The server provides Evidence and Readiness. Check the address, or start again from Evidence.</p>
      <Link
        className="ui-button ui-button--primary"
        to={home}
      >
        Go to Evidence
      </Link>
    </section>
  );
}
