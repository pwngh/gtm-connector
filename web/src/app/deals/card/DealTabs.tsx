import {Link} from 'react-router';
import {type DealTab, dealHref} from '../deals.ts';

const tabs: {tab: DealTab; label: string}[] = [
  {tab: 'card', label: 'Card'},
  {tab: 'communications', label: 'Communications'},
  {tab: 'text', label: 'Text'},
];

// The deal's tabs, each its own address: the card, everything it read, and
// the plain-text card.
// A card pinned in the address and the list's filter are kept.
export function DealTabs({account, pinned, query, open}: {account: string; pinned: string | null; query: string; open: DealTab}) {
  return (
    <nav
      className="dc-tabs"
      aria-label="Views of the card"
    >
      {tabs.map(({tab, label}) => (
        <Link
          key={tab}
          className="dc-tab"
          to={dealHref(account, query, {tab, card: pinned})}
          aria-current={tab === open ? 'page' : undefined}
        >
          {label}
        </Link>
      ))}
    </nav>
  );
}
