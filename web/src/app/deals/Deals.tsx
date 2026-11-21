import {ArrowLeft} from 'lucide-react';
import {type KeyboardEvent, type ReactNode, type RefObject, useEffect, useRef, useState} from 'react';
import {Link, useLocation, useSearchParams} from 'react-router';
import {demoAccount, formatUtc} from '../../api/client.ts';
import {readsFor, useRead, useServer} from '../../api/reads.ts';
import type {AccountCards, AccountView} from '../../api/types.ts';
import {Button} from '../../ui/Button.tsx';
import {Field} from '../../ui/Field.tsx';
import {Refusal, refusalText} from '../../ui/Refusal.tsx';
import {displayStorage} from '../../ui/theme.ts';
import DealCard from './card/DealCard.tsx';
import {byName, cardReadLimit, dealAt, dealHref, dealsPath, hintKey, matching, type RowRead, readInTurn, rowFrom, useMedia, wideQuery} from './deals.ts';

// Deals: every account the server grants the signed-in person, by name, each
// with the time of its newest card. At 1100 and wider the list is a rail and
// the open deal is beside it; narrower, the list and the deal are each the
// whole page. The open deal is its Deal card.

const readAccounts = 'accounts';
const cardsKey = (account: string) => `cards:${account}`;

// Each account's card list, read in name order with at most four reads in
// flight. A row's answer is kept under its own account, so a late answer
// never lands on another row. Answers already read in this session show
// while they are read again.
function useNewestCards(accounts: readonly string[] | null): (account: string) => RowRead {
  const {store, state} = useServer();
  const run = accounts === null ? null : `${state.generation}|${accounts.join('\n')}`;
  const [rows, setRows] = useState<{run: string | null; read: Record<string, RowRead>}>({run: null, read: {}});
  // biome-ignore lint/correctness/useExhaustiveDependencies: run names the accounts and the session, and a new run reads afresh.
  useEffect(() => {
    if (run === null || accounts === null) {
      return undefined;
    }
    const reads = readsFor(store);
    setRows({run, read: {}});
    return readInTurn(accounts, cardReadLimit, (account, done) =>
      reads.request<AccountCards>(
        `${state.generation}|${cardsKey(account)}`,
        (api, signal) => api.accountCards(account, {signal}),
        outcome => {
          setRows(current => (current.run === run ? {run, read: {...current.read, [account]: rowFrom(outcome)}} : current));
          done();
        },
      ),
    );
  }, [run, store]);
  return account => {
    const answered = rows.run === run ? rows.read[account] : undefined;
    if (answered) {
      return answered;
    }
    const last = readsFor(store).last<AccountCards>(`${state.generation}|${cardsKey(account)}`);
    return last ? rowFrom({status: 'ready', data: last.data}) : {status: 'reading'};
  };
}

function RowCard({read}: {read: RowRead}) {
  if (read.status === 'reading') {
    return (
      <span className="deals-row__card">
        <span className="deals-row__value">Reading</span>
      </span>
    );
  }
  if (read.status === 'failed') {
    return (
      <span
        className="deals-row__card"
        data-refusal={read.error.kind}
      >
        <span className="deals-row__value">{refusalText(read.error).title.replace(/\.$/, '')}</span>
        {read.error.code && <span className="deals-row__meta">Server code: {read.error.code}</span>}
      </span>
    );
  }
  if (!read.newest) {
    return (
      <span className="deals-row__card">
        <span className="deals-row__value">No card</span>
      </span>
    );
  }
  return (
    <span className="deals-row__card">
      <span className="deals-row__label">Card published</span>
      <time
        className="deals-row__value"
        dateTime={read.newest.published_at}
      >
        {formatUtc(read.newest.published_at)}
      </time>
    </span>
  );
}

// Up and Down move between rows, Home and End jump; Enter opens the row's link.
const keyTargets: Record<string, (at: number, last: number) => number> = {
  ArrowDown: (at, last) => Math.min(at + 1, last),
  ArrowUp: at => Math.max(at - 1, 0),
  Home: () => 0,
  End: (_, last) => last,
};

function moveFocus(event: KeyboardEvent<HTMLUListElement>) {
  const target = keyTargets[event.key];
  if (!target || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) {
    return;
  }
  const rows = [...event.currentTarget.querySelectorAll<HTMLAnchorElement>('.deals-row')];
  const at = rows.indexOf(document.activeElement as HTMLAnchorElement);
  if (at < 0) {
    return;
  }
  event.preventDefault();
  rows[target(at, rows.length - 1)]?.focus();
}

