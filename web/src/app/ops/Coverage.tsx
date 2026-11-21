import {ArrowDown, ArrowUp, ArrowUpDown} from 'lucide-react';
import {type ReactNode, useState} from 'react';
import {Link, useSearchParams} from 'react-router';
import {ApiError, formatUtc} from '../../api/client.ts';
import {type Read, useRead, useServer} from '../../api/reads.ts';
import type {AccountCards, AccountView, Health} from '../../api/types.ts';
import {Button} from '../../ui/Button.tsx';
import {Placeholder} from '../../ui/Placeholder.tsx';
import {Refusal, refusalText} from '../../ui/Refusal.tsx';
import {serverPaths} from '../routes.ts';
import {
  type CardList,
  type CoverageRow,
  type Filter,
  filterOf,
  filterRows,
  newestCard,
  nextSort,
  type Sort,
  type SortKey,
  sortRows,
  summarize,
} from './coverageRows.ts';
import {type Pooled, usePooledReads} from './pooledReads.ts';

// Coverage: each deal the signed-in principal is granted, whether it has a
// card, and whether the server is healthy. Built from the account list, each
// account's card list and /healthz, and nothing else: the server sends no
// per-source state and no reason for a missing card, so neither is shown.

/** How many card lists are read at once. */
export const cardListsInFlight = 4;

const listKey = (account: string) => `cards:${account}`;

/** What /healthz answered, in words. */
export function healthSentence(status: string): string {
  if (status === 'ok') {
    return 'The database answers and its migrations are this binary’s.';
  }
  if (status === 'database_unavailable') {
    return 'The database did not answer.';
  }
  if (status.startsWith('migrations_')) {
    return 'The database answers, but its schema is not this binary’s.';
  }
  return 'The server answered with a status this page does not know.';
}

export function ServerLine({health}: {health: Read<Health>}) {
  let said: string;
  let status: string | null = null;
  if (health.status === 'ready') {
    status = health.data.status;
    said = healthSentence(status);
  } else if (health.status === 'failed') {
    said = `The health check could not be read. ${refusalText(health.error).title}`;
  } else {
    said = 'Reading the server’s health.';
  }
  return (
    <p
      className="cv-server"
      data-health={status ?? health.status}
    >
      <span className="cv-server__label">Server</span>
      <span>
        {said}
        {status && (
          <>
            {' '}
            <code className="cv-code">{status}</code>
          </>
        )}
      </span>
    </p>
  );
}

const plural = (count: number, one: string, many: string) => (count === 1 ? one : many);

export function SummaryLine({rows, filter, choose}: {rows: CoverageRow[]; filter: Filter; choose: (filter: Filter) => void}) {
  const summary = summarize(rows);
  const counts: [Filter, number, string][] = [
    ['all', summary.all, plural(summary.all, 'deal', 'deals')],
    ['card', summary.card, plural(summary.card, 'has a card', 'have a card')],
    ['none', summary.none, plural(summary.none, 'has none', 'have none')],
  ];
  if (summary.unread > 0) {
    counts.push(['unread', summary.unread, 'could not be read']);
  }
  return (
    <div className="cv-summary">
      <p className="cv-summary__counts">
        {counts.map(([id, count, words]) => (
          <Button
            key={id}
            className="cv-count"
            data-filter={id}
            aria-pressed={filter === id}
            onClick={() => choose(id)}
          >
            <strong>{count}</strong> {words}
          </Button>
        ))}
      </p>
      {summary.reading > 0 && (
        <p
          className="cv-meta"
          role="status"
        >
          Reading the cards of {summary.reading} {plural(summary.reading, 'deal', 'deals')}. The counts change as they arrive.
        </p>
      )}
    </div>
  );
}

const captions: Record<Filter, string> = {
  all: 'Every granted deal, with its newest card',
  card: 'Deals that have a card',
  none: 'Deals that have no card',
  unread: 'Deals whose cards could not be read',
};

const columns: {key: SortKey; label: string}[] = [
  {key: 'deal', label: 'Deal'},
  {key: 'newest', label: 'Newest card'},
  {key: 'cards', label: 'Cards'},
];

function SortHeader({column, sort, choose}: {column: {key: SortKey; label: string}; sort: Sort; choose: (key: SortKey) => void}) {
  const sorted = sort.key === column.key;
  const Icon = !sorted ? ArrowUpDown : sort.direction === 'ascending' ? ArrowUp : ArrowDown;
  return (
    <th
      scope="col"
      className={column.key === 'cards' ? 'cv-cards' : undefined}
      aria-sort={sorted ? sort.direction : undefined}
    >
      <button
        type="button"
        className="cv-sort"
        data-sort={column.key}
        onClick={() => choose(column.key)}
      >
        {column.label}
        <Icon
          size={16}
          aria-hidden="true"
        />
      </button>
    </th>
  );
}

function ListProblem({error}: {error: ApiError}) {
  const said = error.kind === 'not_permitted' ? 'Not permitted' : refusalText(error).title;
  return (
    <span
      className="cv-problem"
      data-refusal={error.kind}
    >
      {said}
      {error.code && <span className="cv-note">Server code: {error.code}</span>}
    </span>
  );
}

