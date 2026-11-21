import type {ReactNode} from 'react';
import {Link, useSearchParams} from 'react-router';
import {type Read, useRead} from '../../../api/reads.ts';
import {checkCard, resolveAccount, resolveCard} from '../../../api/selection.ts';
import type {AccountCards, AccountList, CardDetail, CardView} from '../../../api/types.ts';
import {Placeholder} from '../../../ui/Placeholder.tsx';
import {Refusal} from '../../../ui/Refusal.tsx';
import {dealHref} from '../deals.ts';
import {type Area, type SectionProps, sections} from './sections.ts';
import {type Named, Title} from './Title.tsx';

// The Deal card of one account: its newest published card, or the one the
// address pins when that card is one of the account's. It is laid out from
// the card's units and gaps: the record on one side, the words on the other,
// a numbered spine between them. A card of another account is not read.

/** Where the countdown and ARR are, until this page shows them. */
export const valuesNote = 'The countdown and ARR are on the plain-text card. This page does not show them yet.';

/** The three reads a deal needs: the account list, the account's cards, and the open card. */
export interface DealReads {
  accounts: Read<AccountList>;
  cards: Read<AccountCards>;
  card: Read<CardDetail>;
}

/** Which account and card the address opens, from what the server has answered so far. */
export function dealOf(asked: string, pinned: string | null, accounts: DealReads['accounts'], cards: DealReads['cards']) {
  const granted = accounts.status === 'ready' && Array.isArray(accounts.data?.accounts) ? accounts.data.accounts : null;
  const {account, verified, invalid, name} = resolveAccount(asked, granted);
  const listed: CardView[] | null = cards.status === 'ready' && Array.isArray(cards.data?.cards) ? cards.data.cards : null;
  const {cardId, mismatch} = resolveCard(pinned, listed);
  const named: Named =
    invalid || !account
      ? {kind: 'invalid'}
      : verified
        ? {kind: 'verified', account, name: name || account}
        : accounts.status === 'loading' || accounts.status === 'idle'
          ? {kind: 'reading'}
          : {kind: 'unverified', account};
  return {account: invalid ? null : account, named, listed, cardId, mismatch};
}

// The reads, each started once what it depends on is known. The card is
// read only when it is one of the account's cards, and the card the server
// returns must be that card of that account.
function useDealReads(asked: string, pinned: string | null): DealReads {
  const accounts = useRead<AccountList>('accounts', (api, signal) => api.accounts({signal}));
  const first = dealOf(asked, pinned, accounts, {status: 'idle'});
  const cards = useRead<AccountCards>(first.account ? `cards:${first.account}` : null, (api, signal) => api.accountCards(first.account as string, {signal}));
  const {account, cardId} = dealOf(asked, pinned, accounts, cards);
  const read = useRead<CardDetail>(cardId ? `card:${account}:${cardId}` : null, (api, signal) => api.card(cardId as string, {signal}));
  return {accounts, cards, card: cardId ? checkCard(read, account, cardId) : read};
}

function Areas({props, area}: {props: SectionProps; area: Area}) {
  return sections
    .filter(section => section.area === area)
    .map(({id, Section}) => (
      <Section
        key={id}
        {...props}
      />
    ));
}

/** The card's sections, laid out for the pane's width. */
export function CardBody(props: SectionProps) {
  return (
    <>
      <Areas
        props={props}
        area="top"
      />
      <div className="dc-ledger">
        <div className="dc-words">
          <Areas
            props={props}
            area="words"
          />
        </div>
        <div className="dc-record">
          <Areas
            props={props}
            area="record"
          />
        </div>
      </div>
      <Areas
        props={props}
        area="bottom"
      />
    </>
  );
}

export interface DealViewProps {
  /** The account ID the address names. */
  asked: string;
  /** The card ID in ?card=, if any. */
  pinned: string | null;
  /** The deal list's filter, kept on links back to this deal. */
  query: string;
  reads: DealReads;
}

/** A deal as its reads stand: the title, then the card, a refusal, or what is being read. */
export function DealView({asked, pinned, query, reads}: DealViewProps) {
  const {cards, card} = reads;
  const {account, named, listed, mismatch} = dealOf(asked, pinned, reads.accounts, cards);
  let body: ReactNode = null;
  let shown = false;
  if (!account) {
    body = null;
  } else if (cards.status === 'failed') {
    body = (
      <Refusal
        error={cards.error}
        retry={cards.retry}
      />
    );
  } else if (cards.status === 'ready' && !listed) {
    body = <Refusal error={{kind: 'unexpected'}} />;
  } else if (!listed || named.kind === 'reading') {
    body = <Placeholder status="Reading the card" />;
  } else if (mismatch) {
    body = (
      <p
        className="dc-notice"
        data-notice="mismatch"
        role="status"
      >
        <span className="dc-notice__text">The card in this link is not one of this deal’s published cards. Nothing from it is read or shown.</span>
        <Link
          className="dc-link"
          to={dealHref(account, query)}
        >
          Open the newest
        </Link>
      </p>
    );
  } else if (!listed.length) {
    body = <p className="dc-empty">No card is published for this deal yet. Nothing about its sources is inferred from that.</p>;
  } else if (card.status === 'failed') {
    body = (
      <Refusal
        error={card.error}
        retry={card.retry}
      />
    );
  } else if (card.status !== 'ready') {
    body = <Placeholder status="Reading the card" />;
  } else {
    shown = true;
    body = (
      <CardBody
        account={account}
        card={card.data}
        listed={listed}
        query={query}
      />
    );
  }
  return (
    <article
      className="dc"
      aria-labelledby="dc-title"
    >
      <Title named={named}>{shown && <p className="dc-values-note">{valuesNote}</p>}</Title>
      {body}
    </article>
  );
}

export default function DealCard({account}: {account: string}) {
  const [params] = useSearchParams();
  const pinned = params.get('card');
  return (
    <DealView
      asked={account}
      pinned={pinned}
      query={params.get('q') ?? ''}
      reads={useDealReads(account, pinned)}
    />
  );
}
