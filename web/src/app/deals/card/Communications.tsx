import {formatUtc} from '../../../api/client.ts';
import {Help} from '../../../ui/Help.tsx';
import {Cite} from './Cite.tsx';
import {groupOf, roleWords} from './card.ts';
import {countsOf, reasonOf, type Talk, talksOf, talkWord} from './communications.ts';
import {Proof} from './Proof.tsx';
import {Words} from './Sentence.tsx';
import type {SectionProps} from './sections.ts';

// Everything the card read, call by call and message by message, oldest
// first, including the sentences the card does not show. Each group is one
// record: what it is and its time, then its sentences in unit order, each with
// its number, the speaker and the role, and the reason when the card leaves it
// out. A group with no usable time comes last, under No time recorded. A
// number opens the proof drawer, as on the card. The first sentence of each
// role carries the help for its role word.

function TalkGroup({talk, level, helped}: {talk: Talk; level: 'h3' | 'h4'; helped: ReadonlySet<number>}) {
  const Head = level;
  return (
    <li
      className="dc-talk"
      data-talk={talk.ref}
    >
      <Head className="dc-talk__head">
        <span className="dc-talk__what">{talkWord(talk.source)}</span>
        {talk.time === null ? (
          <span className="dc-talk__time">{formatUtc(talk.at)}</span>
        ) : (
          <time
            className="dc-talk__time"
            dateTime={talk.at ?? undefined}
          >
            {formatUtc(talk.at)}
          </time>
        )}
      </Head>
      <ol className="dc-lines">
        {talk.sentences.map(sentence => {
          const group = groupOf(sentence);
          const reason = reasonOf(sentence);
          return (
            <li
              key={sentence.n}
              className="dc-line"
              data-comm-unit={sentence.n}
            >
              <span className="dc-line__n">
                <Cite n={sentence.n} />
              </span>
              <div className="dc-line__body">
                <Words sentence={sentence} />
                <p className="dc-attr">
                  <span className="dc-attr__part dc-attr__speaker">{sentence.speaker || 'No speaker label'}</span>
                  <span className="dc-attr__part">
                    {roleWords[group]}
                    {group === 'unknown' && ` (${sentence.basis ?? 'basis not recorded'})`}
                    {helped.has(sentence.n) && <Help term={`role.${group}`} />}
                  </span>
                </p>
                {reason && (
                  <p
                    className="dc-meta dc-talk__reason"
                    data-reason={sentence.partial ? 'cut' : (sentence.shown ?? '')}
                  >
                    {reason}
                  </p>
                )}
              </div>
            </li>
          );
        })}
      </ol>
    </li>
  );
}

export function Communications(props: SectionProps) {
  const {timed, untimed} = talksOf(props.card);
  const all = [...timed, ...untimed];
  // The number of the first sentence of each role, in the order the tab lists them.
  const firsts = new Map<string, number>();
  for (const sentence of all.flatMap(talk => talk.sentences)) {
    if (!firsts.has(groupOf(sentence))) {
      firsts.set(groupOf(sentence), sentence.n);
    }
  }
  const helped = new Set(firsts.values());
  return (
    <>
      <Proof {...props} />
      <section
        className="dc-section dc-comms"
        aria-labelledby="dc-comms-title"
      >
        <h2
          id="dc-comms-title"
          className="dc-section__title"
        >
          Communications
        </h2>
        {all.length ? (
          <>
            <p className="dc-meta dc-comms__counts">{countsOf(all)}</p>
            {timed.length > 0 && (
              <ol className="dc-comms__list">
                {timed.map(talk => (
                  <TalkGroup
                    key={talk.ref}
                    talk={talk}
                    level="h3"
                    helped={helped}
                  />
                ))}
              </ol>
            )}
            {untimed.length > 0 && (
              <section
                className="dc-comms__untimed"
                aria-labelledby="dc-comms-untimed"
              >
                <h3
                  id="dc-comms-untimed"
                  className="dc-group__title"
                >
                  No time recorded
                </h3>
                <ol className="dc-comms__list">
                  {untimed.map(talk => (
                    <TalkGroup
                      key={talk.ref}
                      talk={talk}
                      level="h4"
                      helped={helped}
                    />
                  ))}
                </ol>
              </section>
            )}
          </>
        ) : (
          <p className="dc-empty">No sentence was read.</p>
        )}
      </section>
    </>
  );
}
