import {CircleAlert} from 'lucide-react';
import {Button} from './Button.tsx';
import './refusal.css';

/** A refusal or failure as a page has it: the kind, and the server's code when it sent one. */
export interface Problem {
  kind?: string;
  code?: string | null;
}

// What a refusal or failure means for the person reading the page. A refusal
// is never shown as an empty result.
export function refusalText(error: Problem | null | undefined): {title: string; detail: string} {
  switch (error?.kind) {
    case 'not_permitted':
      return {
        title: 'Not permitted.',
        detail: 'The signed-in principal holds no grant for this, or nothing by this ID is visible to it. This is a refusal, not an empty result.',
      };
    case 'signed_out':
      return {title: 'Your session ended.', detail: 'Sign in again to read from the server.'};
    case 'sign_in_refused':
      return {title: 'The login or password is not right.', detail: ''};
    case 'not_found':
      return {title: 'Not found.', detail: 'The server has nothing at this address.'};
    case 'unreachable':
      return {title: 'The server did not answer.', detail: 'Nothing was read, and nothing is shown in its place.'};
    case 'server':
      return {title: 'The server could not answer.', detail: 'Nothing was read. Try again in a moment.'};
    case 'host':
      return {title: 'This address is not served.', detail: 'Open the app at the server’s configured address.'};
    case 'redirect':
      return {title: 'The server redirected this request.', detail: 'The redirect was not followed, and nothing was read.'};
    case 'mismatch':
      return {
        title: 'The server returned another card.',
        detail: 'It is not the card of this account that was asked for, so nothing from it is shown.',
      };
    case 'sign_in_denied':
      return {title: 'The sign-in was cancelled.', detail: 'Your company account did not approve it. Sign in again when you are ready.'};
    case 'sign_in_not_allowed':
      return {title: 'Your company account may not sign in here.', detail: 'Ask the person who runs this install to allow your account.'};
    case 'sign_in_disabled':
      return {title: 'Your access here is turned off.', detail: 'Ask the person who runs this install.'};
    case 'sign_in_expired':
      return {title: 'The sign-in took too long or was already used.', detail: 'Nothing was signed in. Sign in again.'};
    case 'sign_in_unavailable':
      return {title: 'Your company’s sign-in could not be reached.', detail: 'Nothing was signed in. Try again in a moment.'};
    case 'sign_in_failed':
      return {title: 'The sign-in could not be checked.', detail: 'Nothing was signed in. Sign in again.'};
    case 'origin':
    case 'csrf':
      return {title: 'The server refused this request.', detail: 'It was refused even with a fresh sign-in token. Reload the page and try again.'};
    default:
      return {title: 'The server’s answer could not be read.', detail: 'Nothing is shown in its place.'};
  }
}

export interface RefusalProps {
  error: Problem | null | undefined;
  retry?: (() => void) | undefined;
}

/** A refusal: ink, a rule on the left and an icon, never a deal colour. */
export function Refusal({error, retry}: RefusalProps) {
  const {title, detail} = refusalText(error);
  return (
    <div
      className="ui-refusal"
      role="alert"
      data-refusal={error?.kind || 'unexpected'}
    >
      <CircleAlert
        className="ui-refusal__icon"
        size={20}
        aria-hidden="true"
      />
      <div className="ui-refusal__body">
        <strong className="ui-refusal__title">{title}</strong>
        {detail && <p className="ui-refusal__detail">{detail}</p>}
        {error?.code && <p className="ui-refusal__code">Server code: {error.code}</p>}
        {retry && <Button onClick={retry}>Try again</Button>}
      </div>
    </div>
  );
}