interface RowsProps {
  deals: AccountView[];
  open: string | null;
  query: string;
  rowRead: (account: string) => RowRead;
  list: RefObject<HTMLUListElement | null>;
}

// The rows: one link each. Only one row is in the Tab order, the last one
// focused, else the open deal, else the first; the arrow keys move between them.
function Rows({deals, open, query, rowRead, list}: RowsProps) {
  const [focused, setFocused] = useState<string | null>(null);
  const tabbable = [focused, open].find(account => account !== null && deals.some(deal => deal.account === account)) ?? deals[0]?.account;
  return (
    <ul
      ref={list}
      className="deals-rows"
      aria-label="Deals by name"
      onKeyDown={moveFocus}
    >
      {deals.map(deal => (
        <li
          key={deal.account}
          className="deals-rows__item"
        >
          <Link
            to={dealHref(deal.account, query)}
            className="deals-row"
            aria-current={deal.account === open ? 'page' : undefined}
            tabIndex={deal.account === tabbable ? 0 : -1}
            data-account={deal.account}
            onFocus={() => setFocused(deal.account)}
          >
            <span className="deals-row__who">
              <span className="deals-row__name">{deal.name || deal.account}</span>
              <span className="deals-row__id">{deal.account}</span>
            </span>
            <RowCard read={rowRead(deal.account)} />
          </Link>
        </li>
      ))}
    </ul>
  );
}

function Reading() {
  return (
    <div className="deals-reading">
      <ul
        className="deals-rows"
        aria-hidden="true"
      >
        {['1', '2', '3', '4', '5', '6'].map(key => (
          <li
            key={key}
            className="deals-rows__item deals-placeholder"
          >
            <span className="deals-placeholder__name" />
            <span className="deals-placeholder__id" />
          </li>
        ))}
      </ul>
      <p
        className="deals-status"
        role="status"
      >
        Reading your deals
      </p>
    </div>
  );
}

// One line for a demo principal whose list holds the README's deal, until dismissed.
function FirstRunHint({deal, query}: {deal: AccountView; query: string}) {
  const [dismissed, setDismissed] = useState(() => displayStorage.get(hintKey) === 'dismissed');
  if (dismissed) {
    return null;
  }
  return (
    <p className="deals-hint">
      <span className="deals-hint__text">
        New here? Open{' '}
        <Link
          className="deals-hint__link"
          to={dealHref(deal.account, query)}
        >
          {deal.name || deal.account}
        </Link>
        , the deal the README walks through.
      </span>
      <Button
        variant="quiet"
        className="deals-hint__dismiss"
        onClick={() => {
          displayStorage.set(hintKey, 'dismissed');
          setDismissed(true);
        }}
      >
        Dismiss
      </Button>
    </p>
  );
}

// The filter as typed, kept in the address as ?q=. The router applies an
// address in a transition, after later keystrokes, so the field holds its own
// value: an address the field wrote is marked in its state and never read back
// into it; any other address, as a link to /deals, is.
function useFilter(): [string, (value: string) => void] {
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const query = params.get('q') ?? '';
  const [text, setText] = useState(query);
  const written = (location.state as {filter?: boolean} | null)?.filter === true;
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new address, even with the same ?q=, is read again.
  useEffect(() => {
    if (!written) {
      setText(query);
    }
  }, [query, written, location.key]);
  const setQuery = (value: string) => {
    setText(value);
    setParams(
      current => {
        const next = new URLSearchParams(current);
        if (value) {
          next.set('q', value);
        } else {
          next.delete('q');
        }
        return next;
      },
      {replace: true, preventScrollReset: true, state: {filter: true}},
    );
  };
  return [text, setQuery];
}

