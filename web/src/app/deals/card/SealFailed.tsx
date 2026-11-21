import {CircleAlert} from 'lucide-react';
import {Link} from 'react-router';
import {formatUtc} from '../../../api/client.ts';
import type {CardView} from '../../../api/types.ts';
import {type DealTab, dealHref} from '../deals.ts';

export interface SealFailedProps {
  account: string;
  /** The card that failed, as the server lists it. */
  card: CardView;
  /** The account's published cards, newest first. */
  listed: readonly CardView[];
  query: string;
  tab: DealTab;
}

// In place of every tab of a card whose bytes do not match its seal: the
// card's ID and publish time from the server's list, and a link to the card
// published before it when there is one. Nothing from the card is shown.
export function SealFailed({account, card, listed, query, tab}: SealFailedProps) {
  const at = listed.findIndex(item => item.id === card.id);
  const before = at >= 0 ? listed[at + 1] : undefined;
  return (
    <div
      className="dc-failed"
      role="alert"
      data-seal="failed"
    >
      <CircleAlert
        className="dc-failed__icon"
        size={20}
        aria-hidden="true"
      />
      <div className="dc-failed__body">
        <p className="dc-failed__title">This card failed its seal check. Nothing from it is shown.</p>
        <p className="dc-failed__card">
          Card {card.id}, published <time dateTime={card.published_at}>{formatUtc(card.published_at)}</time>
        </p>
        {before && (
          <Link
            className="dc-link"
            to={dealHref(account, query, {tab, card: before.id})}
          >
            Open the card before it
          </Link>
        )}
      </div>
    </div>
  );
}
