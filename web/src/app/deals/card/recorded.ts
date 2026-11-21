import {readFileSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';
import type {CardDetail, GapView, Json} from '../../../api/types.ts';

// For the tests: every recorded card from testdata/oracle/cards, shaped as
// GET /api/v1/cards/{card} returns it, with the lines the server builds for it
// from testdata/card_lines.jsonl.gz, which a Go test holds to the server's own
// builder. Only the IDs and the publish time are made up, as the server makes
// its own.

export interface Recorded {
  file: string;
  key: string;
  card: CardDetail;
}

export const files = ['demo', 'cases', 'varied_demo'];

const gunzipped = (path: string) =>
  gunzipSync(readFileSync(new URL(`../../../../../testdata/${path}`, import.meta.url)))
    .toString()
    .split('\n')
    .filter(Boolean);

const recordedLines = new Map(
  gunzipped('card_lines.jsonl.gz').map(line => {
    const row = JSON.parse(line) as {family: string; key: string; lines: Json[]};
    return [`${row.family} ${row.key}`, row.lines];
  }),
);

function shaped(account: string, id: string, sealed: Record<string, unknown>, sha256: string, size: number, lines: Json[] | null): CardDetail {
  const units = sealed.units as Record<string, unknown>[];
  return {
    id,
    account,
    brief_id: String(sealed.brief_id),
    kind: 'go',
    schema_version: 3,
    rules_version: '3',
    sha256,
    size,
    seal_receipt_id: `seal-${id}`,
    published_at: '2027-01-15T15:00:04Z',
    as_of: String(sealed.as_of),
    sources: null,
    lines,
    units: units.map(body => ({
      card: id,
      n: body.n as number,
      kind: body.kind as string,
      record_ref: body.record_ref as string,
      tool: body.tool as string,
      ...(body.kind === 'sentence' ? {shown: body.shown as string} : {}),
      receipt_id: `receipt-${body.tool}`,
      record_version_id: `version-${body.n}`,
      body: body as never,
    })),
    gaps: sealed.gaps as GapView[],
  };
}

const recordedFiles = (names: string[]): Recorded[] =>
  names.flatMap(file =>
    gunzipped(`oracle/cards/${file}.jsonl.gz`).map((line, at) => {
      const row = JSON.parse(line) as {key: string; account_id: string; card: string; sha256: string};
      const lines = recordedLines.get(`${file} ${row.key}`) ?? null;
      return {file, key: row.key, card: shaped(row.account_id, `card-${file}-${at}`, JSON.parse(row.card), row.sha256, row.card.length, lines)};
    }),
  );

export const recorded: Recorded[] = recordedFiles(files);

/** The Salesforce book's cards: each counts to a contract end or has none, and has no ARR line. */
export const salesforce: Recorded[] = recordedFiles(['books_salesforce']);

/** The card as one published before its lines were kept. */
export const unlined = (card: CardDetail): CardDetail => ({...card, lines: null});

export const find = (file: string, key: string) => recorded.find(item => item.file === file && item.key === key)?.card as CardDetail;

/** The README's deal: the demo's acc-003. */
export const thornfield = () => find('demo', 'acc-003');
