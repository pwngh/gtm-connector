import {Link} from 'react-router';
import {home, opsHome} from './pages.ts';

/** Any address inside the app that is not a page. */
export function NoPage() {
  return (
    <section className="fr-nopage">
      <h1 className="fr-nopage__title">There is no page at this address.</h1>
      <p className="fr-nopage__text">Check the address, or start again from Deals or Ops.</p>
      <div className="fr-nopage__links">
        <Link
          className="ui-button ui-button--primary"
          to={home}
        >
          Go to Deals
        </Link>
        <Link
          className="ui-button"
          to={opsHome}
        >
          Go to Ops
        </Link>
      </div>
    </section>
  );
}
