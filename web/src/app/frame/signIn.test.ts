import assert from 'node:assert/strict';
import {after, before, test} from 'node:test';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {MemoryRouter} from 'react-router';
import {createServer, type ViteDevServer} from 'vite';

let server: ViteDevServer;
// biome-ignore lint/suspicious/noExplicitAny: the module is loaded through Vite at run time.
let signIn: any;
before(async () => {
  server = await createServer({configFile: false, server: {middlewareMode: true, hmr: false, ws: false}, appType: 'custom', logLevel: 'error'});
  signIn = await server.ssrLoadModule('/src/app/frame/SignIn.tsx');
});
after(async () => {
  await server?.close();
});

// The sign-in screen at an address with a query, as the server's ways in say.
function screen(ways: {oidc: boolean; demo: boolean}, extra: Record<string, unknown> = {}) {
  const props = {store: {}, ended: false, elsewhere: null, problem: null, ways, refused: null, endSessionUrl: null, ...extra};
  return renderToStaticMarkup(createElement(MemoryRouter, {initialEntries: ['/deals/acc-003?card=card-1']}, createElement(signIn.SignIn, props)));
}
const text = (html: string) =>
  html
    .replace(/<[^>]+>/g, ' ')
    .replaceAll('&#x27;', "'")
    .replaceAll('&amp;', '&')
    .replace(/\s+/g, ' ');

test('the sign-in screen offers the company sign-in when the server does, and the demo form only with the demo sign-in', () => {
  const company = screen({oidc: true, demo: false});
  assert.match(
    company,
    /<a class="ui-button ui-button--primary fr-signin__company" href="\/api\/v1\/session\/oidc\?return=%2Fdeals%2Facc-003%3Fcard%3Dcard-1">Sign in with your company account<\/a>/,
    'the company sign-in comes back to the address that was open',
  );
  assert.doesNotMatch(company, /name="password"|demo-reviewer|The demo workspace/);
  assert.match(text(company), /Sign in with the account your company gives you\./);

  const demo = screen({oidc: false, demo: true});
  assert.match(demo, /name="password"/);
  assert.match(demo, /<button type="submit" class="ui-button ui-button--primary">Sign in<\/button>/);
  assert.doesNotMatch(demo, /fr-signin__company|The demo workspace/);
  assert.match(text(demo), /This install signs in a demo principal\. Its records are invented\./);

  const both = screen({oidc: true, demo: true});
  assert.match(both, /fr-signin__company/);
  assert.match(both, /<h2 id="fr-signin-demo" class="fr-signin__subtitle">The demo workspace<\/h2>/);
  assert.match(both, /<button type="submit" class="ui-button ui-button--plain">Sign in<\/button>/, 'one primary action on the screen');

  const neither = screen({oidc: false, demo: false});
  assert.doesNotMatch(neither, /<form|fr-signin__company/);
  assert.match(text(neither), /This install offers no sign-in for the web app\./);
});

test('a refused company sign-in says why, and a sign-out offers the provider’s own sign-out page', () => {
  const refused = screen({oidc: true, demo: false}, {refused: 'not_allowed'});
  assert.match(refused, /data-refusal="sign_in_not_allowed"/);
  assert.match(text(refused), /Your company account may not sign in here\./);
  assert.match(screen({oidc: true, demo: false}, {refused: 'anything else'}), /data-refusal="sign_in_failed"/);

  const offered = 'https://login.invented.example/logout?client_id=connector-invented-client';
  const out = screen({oidc: true, demo: false}, {endSessionUrl: offered});
  assert.match(
    out,
    /<a class="ui-button" href="https:\/\/login\.invented\.example\/logout\?client_id=connector-invented-client">Sign out of your company account<\/a>/,
  );
  assert.match(text(out), /You are signed out here\. Your company account may still be signed in on this browser\./);
});
