import type {ComponentType} from 'react';
import type {CardDetail, CardView} from '../../../api/types.ts';
import {MoreRead} from './MoreRead.tsx';
import {Notices} from './Notices.tsx';
import {NotRead} from './NotRead.tsx';
import {PeopleSaid} from './PeopleSaid.tsx';
import {Seal} from './Seal.tsx';
import {TheRecord} from './TheRecord.tsx';

/** What every section of the card is given: the open card, its account and the account's card list. */
export interface SectionProps {
  account: string;
  card: CardDetail;
  /** The account's published cards, newest first. */
  listed: readonly CardView[];
  /** The deal list's filter, kept on links back to this deal. */
  query: string;
}

/**
 * Where a section sits. Top and bottom span the card. On a card pane 760
 * wide or more, words is the right column and record the left; narrower,
 * the words come before the record.
 */
export type Area = 'top' | 'words' | 'record' | 'bottom';

export interface Section {
  id: string;
  area: Area;
  Section: ComponentType<SectionProps>;
}

// The card's sections, in reading order within each area. A later slice adds
// its section here, in its own file.
export const sections: Section[] = [
  {id: 'notices', area: 'top', Section: Notices},
  {id: 'people-said', area: 'words', Section: PeopleSaid},
  {id: 'more-read', area: 'words', Section: MoreRead},
  {id: 'the-record', area: 'record', Section: TheRecord},
  {id: 'not-read', area: 'bottom', Section: NotRead},
  {id: 'seal', area: 'bottom', Section: Seal},
];
