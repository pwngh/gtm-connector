import {Cite} from './Cite.tsx';
import {linesOf, signalsOf} from './lines.ts';
import type {SectionProps} from './sections.ts';

// The card's fixed signals, in the card's order, each in the connector's
// words with its numbers and how many more it counts. A card with none, or a
// card published before its lines were kept, draws no section.
export function Flags({card}: SectionProps) {
  const signals = signalsOf(linesOf(card) ?? []);
  if (!signals.length) {
    return null;
  }
  return (
    <section
      className="dc-section dc-flags"
      aria-labelledby="dc-flags-title"
    >
      <h2
        id="dc-flags-title"
        className="dc-section__title"
      >
        Flags
      </h2>
      <ol className="dc-lines">
        {signals.map((signal, at) => (
          <li
            // A card may raise one code more than once, so the place keys it.
            // biome-ignore lint/suspicious/noArrayIndexKey: the lines are fixed for a sealed card.
            key={at}
            className="dc-flag"
            data-signal={signal.code}
          >
            <span className="dc-flag__text">{signal.text}</span>
            <span className="dc-flag__cites">
              {signal.cites.map(n => (
                <Cite
                  key={n}
                  n={n}
                />
              ))}
              {signal.more > 0 && <span className="dc-flag__more">{`and ${signal.more} more`}</span>}
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}
