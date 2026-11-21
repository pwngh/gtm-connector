import {type ReactNode, useMemo, useRef, useState} from 'react';
import {Link, useLocation, useNavigate} from 'react-router';
import {ApiError, formatUtc} from '../../api/client.ts';
import {useRead, useServer} from '../../api/reads.ts';
import type {CardDetail, ReceiptView} from '../../api/types.ts';
import {Button} from '../../ui/Button.tsx';
import {Dialog} from '../../ui/Dialog.tsx';
import {Help} from '../../ui/Help.tsx';
import {Placeholder} from '../../ui/Placeholder.tsx';
import {Refusal} from '../../ui/Refusal.tsx';
import {dealHref, useMedia} from '../deals/deals.ts';
import {serverPaths} from '../routes.ts';
import {
  activityHref,
  answers,
  cardParam,
  elapsedOf,
  inert,
  inertJson,
  inOrder,
  type Named,
  receiptParam,
  receiptsOf,
  sealMatches,
  statusOf,
} from './activity.ts';
import {type Pooled, usePooledReads} from './pooledReads.ts';

// Activity: what ran to make one card. The card's seal receipt, then each
// read receipt its units name, each read from GET /api/v1/receipts/{id}. A
// row opens the receipt whole in a drawer; ?receipt= in the address opens the
// same drawer. The seal's drawer checks the card's bytes against the digest
// the seal names.

/** How many receipts are read at once. */
export const receiptsInFlight = 4;

const phoneQuery = '(max-width: 719.98px)';
const unexpected = () => new ApiError({kind: 'unexpected'});
const receiptKey = (id: string) => `receipt:${id}`;

/** A receipt's read as the page shows it: the answer, a refusal, or still reading. */
export type ReceiptRead = Pooled<ReceiptView>;

/** The receipt a read answered, when it answered the one asked for. */
const readyReceipt = (read: ReceiptRead | undefined, id: string): ReceiptView | null => (read?.status === 'ready' && answers(read.data, id) ? read.data : null);

function Ref({children}: {children: ReactNode}) {
  return <span className="ac-ref">{children}</span>;
}

/** A value as inert text in the reference face, or None. */
function Text({value}: {value: unknown}) {
  return value === null || value === undefined ? 'None' : <Ref>{inert(String(value))}</Ref>;
}

/** Every field of a receipt with its label, in the order the server sends them. */
const fields: [keyof ReceiptView, string][] = [
  ['id', 'Receipt'],
  ['operation_id', 'Operation'],
  ['parent_id', 'Parent receipt'],
  ['ordinal', 'Ordinal'],
  ['principal_id', 'Principal'],
  ['token_id', 'Token fingerprint'],
  ['tool', 'Tool'],
  ['scope', 'Scope'],
  ['account', 'Account'],
  ['connection_generation', 'Connection generation'],
  ['arguments', 'Arguments'],
  ['request_id', 'Request ID'],
  ['brief_id', 'Brief'],
  ['status', 'Status'],
  ['http_status', 'HTTP status'],
  ['row_count', 'Row count'],
  ['rows', 'Rows'],
  ['elapsed_ms', 'Elapsed, in milliseconds'],
  ['error', 'Error'],
  ['started_at', 'Started'],
  ['finished_at', 'Finished'],
  ['delivered_at', 'Delivered'],
];

/** One field's value: what a caller supplied as inert ASCII JSON in a block of its own, the rows as a list, the rest as inert text. */
function FieldValue({receipt, field}: {receipt: ReceiptView; field: keyof ReceiptView}) {
  if (field === 'arguments' || field === 'request_id') {
    return <pre className="ac-json">{inertJson(receipt[field])}</pre>;
  }
  if (field === 'rows') {
    const rows = Array.isArray(receipt.rows) ? receipt.rows : null;
    if (!rows) {
      return 'None';
    }
    if (rows.length === 0) {
      return 'No rows';
    }
    return (
      <ol className="ac-rows">
        {rows.map((row, at) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: rows are kept in order, and two may be equal.
          <li key={at}>
            <Text value={row} />
          </li>
        ))}
      </ol>
    );
  }
  return <Text value={receipt[field]} />;
}

/** Every field of a receipt, in the order the server sends them. */
export function ReceiptFields({receipt}: {receipt: ReceiptView}) {
  return (
    <dl className="ac-fields">
      {fields.map(([field, label]) => (
        <div
          key={field}
          className="ac-field"
          data-field={field}
        >
          <dt>
            {field === 'arguments' ? (
              <div className="ui-help-head">
                {label}
                <Help term="receipt.arguments" />
              </div>
            ) : (
              label
            )}
          </dt>
          <dd>
            <FieldValue
              receipt={receipt}
              field={field}
            />
          </dd>
        </div>
      ))}
    </dl>
  );
}

type Check = {status: 'idle'} | {status: 'checking'} | {status: 'done'; match: boolean} | {status: 'failed'; error: ApiError};

const checkWords = {
  match: 'These bytes match the digest the seal names.',
  mismatch: 'These bytes do not match the digest the seal names.',
};

