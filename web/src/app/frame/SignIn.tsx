import {type FormEvent, useEffect, useRef, useState} from 'react';
import {useLocation} from 'react-router';
import {ApiError, companyRefusals, companySignInHref} from '../../api/client.ts';
import type {ServerStore} from '../../api/session.ts';
import type {SessionView, SignInView} from '../../api/types.ts';
import {Button} from '../../ui/Button.tsx';
import {Field} from '../../ui/Field.tsx';
import {Refusal} from '../../ui/Refusal.tsx';

export interface SignInProps {
  store: ServerStore;
  /** The session ended here without a sign-out. */
  ended: boolean;
  /** A session another tab signed this browser in to. */
  elsewhere: SessionView | null;
  /** Why the server could not be asked for a session, when it could not. */
  problem: ApiError | null;
  /** The ways in the server offers. */
  ways: SignInView;
  /** Why a sign-in through the identity provider came back refused, by the reason it came back with. */
  refused: string | null;
  /** The identity provider's own sign-out page, which the sign-out that just happened here offers. */
  endSessionUrl: string | null;
}

const asError = (error: unknown) => (error instanceof ApiError ? error : new ApiError());

// The one screen a signed-out person sees, at whatever address they opened.
// The address stays, so the page asked for renders after sign-in: the demo
// form signs in here, and the company sign-in comes back to the same address.
export function SignIn({store, ended, elsewhere, problem, ways, refused, endSessionUrl}: SignInProps) {
  const location = useLocation();
  const password = useRef<HTMLInputElement>(null);
  const [login, setLogin] = useState('');
  const [secret, setSecret] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  // A refused sign-in clears the password and puts focus there.
  useEffect(() => {
    if (error?.kind === 'sign_in_refused') {
      password.current?.focus();
    }
  }, [error]);
  async function send() {
    if (pending) {
      return;
    }
    setPending(true);
    setError(null);
    try {
      await store.signIn(login, secret);
    } catch (failure) {
      const refused = asError(failure);
      if (refused.kind === 'sign_in_refused') {
        setSecret('');
      }
      setError(refused);
      setPending(false);
    }
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    send();
  }
  const name = elsewhere?.principal?.display_name;
  const lead = ways.oidc
    ? 'Sign in with the account your company gives you.'
    : ways.demo
      ? 'This install signs in a demo principal. Its records are invented.'
      : 'This install offers no sign-in for the web app.';
  return (
    <section
      className="fr-signin"
      aria-labelledby="fr-signin-title"
    >
      <h1
        id="fr-signin-title"
        className="fr-signin__title"
        tabIndex={-1}
      >
        Sign in
      </h1>
      <p className="fr-signin__lead">{lead}</p>
      {ended && (
        <p
          className="fr-signin__notice"
          role="status"
        >
          Your session ended. Sign in again.
        </p>
      )}
      {endSessionUrl && (
        <div
          className="fr-signin__notice fr-signin__provider"
          role="status"
        >
          <strong>You are signed out here.</strong>
          <p className="fr-signin__text">Your company account may still be signed in on this browser.</p>
          <a
            className="ui-button"
            href={endSessionUrl}
          >
            Sign out of your company account
          </a>
        </div>
      )}
      {refused !== null && <Refusal error={{kind: companyRefusals[refused] ?? 'sign_in_failed'}} />}
      {name !== undefined && (
        <div
          className="fr-signin__notice fr-signin__elsewhere"
          role="alert"
        >
          <strong>This browser is already signed in as {name}, from another tab.</strong>
          <p className="fr-signin__text">The login you typed was not checked. Continue as that principal, or sign in again to check yours.</p>
          <Button onClick={() => store.adoptElsewhere()}>Continue as {name}</Button>
        </div>
      )}
      {problem ? (
        <Refusal
          error={problem}
          retry={() => store.refresh()}
        />
      ) : (
        <>
          {ways.oidc && (
            <a
              className="ui-button ui-button--primary fr-signin__company"
              href={companySignInHref(location.pathname + location.search)}
            >
              Sign in with your company account
            </a>
          )}
          {ways.demo && (
            <form
              className="fr-signin__form"
              aria-labelledby={ways.oidc ? 'fr-signin-demo' : undefined}
              onSubmit={submit}
            >
              {ways.oidc && (
                <h2
                  id="fr-signin-demo"
                  className="fr-signin__subtitle"
                >
                  The demo workspace
                </h2>
              )}
              <Field
                label="Login"
                name="login"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                required
                value={login}
                onChange={event => setLogin(event.target.value)}
              />
              <Field
                ref={password}
                label="Password"
                name="password"
                type="password"
                autoComplete="current-password"
                required
                value={secret}
                onChange={event => setSecret(event.target.value)}
              />
              <SignInHelp />
              {error && error.kind !== 'already_signed_in' && (
                <Refusal
                  error={error}
                  retry={error.kind === 'sign_in_refused' ? undefined : send}
                />
              )}
              <Button
                type="submit"
                variant={ways.oidc ? 'plain' : 'primary'}
                pending={pending}
              >
                {pending ? 'Signing in' : 'Sign in'}
              </Button>
            </form>
          )}
        </>
      )}
      {problem && ways.demo && <SignInHelp />}
    </section>
  );
}

function SignInHelp() {
  return (
    <p className="fr-signin__text">
      The login is <code className="fr-code">demo-reviewer</code>. The password is in <code className="fr-code">demo-credentials.json</code> in this install’s
      credentials folder. <code className="fr-code">setup</code> printed the folder’s path.
    </p>
  );
}
