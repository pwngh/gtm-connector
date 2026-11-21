// Every golden card in testdata/oracle/cards, as the server stores it: the
// sealed file's exact bytes, their SHA-256 as the row records it, and the
// plain-text card the file holds. A row that holds inputs alone is left out.
// Only tests import it.

import {readFileSync} from 'node:fs';
import {gunzipSync} from 'node:zlib';

export interface SealedCard {
  file: string;
  key: string;
  account: string;
  bytes: Uint8Array<ArrayBuffer>;
  sha256: string;
  text: string;
}

/** The files of testdata/oracle/cards that hold cards: every family the index lists. */
export const cardFiles: string[] = Object.keys(
  (JSON.parse(readFileSync(new URL('../../../testdata/oracle/cards/index.json', import.meta.url), 'utf8')) as {families: Record<string, number>}).families,
);

export const sealedCards: SealedCard[] = cardFiles.flatMap(file =>
  gunzipSync(readFileSync(new URL(`../../../testdata/oracle/cards/${file}.jsonl.gz`, import.meta.url)))
    .toString()
    .split('\n')
    .filter(Boolean)
    .map(line => JSON.parse(line) as {key: string; account_id: string; card?: string; sha256?: string})
    .filter((row): row is {key: string; account_id: string; card: string; sha256: string} => row.card !== undefined)
    .map(row => {
      return {
        file,
        key: row.key,
        account: row.account_id,
        bytes: new Uint8Array(new TextEncoder().encode(row.card)),
        sha256: row.sha256,
        text: (JSON.parse(row.card) as {text: string}).text,
      };
    }),
);