/** Check the seal: the card's stored bytes, hashed in the browser, against the digest the seal receipt's row names. */
export function SealCheck({card, receipt}: {card: CardDetail; receipt: ReceiptView}) {
  const {store} = useServer();
  const [check, setCheck] = useState<Check>({status: 'idle'});
  async function run() {
    if (check.status === 'checking') {
      return;
    }
    setCheck({status: 'checking'});
    try {
      setCheck({status: 'done', match: await sealMatches(store.api, card, receipt)});
    } catch (error) {
      setCheck({status: 'failed', error: error instanceof ApiError ? error : unexpected()});
    }
  }
  const said = check.status === 'done' ? checkWords[check.match ? 'match' : 'mismatch'] : check.status === 'checking' ? 'Checking the seal' : '';
  return (
    <div className="ac-seal">
      <div className="ui-help-head">
        <Button
          pending={check.status === 'checking'}
          onClick={run}
        >
          Check the seal
        </Button>
        <Help term="seal" />
      </div>
      <p
        className="ac-seal__said"
        role="status"
        data-seal-check={check.status === 'done' ? (check.match ? 'match' : 'mismatch') : check.status}
      >
        {said}
      </p>
      {check.status === 'failed' && <Refusal error={check.error} />}
    </div>
  );
}

interface DrawerBodyProps {
  card: CardDetail;
  asked: string;
  named: Named | null;
  read: ReceiptRead | undefined;
}

/** The drawer's content for the receipt asked for, as its read stands. */
export function DrawerBody({card, asked, named, read}: DrawerBodyProps) {
  if (!named) {
    return (
      <p
        className="ac-state"
        data-receipt="missing"
        role="status"
      >
        {`This card names no receipt ${inert(asked)}. No other receipt is shown in its place.`}
      </p>
    );
  }
  if (!read || read.status === 'reading') {
    return (
      <div data-receipt="reading">
        <Placeholder status="Reading the receipt" />
      </div>
    );
  }
  if (read.status === 'failed' || !answers(read.data, named.id)) {
    const error = read.status === 'failed' ? read.error : unexpected();
    return (
      <div
        className="ac-drawer"
        data-receipt="refused"
      >
        <p className="ac-state">
          Receipt <Ref>{inert(named.id)}</Ref>
        </p>
        <Refusal error={error} />
      </div>
    );
  }
  return (
    <div
      className="ac-drawer"
      data-receipt="ready"
    >
      {named.seal && (
        <SealCheck
          card={card}
          receipt={read.data}
        />
      )}
      <ReceiptFields receipt={read.data} />
    </div>
  );
}

function ReceiptRow({card, named, read}: {card: CardDetail; named: Named; read: ReceiptRead | undefined}) {
  const receipt = readyReceipt(read, named.id);
  const failed = read?.status === 'failed' ? read.error : read?.status === 'ready' && !receipt ? unexpected() : null;
  let started: ReactNode = 'Reading';
  if (receipt) {
    started = Number.isFinite(Date.parse(receipt.started_at)) ? (
      <time dateTime={receipt.started_at}>{formatUtc(receipt.started_at)}</time>
    ) : (
      formatUtc(receipt.started_at)
    );
  } else if (failed) {
    started = 'Not read';
  }
  return (
    <tr
      data-receipt={named.id}
      data-seal={named.seal || undefined}
    >
      <th scope="row">
        <span className="ac-label">Started</span>
        <Link
          className="ac-open"
          to={activityHref(card.id, named.id)}
          preventScrollReset
        >
          {started}
          <span className="ac-hidden">{named.seal ? ', open the seal receipt' : ', open the receipt'}</span>
        </Link>
      </th>
      <td>
        <span className="ac-label">Tool</span>
        {named.seal && (
          <>
            <span className="ac-seal-tag">Seal</span>{' '}
          </>
        )}
        {receipt ? <Ref>{inert(String(receipt.tool))}</Ref> : null}
      </td>
      <td>
        <span className="ac-label">Status</span>
        {receipt ? (
          <Ref>{statusOf(receipt)}</Ref>
        ) : failed ? (
          <span
            className="ac-problem"
            data-refusal={failed.kind}
          >
            {failed.kind === 'not_permitted' ? 'Not permitted' : 'Not read'}
            {failed.code && <span className="ac-note">Server code: {failed.code}</span>}
          </span>
        ) : null}
      </td>
      <td className="ac-num">
        <span className="ac-label">Rows</span>
        {receipt ? receipt.row_count : null}
      </td>
      <td className="ac-num">
        <span className="ac-label">Elapsed</span>
        {receipt ? elapsedOf(receipt) : null}
      </td>
    </tr>
  );
}

