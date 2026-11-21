import type {ApiError} from '../../api/client.ts';
import type {CardView} from '../../api/types.ts';

// The rows of Coverage, built only from what the server sent: each granted
// account (a deal), and its card list as read, refused or still reading.
// Nothing here guesses why a deal has no card.

export type CardList = {status: 'reading'} | {status: 'ready'; cards: CardView[]} | {status: 'failed'; error: ApiError};

export interface CoverageRow {
  account: string;
  name: string;
  list: CardList;
}

/** Which rows a summary count stands for. "all" is every row. */
export type Filter = 'all' | 'card' | 'none' | 'unread';

const named: Filter[] = ['card', 'none', 'unread'];

/** The filter in the address, from its `show` value. Anything else is every row. */
export function filterOf(value: string | null): Filter {
  return named.find(filter => filter === value) ?? 'all';
}

/** The group a row counts in: it has a card, it has none, its list could not be read, or it is still reading. */
export function groupOf(row: CoverageRow): 'card' | 'none' | 'unread' | 'reading' {
  switch (row.list.status) {
    case 'ready':
      return row.list.cards.length > 0 ? 'card' : 'none';
    case 'failed':
      return 'unread';
    default:
      return 'reading';
  }
}

export interface Summary {
  all: number;
  card: number;
  none: number;
  unread: number;
  reading: number;
}

export function summarize(rows: CoverageRow[]): Summary {
  const summary: Summary = {all: rows.length, card: 0, none: 0, unread: 0, reading: 0};
  for (const row of rows) {
    summary[groupOf(row)] += 1;
  }
  return summary;
}

export function filterRows(rows: CoverageRow[], filter: Filter): CoverageRow[] {
  return filter === 'all' ? rows : rows.filter(row => groupOf(row) === filter);
}

/** The newest card of a list the server sent newest first. */
export function newestCard(row: CoverageRow): CardView | null {
  return row.list.status === 'ready' ? (row.list.cards[0] ?? null) : null;
}

export type SortKey = 'deal' | 'newest' | 'cards';
export interface Sort {
  key: SortKey;
  direction: 'ascending' | 'descending';
}

const byDeal = (a: CoverageRow, b: CoverageRow) => a.name.localeCompare(b.name, 'en') || (a.account < b.account ? -1 : a.account > b.account ? 1 : 0);

// A row whose list is not read has no time and no count, so it sorts after
// every row that has them, in either direction. A deal with no card has the
// lowest time and a count of 0.
function value(row: CoverageRow, key: 'newest' | 'cards'): number | null {
  if (row.list.status !== 'ready') {
    return null;
  }
  if (key === 'cards') {
    return row.list.cards.length;
  }
  const time = Date.parse(newestCard(row)?.published_at ?? '');
  return Number.isFinite(time) ? time : Number.NEGATIVE_INFINITY;
}

export function sortRows(rows: CoverageRow[], sort: Sort): CoverageRow[] {
  const sign = sort.direction === 'ascending' ? 1 : -1;
  return [...rows].sort((a, b) => {
    if (sort.key === 'deal') {
      return sign * byDeal(a, b);
    }
    const x = value(a, sort.key);
    const y = value(b, sort.key);
    if (x === null || y === null) {
      return x === y ? byDeal(a, b) : x === null ? 1 : -1;
    }
    return x === y ? byDeal(a, b) : sign * (x < y ? -1 : 1);
  });
}

/** A new sort for a pressed column header: the same column turns round, another starts ascending. */
export function nextSort(sort: Sort, key: SortKey): Sort {
  if (sort.key === key) {
    return {key, direction: sort.direction === 'ascending' ? 'descending' : 'ascending'};
  }
  return {key, direction: 'ascending'};
}
