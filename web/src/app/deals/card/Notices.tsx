import {Info} from 'lucide-react';
import {Link} from 'react-router';
import {formatUtc} from '../../../api/client.ts';
import {dealHref} from '../deals.ts';
import {pinnedOf} from './card.ts';
import type {SectionProps} from './sections.ts';

// What changes how far to trust the card. In this slice, one: the address
// pins a card that is not the account's newest.
export function Notices({account, card, listed, query}: SectionProps) {
  const {older, newest} = pinnedOf(listed, card.id);
  if (!older || !newest) {
    return null;
  }
  return (
    <section
      className="dc-notices"
      aria-label="Notices"
    >
      <p
        className="dc-notice"
        data-notice="older"
      >
        <Info
          className="dc-notice__icon"
          size={20}
          aria-hidden="true"
        />
        <span className="dc-notice__text">
          You are reading the card of <time dateTime={older.published_at}>{formatUtc(older.published_at)}</time>. A newer card was published{' '}
          <time dateTime={newest.published_at}>{formatUtc(newest.published_at)}</time>.
        </span>
        <Link
          className="dc-link"
          to={dealHref(account, query)}
        >
          Open the newest
        </Link>
      </p>
    </section>
  );
}