function NewestCell({list, row}: {list: CardList; row: CoverageRow}) {
  if (list.status === 'reading') {
    return <span className="cv-reading">Reading</span>;
  }
  if (list.status === 'failed') {
    return <ListProblem error={list.error} />;
  }
  const newest = newestCard(row);
  if (!newest) {
    return (
      <>
        No card
        <span className="cv-note">Nothing about its sources is inferred from that.</span>
      </>
    );
  }
  return <time dateTime={newest.published_at}>{formatUtc(newest.published_at)}</time>;
}

function cardHref(account: string, card: string): string {
  return `${serverPaths.evidence}?${new URLSearchParams({account, card})}`;
}

function DealRow({row}: {row: CoverageRow}) {
  const newest = newestCard(row);
  return (
    <tr data-account={row.account}>
      <th scope="row">
        <span className="cv-name">{row.name}</span>
        <span className="cv-id">{row.account}</span>
      </th>
      <td>
        <span className="cv-label">Newest card</span>
        <NewestCell
          list={row.list}
          row={row}
        />
      </td>
      <td className="cv-cards">
        {row.list.status === 'ready' && (
          <>
            <span className="cv-label">Cards</span>
            {row.list.cards.length}
          </>
        )}
      </td>
      <td>
        {newest && (
          <Link
            className="cv-open"
            to={cardHref(row.account, newest.id)}
          >
            Open the card<span className="cv-hidden"> of {row.name}</span>
          </Link>
        )}
      </td>
    </tr>
  );
}

export function CoverageTable({rows, filter, sort, choose}: {rows: CoverageRow[]; filter: Filter; sort: Sort; choose: (key: SortKey) => void}) {
  const shown = sortRows(filterRows(rows, filter), sort);
  if (shown.length === 0) {
    return <p className="cv-empty">No deal is in this view.</p>;
  }
  return (
    <table className="cv-table">
      <caption>{captions[filter]}</caption>
      <thead>
        <tr>
          {columns.map(column => (
            <SortHeader
              key={column.key}
              column={column}
              sort={sort}
              choose={choose}
            />
          ))}
          <th scope="col">
            <span className="cv-hidden">Link</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {shown.map(row => (
          <DealRow
            key={row.account}
            row={row}
          />
        ))}
      </tbody>
    </table>
  );
}

const isAccount = (value: unknown): value is AccountView =>
  typeof value === 'object' && value !== null && typeof (value as AccountView).account === 'string' && (value as AccountView).account !== '';

const unexpected = () => new ApiError({kind: 'unexpected'});

/** One row per account, with its card list as read so far. A list without its cards array is an answer that could not be read. */
export function rowsOf(accounts: AccountView[], lists: Map<string, Pooled<AccountCards>>): CoverageRow[] {
  return accounts.map((account): CoverageRow => {
    const read = lists.get(listKey(account.account)) ?? {status: 'reading'};
    let list: CardList;
    if (read.status === 'ready') {
      list = Array.isArray(read.data.cards) ? {status: 'ready', cards: read.data.cards} : {status: 'failed', error: unexpected()};
    } else {
      list = read;
    }
    return {account: account.account, name: typeof account.name === 'string' ? account.name : '', list};
  });
}

export default function Coverage() {
  const {state} = useServer();
  const signedIn = Boolean(state.session?.signed_in);
  const [params, setParams] = useSearchParams();
  const filter = filterOf(params.get('show'));
  const [sort, setSort] = useState<Sort>({key: 'deal', direction: 'ascending'});
  const accounts = useRead(signedIn ? 'accounts' : null, (api, signal) => api.accounts({signal}));
  const health = useRead(signedIn ? 'health' : null, (api, signal) => api.health({signal}));
  const listed = accounts.status === 'ready' && Array.isArray(accounts.data.accounts) ? accounts.data.accounts.filter(isAccount) : [];
  const lists = usePooledReads<AccountCards>(
    listed.map(account => listKey(account.account)),
    key => (api, signal) => api.accountCards(key.slice('cards:'.length), {signal}),
    cardListsInFlight,
  );
  const rows = rowsOf(listed, lists);

  const choose = (next: Filter) => {
    const kept = new URLSearchParams(params);
    if (next === 'all') {
      kept.delete('show');
    } else {
      kept.set('show', next);
    }
    setParams(kept, {replace: true, preventScrollReset: true});
  };

  let body: ReactNode;
  if (accounts.status === 'failed') {
    body = (
      <Refusal
        error={accounts.error}
        retry={accounts.retry}
      />
    );
  } else if (accounts.status !== 'ready') {
    body = <Placeholder status="Reading your deals" />;
  } else if (!Array.isArray(accounts.data.accounts)) {
    body = <Refusal error={unexpected()} />;
  } else if (listed.length === 0) {
    body = (
      <div className="cv-none">
        <p>No deal is granted to you.</p>
        <p className="cv-meta">A grant names the deals a person reads.</p>
      </div>
    );
  } else {
    body = (
      <SummaryLine
        rows={rows}
        filter={filter}
        choose={choose}
      />
    );
  }

  return (
    <section
      className="cv"
      aria-labelledby="cv-title"
    >
      <h1
        id="cv-title"
        className="cv-title"
      >
        Coverage
      </h1>
      <p className="cv-meta">Which deals granted to you have a card, read from the server as you. Times are UTC.</p>
      {body}
      <ServerLine health={health} />
      {listed.length > 0 && (
        <CoverageTable
          rows={rows}
          filter={filter}
          sort={sort}
          choose={key => setSort(current => nextSort(current, key))}
        />
      )}
    </section>
  );
}
