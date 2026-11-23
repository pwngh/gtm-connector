import {ArrowLeft} from 'lucide-react';
import {useState} from 'react';
import {Link} from 'react-router';
import {ApiError, formatUtc} from '../../api/client.ts';
import {type Read, useServer} from '../../api/reads.ts';
import type {MatchList, MatchView} from '../../api/types.ts';
import {Button} from '../../ui/Button.tsx';
import {Refusal} from '../../ui/Refusal.tsx';
import {dealsPath, matchKind, matchReason} from './deals.ts';

// The calls and Slack threads that wait for a person: each fits more than one
// deal of its account, so the connector did not choose, and no card reads it
// until someone does. A row says what it is, when, whose, and why it waits,
// and offers each deal it could join, or to leave it off every card. It shows
// no word of the call or thread: the words appear on the card once it is added.

interface RowState {
  pending: string | null;
  error: ApiError | null;
}

export interface MatchRowsProps {
  matches: readonly MatchView[];
  /** Each row's request in flight and its last refusal, by match ID. */
  rows: Readonly<Record<string, RowState>>;
  decide: (match: MatchView, deal: string | null) => void;
}

/** The waiting matches as rows. Text from the server is rendered as text. */
export function MatchRows({matches, rows, decide}: MatchRowsProps) {
  if (!matches.length) {
    return (
      <p
        className="matches-none"
        role="status"
      >
        Nothing waits. Every call and thread is on a deal, or was left off.
      </p>
    );
  }
  return (
    <ul
      className="matches-rows"
      aria-label="Waiting calls and threads"
    >
      {matches.map(match => {
        const row = rows[match.id];
        const busy = row?.pending != null;
        return (
          <li
            key={match.id}
            className="matches-row"
            data-match={match.id}
          >
            <div className="matches-row__what">
              <strong className="matches-row__kind">{matchKind(match.kind)}</strong>
              <span className="matches-row__account">{match.account_name || match.account}</span>
              {match.at ? (
                <time
                  className="matches-row__time"
                  dateTime={match.at}
                >
                  {formatUtc(match.at)}
                </time>
              ) : (
                <span className="matches-row__time">No time recorded</span>
              )}
            </div>
            <p className="matches-row__why">{matchReason(match.basis)}</p>
            <div className="matches-row__actions">
              {(match.candidates ?? []).map(deal => (
                <Button
                  key={deal}
                  pending={row?.pending === deal}
                  disabled={busy}
                  onClick={() => decide(match, deal)}
                >
                  Add to {deal}
                </Button>
              ))}
              <Button
                variant="quiet"
                pending={row?.pending === ''}
                disabled={busy}
                onClick={() => decide(match, null)}
              >
                Leave off
              </Button>
            </div>
            {row?.error && <Refusal error={row.error} />}
          </li>
        );
      })}
    </ul>
  );
}

interface MatchesProps {
  read: Read<MatchList>;
  /** Called once the server has confirmed a decision, to read the list again. */
  decided: () => void;
  back: boolean;
}

export default function Matches({read, decided, back}: MatchesProps) {
  const {store, state} = useServer();
  const [rows, setRows] = useState<Record<string, RowState>>({});
  const [gone, setGone] = useState<ReadonlySet<string>>(new Set());

  function decide(match: MatchView, deal: string | null) {
    if (rows[match.id]?.pending != null) {
      return;
    }
    setRows(current => ({...current, [match.id]: {pending: deal ?? '', error: null}}));
    store.api
      .decideMatch(state.session?.csrf_token, match.id, deal)
      .then(() => {
        // The server confirmed: the row goes now, and the list is read again.
        setGone(current => new Set(current).add(match.id));
        setRows(current => ({...current, [match.id]: {pending: null, error: null}}));
        decided();
      })
      .catch((error: unknown) => {
        const problem = error instanceof ApiError ? error : new ApiError();
        if (problem.kind === 'signed_out') {
          store.expire();
        }
        setRows(current => ({...current, [match.id]: {pending: null, error: problem}}));
      });
  }

  let body = null;
  if (read.status === 'failed') {
    body = (
      <Refusal
        error={read.error}
        retry={read.retry}
      />
    );
  } else if (read.status === 'ready') {
    body = (
      <MatchRows
        matches={(read.data.matches ?? []).filter(match => !gone.has(match.id))}
        rows={rows}
        decide={decide}
      />
    );
  } else {
    body = (
      <p
        className="matches-none"
        role="status"
      >
        Reading what waits
      </p>
    );
  }
  return (
    <section
      className="matches"
      aria-labelledby="matches-title"
    >
      {back && (
        <Link
          className="deals-back"
          to={dealsPath}
        >
          <ArrowLeft
            size={20}
            aria-hidden="true"
          />
          Deals
        </Link>
      )}
      <h1
        id="matches-title"
        className="matches-title"
      >
        Waiting for your OK
      </h1>
      <p className="matches-lead">
        Each of these calls and Slack threads fits more than one deal of its account. Until someone says which, no card reads it. Add it to the deal it belongs
        to, or leave it off every card.
      </p>
      {body}
    </section>
  );
}
