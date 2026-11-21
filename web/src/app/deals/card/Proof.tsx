import {type ReactNode, useEffect, useMemo, useRef, useState} from 'react';
import {Link, useLocation, useNavigate} from 'react-router';
import {formatUtc, unitFacts} from '../../../api/client.ts';
import {type Read, useRead} from '../../../api/reads.ts';
import type {CardDetail, UnitView} from '../../../api/types.ts';
import {Dialog} from '../../../ui/Dialog.tsx';
import {Help} from '../../../ui/Help.tsx';
import {Placeholder} from '../../../ui/Placeholder.tsx';
import {Refusal} from '../../../ui/Refusal.tsx';
import {displayStorage} from '../../../ui/theme.ts';
import {activityHref} from '../../ops/activity.ts';
import {useMedia, wideQuery} from '../deals.ts';
import {roleOf, type Sentence, sourceWord, usableTime} from './card.ts';
import {type Asked, answers, around, askedUnit, firstLine, placeOf, proofSeenKey, readFrom, stepFrom, unitOf, unitParam, unitSearch} from './proof.ts';
import {Words} from './Sentence.tsx';
import type {SectionProps} from './sections.ts';

// The proof drawer: the one record or the one whole sentence behind a number
// on the card, and how it was read. ?unit=5 in the address opens it, so a
// link opens the same proof. Until a drawer has been opened in this browser,
// one line above the ledger says what a number does.

const phoneQuery = '(max-width: 719.98px)';

/** A sentence unit in full: the whole sentence as a quotation, then who said it, when and where, as recorded. */
function SentenceProof({unit}: {unit: UnitView}) {
  const sentence = unitFacts(unit) as Sentence;
  const time = usableTime(sentence.at);
  return (
    <>
      <Words sentence={sentence} />
      <p className="dc-attr">
        <span className="dc-attr__part dc-attr__speaker">{sentence.speaker || 'No speaker label'}</span>
        <span className="dc-attr__part">{roleOf(sentence)}</span>
        <span className="dc-attr__part">
          {time === null ? formatUtc(sentence.at) : <time dateTime={sentence.at ?? undefined}>{formatUtc(sentence.at)}</time>}
        </span>
        <span className="dc-attr__part">{sourceWord(sentence.source)}</span>
      </p>
    </>
  );
}

/** A record unit: its reference, and that its fields are not served yet. */
function RecordProof({unit}: {unit: UnitView}) {
  return (
    <div className="dc-proof__record">
      <p className="dc-proof__label">Record</p>
      <p className="dc-ref">{unit.record_ref || 'Reference not recorded'}</p>
      <p className="dc-meta">This server returns a record’s reference, not its fields yet.</p>
    </div>
  );
}

/**
 * How the unit was read: the start of its read and the source, with the
 * receipt's help, or the refusal of the read alone; then a link to the
 * receipt whole in Ops.
 */
function ReadLine({card, unit, refused}: {card: string; unit: UnitView; refused: boolean}) {
  const receipt = refused ? null : unit.receipt;
  let read: ReactNode = 'Not recorded';
  if (refused) {
    read = 'Not permitted';
  } else if (receipt) {
    const started = usableTime(receipt.started_at);
    read = (
      <>
        {started === null ? formatUtc(receipt.started_at) : <time dateTime={receipt.started_at}>{formatUtc(receipt.started_at)}</time>}
        {` from ${readFrom(unitFacts(unit).source)}`}
      </>
    );
  }
  const id = typeof unit.receipt_id === 'string' && unit.receipt_id ? unit.receipt_id : null;
  return (
    <div className="dc-proof__how">
      <p
        className="dc-proof__read"
        data-read={refused ? 'refused' : receipt ? 'read' : 'none'}
      >
        <span className="dc-proof__label">Read</span>
        {receipt && <Help term="ops.receipt" />} <span>{read}</span>
      </p>
      {id && (
        <Link
          className="dc-proof__receipt"
          to={activityHref(card, id)}
        >
          Open the receipt in Ops
        </Link>
      )}
    </div>
  );
}

