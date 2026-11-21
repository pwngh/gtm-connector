import type {ReactNode} from 'react';
import {Link, useLocation, useSearchParams} from 'react-router';
import {type Read, useRead, useServer} from '../../../api/reads.ts';
import type {Sealed} from '../../../api/sealed.ts';
import {checkCard, resolveAccount, resolveCard} from '../../../api/selection.ts';
import type {AccountCards, AccountList, CardDetail, CardView} from '../../../api/types.ts';
import {Placeholder} from '../../../ui/Placeholder.tsx';
import {Refusal} from '../../../ui/Refusal.tsx';
import {type DealTab, dealHref, tabAt} from '../deals.ts';
import {Communications} from './Communications.tsx';
import {DealTabs} from './DealTabs.tsx';
import {PlainText} from './PlainText.tsx';
import {SealFailed} from './SealFailed.tsx';
import {checkSeal} from './sealCheck.ts';
import {type Area, type SectionProps, sections} from './sections.ts';
import {type Named, Title} from './Title.tsx';

// The Deal card of one account: its newest published card, or the one the
// address pins when that card is one of the account's. It is laid out from
// the card's units, gaps and lines: the record on one side, the words on the other,
// a numbered spine between them. Its Communications tab lists every sentence
// it read, and its Text tab shows the plain-text card. A card of another
// account is not read, and a card whose bytes do not match its seal shows
// nothing on any tab.

/** The four reads a deal needs: the account list, the account's cards, the open card and its seal check. */
export interface DealReads {
  accounts: Read<AccountList>;
  cards: Read<AccountCards>;
  card: Read<CardDetail>;
  sealed: Read<Sealed>;
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
// returns must be that card of that account. Its seal is checked once the
// card is read, against the sha256 the card's JSON holds.
function useDealReads(asked: string, pinned: string | null): DealReads {
  const {store, state} = useServer();
  const accounts = useRead<AccountList>('accounts', (api, signal) => api.accounts({signal}));
  const first = dealOf(asked, pinned, accounts, {status: 'idle'});
  const cards = useRead<AccountCards>(first.account ? `cards:${first.account}` : null, (api, signal) => api.accountCards(first.account as string, {signal}));
  const {account, cardId} = dealOf(asked, pinned, accounts, cards);
  const read = useRead<CardDetail>(cardId ? `card:${account}:${cardId}` : null, (api, signal) => api.card(cardId as string, {signal}));
  const card = cardId ? checkCard(read, account, cardId) : read;
  const open = card.status === 'ready' ? {id: card.data.id, sha256: card.data.sha256} : null;
  const sealed = useRead<Sealed>(open ? `sealed:${account}:${open.id}:${open.sha256}` : null, (api, signal) =>
    checkSeal(store, state.generation, api, open as {id: string; sha256: string}, signal),
  );
  return {accounts, cards, card, sealed};
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
  /** The tab the address opens. */
  tab?: DealTab;
  reads: DealReads;
}

/** A deal as its reads stand: the title, then the open tab, a refusal, or what is being read. */
export function DealView({asked, pinned, query, tab = 'card', reads}: DealViewProps) {
  const {cards, card, sealed} = reads;
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
  } else if (sealed.status === 'failed') {
    body = (
      <Refusal
        error={sealed.error}
        retry={sealed.retry}
      />
    );
  } else if (sealed.status !== 'ready') {
    body = <Placeholder status="Reading the card" />;
  } else if (sealed.data.outcome !== 'verified') {
    body = (
      <SealFailed
        account={account}
        card={card.data}
        listed={listed}
        query={query}
        tab={tab}
      />
    );
  } else if (tab === 'communications') {
    shown = true;
    body = (
      <Communications
        account={account}
        card={card.data}
        listed={listed}
        query={query}
      />
    );
  } else if (tab === 'text') {
    shown = true;
    body = (
      <PlainText
        key={card.data.id}
        text={sealed.data.text}
      />
    );
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
      <Title named={named} />
      {shown && account && (
        <DealTabs
          account={account}
          pinned={pinned}
          query={query}
          open={tab}
        />
      )}
      {body}
    </article>
  );
}

export default function DealCard({account}: {account: string}) {
  const [params] = useSearchParams();
  const {pathname} = useLocation();
  const pinned = params.get('card');
  return (
    <DealView
      asked={account}
      pinned={pinned}
      query={params.get('q') ?? ''}
      tab={tabAt(pathname)}
      reads={useDealReads(account, pinned)}
    />
  );
}
