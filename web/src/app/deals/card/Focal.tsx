import {Fragment} from 'react';
import {Link, useSearchParams} from 'react-router';
import {Help} from '../../../ui/Help.tsx';
import {dealHref} from '../deals.ts';
import {Cite} from './Cite.tsx';
import {arrOf, type CardLine, countsToWords, dayWord, linesOf, renewalOf} from './lines.ts';
import type {SectionProps} from './sections.ts';

// A line's numbers, each opening its unit in the proof drawer. With help, its
// first number carries the help for what a number is.
function Cites({line, help = false}: {line: CardLine; help?: boolean}) {
  return line.cites.map((n, at) => (
    <Fragment key={n}>
      <Cite n={n} />
      {help && at === 0 && <Help term="citation" />}
    </Fragment>
  ));
}

// The countdown: the server's number of days, the record it counts to and
// that record's date, and the card's own date. Nothing is counted here: the
// number is the server's, and a passed date reads as days ago.
function Countdown({asOf, line, help}: {asOf: string | null; line: CardLine | null; help: boolean}) {
  const countdown = line?.countdown ?? null;
  const asOfLine = (
    <p className="dc-focal__sub">
      <span>as of {asOf === null ? 'a date the server did not send' : <time dateTime={asOf}>{asOf}</time>}</span>
      <Help term="countdown" />
    </p>
  );
  if (!line || !countdown) {
    return (
      <div
        className="dc-focal__item"
        data-focal="countdown"
        data-missing=""
      >
        <p className="dc-focal__none">
          <span
            className="dc-ring dc-focal__ring"
            aria-hidden="true"
          />
          <span>No renewal date</span>
          {line && (
            <Cites
              line={line}
              help={help}
            />
          )}
        </p>
        <p className="dc-focal__sub">
          {line ? <span className="dc-focal__words">{line.text}</span> : <span>The record has no end date.</span>}
          <span>Nothing is inferred from the close date.</span>
        </p>
        {asOfLine}
      </div>
    );
  }
  const passed = countdown.days < 0;
  return (
    <div
      className="dc-focal__item"
      data-focal="countdown"
    >
      <p className="dc-focal__figure">
        <span
          className="dc-focal__number"
          data-days={countdown.days}
        >
          {String(Math.abs(countdown.days))}
        </span>
        <span className="dc-focal__unit">{passed ? `${dayWord(countdown.days)} ago` : dayWord(countdown.days)}</span>
        <Cites
          line={line}
          help={help}
        />
      </p>
      <p className="dc-focal__sub">
        <span>
          {passed ? 'since ' : 'to '}
          {countsToWords[countdown.countsTo] ?? countdown.countsTo}, <time dateTime={countdown.date}>{countdown.date}</time>
        </span>
      </p>
      {asOfLine}
    </div>
  );
}

// ARR: the connector's own words for the annual recurring revenue line, or a
// plain statement that the card has none. No other figure stands in for it.
function Arr({line, help}: {line: CardLine | null; help: boolean}) {
  if (!line) {
    return (
      <div
        className="dc-focal__item"
        data-focal="arr"
        data-missing=""
      >
        <p className="dc-focal__none">
          <span
            className="dc-ring dc-focal__ring"
            aria-hidden="true"
          />
          <span>ARR is not mapped</span>
        </p>
        <p className="dc-focal__sub">
          <span>No other figure stands in for it.</span>
          <Help term="arr" />
        </p>
      </div>
    );
  }
  return (
    <div
      className="dc-focal__item"
      data-focal="arr"
    >
      <p className="dc-focal__figure">
        <span className="dc-focal__number dc-focal__amount">{line.text}</span>
        <Cites
          line={line}
          help={help}
        />
      </p>
      <p className="dc-focal__sub">
        <span>ARR, from the mapped field</span>
        <Help term="arr" />
      </p>
    </div>
  );
}

// The two numbers a rep checks first, under the title: the countdown, then
// ARR on the same baseline. A card published before its lines were kept says
// where they are instead, with the link to its Text tab.
export function Focal({account, card, query}: SectionProps) {
  const [params] = useSearchParams();
  const lines = linesOf(card);
  if (!lines) {
    return (
      <p
        className="dc-values-note"
        data-focal="none"
      >
        The countdown and ARR are on{' '}
        <Link
          className="dc-values-note__link"
          to={dealHref(account, query, {tab: 'text', card: params.get('card')})}
        >
          the plain-text card
        </Link>
        .
      </p>
    );
  }
  // The help for what a number is sits on the first number box of ARR, the
  // focal group's last, where it never pushes ARR off the countdown's line
  // on a phone; on the countdown's when ARR has no number.
  const renewal = renewalOf(lines);
  const arr = arrOf(lines);
  const onArr = Boolean(arr?.cites.length);
  return (
    <section
      className="dc-focal"
      aria-label="Countdown and ARR"
    >
      <Countdown
        asOf={typeof card.as_of === 'string' ? card.as_of : null}
        line={renewal}
        help={!onArr}
      />
      <Arr
        line={arr}
        help={onArr}
      />
    </section>
  );
}