/** The units of the same record either side of this one, each opening in place. */
function Around({card, n}: {card: CardDetail; n: number}) {
  const {search} = useLocation();
  const {before, after} = around(card, n);
  const near = [
    {label: 'Before', unit: before},
    {label: 'After', unit: after},
  ].filter((item): item is {label: string; unit: UnitView} => item.unit !== null);
  if (!near.length) {
    return null;
  }
  return (
    <ul
      className="dc-proof__around"
      aria-label="Around it"
    >
      {near.map(({label, unit}) => (
        <li
          key={label}
          className="dc-proof__near"
          data-near={label.toLowerCase()}
        >
          <span className="dc-proof__label">{label}</span>
          <Link
            className="dc-proof__step"
            to={{search: unitSearch(search, unit.n)}}
            replace
            preventScrollReset
          >
            <span className="dc-cite__box">{`[${unit.n}]`}</span>
            <span className="dc-proof__line">{firstLine(unit)}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

export interface ProofBodyProps {
  card: CardDetail;
  asked: Asked;
  /** The server's answer for the unit, with its receipt. */
  read: Read<UnitView>;
}

/**
 * The drawer's content for one unit, as the reads stand. A number the card
 * lacks says so and shows nothing else. While the unit is read, it says so.
 * A refused read shows the refusal, except that a read refused as not
 * permitted for a unit the card holds shows the card's unit with "Not
 * permitted" on the read line alone.
 */
export function ProofBody({card, asked, read}: ProofBodyProps) {
  const held = unitOf(card, asked.n);
  if (!held) {
    return (
      <p
        className="dc-proof__state"
        data-proof="missing"
        role="status"
      >
        {`This card has no unit ${asked.written}. No other unit is shown in its place.`}
      </p>
    );
  }
  if (read.status === 'idle' || read.status === 'loading') {
    return (
      <div data-proof="reading">
        <Placeholder status={`Reading unit ${held.n}`} />
      </div>
    );
  }
  const refused = read.status === 'failed' && read.error.kind === 'not_permitted';
  if (read.status === 'failed' && !refused) {
    return (
      <div data-proof="refused">
        <Refusal
          error={read.error}
          retry={read.retry}
        />
      </div>
    );
  }
  if (read.status === 'ready' && !answers(read.data, card.id, held.n)) {
    return (
      <div data-proof="refused">
        <Refusal error={{kind: 'unexpected'}} />
      </div>
    );
  }
  const unit = read.status === 'ready' ? read.data : held;
  return (
    <div
      className="dc-proof__unit"
      data-proof="unit"
    >
      <section className="dc-proof__thing">{unit.kind === 'sentence' ? <SentenceProof unit={unit} /> : <RecordProof unit={unit} />}</section>
      <p className="dc-proof__place">{placeOf(unit)}</p>
      <Around
        card={card}
        n={held.n}
      />
      <ReadLine
        card={card.id}
        unit={unit}
        refused={refused}
      />
    </div>
  );
}

/** The number on the card that shows unit n on its own line, or the closed disclosure that holds it. */
function cardNumber(n: number | null): HTMLElement | null {
  const cite = n === null ? null : document.querySelector<HTMLElement>(`.dc .dc-line__n > .dc-cite[data-cite="${n}"]`);
  const details = cite?.closest('details');
  return details && !details.open ? details.querySelector('summary') : cite;
}

interface DrawerProps {
  card: CardDetail;
  asked: Asked;
  /** Where focus goes back to on close: the number that opened it. */
  opener: {readonly current: HTMLElement | null};
  go: (n: number | null) => void;
}

function Drawer({card, asked, opener, go}: DrawerProps) {
  const wide = useMedia(wideQuery);
  const phone = useMedia(phoneQuery);
  const held = unitOf(card, asked.n);
  const read = useRead<UnitView>(held ? `unit:${card.id}:${held.n}` : null, (api, signal) => api.unit(card.id, held?.n ?? 0, {signal}));
  // Left and Right move to the previous and next unit of the card.
  useEffect(() => {
    if (!held) {
      return undefined;
    }
    const step = (event: KeyboardEvent) => {
      const by = event.key === 'ArrowLeft' ? -1 : event.key === 'ArrowRight' ? 1 : 0;
      const target = event.target instanceof HTMLElement ? event.target : null;
      if (
        !by ||
        event.defaultPrevented ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        event.shiftKey ||
        target?.closest('input, textarea, select, [contenteditable]')
      ) {
        return;
      }
      const next = stepFrom(card, held.n, by);
      if (next !== null) {
        event.preventDefault();
        go(next);
      }
    };
    document.addEventListener('keydown', step);
    return () => document.removeEventListener('keydown', step);
  }, [card, held, go]);
  return (
    <div className="dc-proof">
      <Dialog
        // At 1100 and wider the card stays in use beside the drawer; narrower, the drawer covers it.
        key={wide ? 'beside' : 'over'}
        title={`Source [${held ? held.n : asked.written}]`}
        variant={phone ? 'sheet' : 'drawer'}
        modal={!wide}
        focusTitle
        returnFocus={opener}
        onClose={() => go(null)}
      >
        <ProofBody
          card={card}
          asked={asked}
          read={read}
        />
      </Dialog>
    </div>
  );
}

export function Proof({card}: SectionProps) {
  const {search} = useLocation();
  const navigate = useNavigate();
  const asked = askedUnit(new URLSearchParams(search).get(unitParam));
  const open = asked !== null;
  const [seen, setSeen] = useState(() => displayStorage.get(proofSeenKey) === 'yes');
  // The number last pressed on the card, and the unit the drawer opened on.
  const pressed = useRef<HTMLElement | null>(null);
  const first = useRef<number | null>(null);
  useEffect(() => {
    const press = (event: MouseEvent) => {
      const cite = event.target instanceof Element ? event.target.closest<HTMLElement>('.dc-cite') : null;
      if (cite && !cite.closest('[role="dialog"]')) {
        pressed.current = cite;
      }
    };
    document.addEventListener('click', press, true);
    return () => document.removeEventListener('click', press, true);
  }, []);
  useEffect(() => {
    if (!open) {
      pressed.current = null;
      first.current = null;
      return;
    }
    first.current ??= asked.n;
    if (!seen) {
      displayStorage.set(proofSeenKey, 'yes');
      setSeen(true);
    }
  }, [open, asked?.n, seen]);
  const opener = useMemo(
    () => ({
      get current() {
        return pressed.current?.isConnected ? pressed.current : cardNumber(first.current);
      },
    }),
    [],
  );
  // Within the drawer a move replaces the address; closing removes ?unit= and keeps the rest.
  const searchRef = useRef(search);
  searchRef.current = search;
  const go = useMemo(() => (n: number | null) => navigate({search: unitSearch(searchRef.current, n)}, {replace: true, preventScrollReset: true}), [navigate]);
  return (
    <>
      {!seen && <p className="dc-proof-hint">Select a number to see its source.</p>}
      {asked && (
        <Drawer
          card={card}
          asked={asked}
          opener={opener}
          go={go}
        />
      )}
    </>
  );
}