/** The table of what ran: the seal first, then each read in ordinal order. */
export function ReceiptTable({card, reads}: {card: CardDetail; reads: Map<string, ReceiptRead>}) {
  const rows = inOrder(receiptsOf(card), named => readyReceipt(reads.get(receiptKey(named.id)), named.id));
  if (rows.length === 0) {
    return <p className="ac-state">This card names no receipt.</p>;
  }
  return (
    <table className="ac-table">
      <caption>The card’s seal, then each read it rests on</caption>
      <thead>
        <tr>
          <th scope="col">Started</th>
          <th scope="col">Tool</th>
          <th scope="col">Status</th>
          <th
            scope="col"
            className="ac-num"
          >
            Rows
          </th>
          <th
            scope="col"
            className="ac-num"
          >
            Elapsed
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map(named => (
          <ReceiptRow
            key={named.id}
            card={card}
            named={named}
            read={reads.get(receiptKey(named.id))}
          />
        ))}
      </tbody>
    </table>
  );
}

/** The card the receipts belong to: its ID, account and publish time, and a link to it. */
export function CardHead({card}: {card: CardDetail}) {
  return (
    <div className="ac-card">
      <dl className="ac-card__facts">
        <div>
          <dt>Card</dt>
          <dd>
            <Ref>{card.id}</Ref>
          </dd>
        </div>
        <div>
          <dt>Account</dt>
          <dd>
            <Ref>{card.account}</Ref>
          </dd>
        </div>
        <div>
          <dt>Published</dt>
          <dd>
            {Number.isFinite(Date.parse(card.published_at)) ? (
              <time dateTime={card.published_at}>{formatUtc(card.published_at)}</time>
            ) : (
              formatUtc(card.published_at)
            )}
          </dd>
        </div>
      </dl>
      <Link
        className="ac-link"
        to={dealHref(card.account, '', {card: card.id})}
      >
        Open the card
      </Link>
    </div>
  );
}

function CardActivity({cardId, asked}: {cardId: string; asked: string | null}) {
  const {search} = useLocation();
  const navigate = useNavigate();
  const phone = useMedia(phoneQuery);
  const read = useRead<CardDetail>(`activity:${cardId}`, (api, signal) => api.card(cardId, {signal}));
  const card = read.status === 'ready' && read.data?.id === cardId ? read.data : null;
  const named = card ? receiptsOf(card) : [];
  const reads = usePooledReads<ReceiptView>(
    named.map(item => receiptKey(item.id)),
    key => (api, signal) => api.receipt(key.slice('receipt:'.length), {signal}),
    receiptsInFlight,
  );
  // Focus goes back to the row of the receipt the drawer showed.
  const shownRef = useRef(asked);
  shownRef.current = asked;
  const opener = useMemo(
    () => ({
      get current() {
        const id = shownRef.current;
        return id
          ? ([...document.querySelectorAll<HTMLElement>('.ac-table tbody tr')]
              .find(row => row.dataset.receipt === id)
              ?.querySelector<HTMLElement>('.ac-open') ?? null)
          : null;
      },
    }),
    [],
  );
  const close = () => {
    const params = new URLSearchParams(search);
    params.delete(receiptParam);
    navigate({search: `?${params}`}, {replace: true, preventScrollReset: true});
  };

  let body: ReactNode;
  if (read.status === 'failed') {
    body = (
      <Refusal
        error={read.error}
        retry={read.retry}
      />
    );
  } else if (read.status !== 'ready') {
    body = <Placeholder status="Reading the card" />;
  } else if (!card) {
    body = <Refusal error={unexpected()} />;
  } else {
    body = (
      <>
        <CardHead card={card} />
        <ReceiptTable
          card={card}
          reads={reads}
        />
      </>
    );
  }
  const openNamed = card && asked ? (named.find(item => item.id === asked) ?? null) : null;
  return (
    <>
      {body}
      {card && asked !== null && (
        <Dialog
          key={asked}
          title={openNamed?.seal ? 'Seal receipt' : openNamed ? 'Read receipt' : 'Receipt'}
          variant={phone ? 'sheet' : 'drawer'}
          focusTitle
          returnFocus={opener}
          onClose={close}
        >
          <DrawerBody
            card={card}
            asked={asked}
            named={openNamed}
            read={openNamed ? reads.get(receiptKey(openNamed.id)) : undefined}
          />
        </Dialog>
      )}
    </>
  );
}

export default function Activity() {
  const {search} = useLocation();
  const params = new URLSearchParams(search);
  const cardId = params.get(cardParam) || null;
  const asked = params.get(receiptParam);
  return (
    <section
      className="ac"
      aria-labelledby="ac-title"
    >
      <div className="ui-help-head">
        <h1
          id="ac-title"
          className="ac-title"
        >
          Activity
        </h1>
        <Help term="ops.activity" />
      </div>
      {cardId ? (
        <>
          <p className="ac-meta">What ran to make this card, read from the server as you. Times are UTC.</p>
          <CardActivity
            key={cardId}
            cardId={cardId}
            asked={asked}
          />
        </>
      ) : (
        <div className="ac-choose">
          <p>Choose a deal in Coverage to see what ran for its card.</p>
          <Link
            className="ac-link"
            to={serverPaths.coverage}
          >
            Go to Coverage
          </Link>
        </div>
      )}
    </section>
  );
}
