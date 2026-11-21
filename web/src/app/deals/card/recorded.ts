import {readFileSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';
import type {CardDetail, GapView} from '../../../api/types.ts';

// For the tests: every recorded card from testdata/oracle/cards, shaped as
// GET /api/v1/cards/{card} returns it. Only the IDs and the publish time are
// made up, as the server makes its own.

export interface Recorded {
  file: string;
  key: string;
  card: CardDetail;
}

export const files = ['demo', 'cases', 'varied_demo'];

function shaped(account: string, id: string, sealed: Record<string, unknown>, sha256: string, size: number): CardDetail {
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
    lines: null,
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

export const recorded: Recorded[] = files.flatMap(file =>
  gunzipSync(readFileSync(new URL(`../../../../../testdata/oracle/cards/${file}.jsonl.gz`, import.meta.url)))
    .toString()
    .split('\n')
    .filter(Boolean)
    .map((line, at) => {
      const row = JSON.parse(line) as {key: string; account_id: string; card: string; sha256: string};
      return {file, key: row.key, card: shaped(row.account_id, `card-${file}-${at}`, JSON.parse(row.card), row.sha256, row.card.length)};
    }),
);

export const find = (file: string, key: string) => recorded.find(item => item.file === file && item.key === key)?.card as CardDetail;

/** The README's deal: the demo's acc-003. */
export const thornfield = () => find('demo', 'acc-003');
