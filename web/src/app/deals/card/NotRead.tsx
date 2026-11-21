import {dispositionWords, orderedGaps} from './card.ts';
import type {SectionProps} from './sections.ts';

// Every gap of the card in the card's own words: a whole source not read
// first, then the rest of what was not read, then what was read and not
// shown, then notes.
export function NotRead({card}: SectionProps) {
  const gaps = orderedGaps(card);
  return (
    <section
      className="dc-section dc-not-read"
      aria-labelledby="dc-not-read-title"
    >
      <h2
        id="dc-not-read-title"
        className="dc-section__title"
      >
        Not read
      </h2>
      {gaps.length ? (
        <ul className="dc-lines">
          {gaps.map((gap, at) => (
            <li
              // A card may state one code more than once, so the place keys it.
              // biome-ignore lint/suspicious/noArrayIndexKey: the gaps are fixed for a sealed card.
              key={at}
              className="dc-line dc-gap"
              data-gap={gap.code}
            >
              <span className="dc-line__n">
                <span
                  className="dc-ring"
                  aria-hidden="true"
                />
              </span>
              <div className="dc-line__body">
                <p className="dc-gap__text">{gap.text}</p>
                <p className="dc-meta">{dispositionWords(gap.disposition)}</p>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <p className="dc-empty">The card states nothing it did not read. That is the card’s statement, not a search of every source.</p>
      )}
    </section>
  );
}