function DealList({open, titled}: {open: string | null; titled: boolean}) {
  const {state} = useServer();
  const [query, setQuery] = useFilter();
  const field = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const accounts = useRead(readAccounts, (api, signal) => api.accounts({signal}));
  const granted = accounts.status === 'ready' && Array.isArray(accounts.data?.accounts) ? byName(accounts.data.accounts) : null;
  const rowRead = useNewestCards(granted?.map(deal => deal.account) ?? null);
  const shown = granted ? matching(granted, query) : [];
  const demo = state.session?.principal?.demo === true;
  const readme = demo ? granted?.find(deal => deal.account === demoAccount) : undefined;
  const Title = titled ? 'h1' : 'h2';

  // The open deal's row is in view in the rail when the page opens.
  // biome-ignore lint/correctness/useExhaustiveDependencies: only when the rows first arrive.
  useEffect(() => {
    list.current?.querySelector('[aria-current="page"]')?.scrollIntoView({block: 'nearest'});
  }, [granted === null]);

  let body: ReactNode;
  if (accounts.status === 'failed') {
    body = (
      <Refusal
        error={accounts.error}
        retry={accounts.retry}
      />
    );
  } else if (accounts.status === 'ready' && !granted) {
    body = <Refusal error={{kind: 'unexpected'}} />;
  } else if (!granted) {
    body = <Reading />;
  } else if (!granted.length && demo) {
    body = (
      <div className="deals-empty">
        <p className="deals-empty__text">
          No deals yet. This demo install has no cards. Run <code className="deals-empty__code">make demo</code>, then read again.
        </p>
        <Button onClick={accounts.retry}>Read again</Button>
      </div>
    );
  } else if (!granted.length) {
    body = (
      <div className="deals-empty">
        <p className="deals-empty__text">No deals are granted to you.</p>
        <p className="deals-empty__meta">A grant names the deals a person reads.</p>
      </div>
    );
  } else if (!shown.length) {
    body = (
      <div className="deals-empty">
        <p
          className="deals-empty__text"
          role="status"
        >
          No deal matches.
        </p>
        <Button
          onClick={() => {
            setQuery('');
            field.current?.focus();
          }}
        >
          Clear the filter
        </Button>
      </div>
    );
  } else {
    body = (
      <Rows
        deals={shown}
        open={open}
        query={query}
        rowRead={rowRead}
        list={list}
      />
    );
  }

  return (
    <section
      className="deals-list"
      aria-labelledby="deals-title"
    >
      <div className="deals-head">
        <Title
          id="deals-title"
          className="deals-head__title"
        >
          Deals
          {granted && (
            <span className="deals-head__count">{query && shown.length !== granted.length ? `${shown.length} of ${granted.length}` : granted.length}</span>
          )}
        </Title>
        <p className="deals-head__meta">Listed by name. This server does not rank deals yet.</p>
        <div className="deals-head__filter">
          <Field
            ref={field}
            label="Filter by name"
            type="search"
            name="q"
            autoComplete="off"
            spellCheck={false}
            value={query}
            onChange={event => setQuery(event.target.value)}
            onKeyDown={event => {
              if (event.key === 'ArrowDown' && list.current) {
                event.preventDefault();
                list.current.querySelector<HTMLAnchorElement>('.deals-row')?.focus();
              }
            }}
          />
        </div>
      </div>
      {readme && (
        <FirstRunHint
          deal={readme}
          query={query}
        />
      )}
      {body}
    </section>
  );
}

// The open deal on its own page, with a way back to the list when the list is not beside it.
function OpenDeal({account, wide}: {account: string; wide: boolean}) {
  const [params] = useSearchParams();
  const query = params.get('q');
  return (
    <div className="deals-open">
      {!wide && (
        <Link
          className="deals-back"
          to={query ? `${dealsPath}?${new URLSearchParams({q: query})}` : dealsPath}
        >
          <ArrowLeft
            size={20}
            aria-hidden="true"
          />
          Deals
        </Link>
      )}
      <DealCard account={account} />
    </div>
  );
}

export default function Deals() {
  const {pathname} = useLocation();
  const wide = useMedia(wideQuery);
  const open = dealAt(pathname);
  if (!wide) {
    return (
      <div className="deals">
        {open === null ? (
          <DealList
            open={null}
            titled
          />
        ) : (
          <OpenDeal
            account={open}
            wide={false}
          />
        )}
      </div>
    );
  }
  return (
    <div className="deals deals--split">
      <div className="deals-rail">
        <DealList
          open={open}
          titled={open === null}
        />
      </div>
      <div className="deals-pane">
        {open === null ? (
          <p className="deals-choose">Choose a deal.</p>
        ) : (
          <OpenDeal
            account={open}
            wide
          />
        )}
      </div>
    </div>
  );
}
