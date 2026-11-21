import {Cite} from './Cite.tsx';
import {recordsOf} from './card.ts';
import {linesOf, valuesOf} from './lines.ts';
import type {SectionProps} from './sections.ts';

// What the record says: every value line of the card in the card's order,
// its label then the connector's value, with its numbers in the gutter, each
// opening its unit in the proof drawer. A card published before its lines
// were kept lists each record's reference and says where the values are.
export function TheRecord({card}: SectionProps) {
  const lines = linesOf(card);
  const heading = (
    <h2
      id="dc-record-title"
      className="dc-section__title"
    >
      {lines ? 'The record says' : 'The record'}
    </h2>
  );
  if (lines) {
    return (
      <section
        className="dc-section dc-the-record"
        aria-labelledby="dc-record-title"
      >
        {heading}
        <ol className="dc-lines">
          {valuesOf(lines).map((line, at) => (
            <li
              // A card may show one code more than once, so the place keys it.
              // biome-ignore lint/suspicious/noArrayIndexKey: the lines are fixed for a sealed card.
              key={at}
              className="dc-line"
              data-value={line.code}
            >
              <span className="dc-line__n">
                {line.cites.map(n => (
                  <Cite
                    key={n}
                    n={n}
                  />
                ))}
              </span>
              <p className="dc-line__body dc-value">
                <span className="dc-value__label">{line.label}</span>
                <span className="dc-value__text">{line.text}</span>
              </p>
            </li>
          ))}
        </ol>
      </section>
    );
  }
  const records = recordsOf(card);
  return (
    <section
      className="dc-section dc-the-record"
      aria-labelledby="dc-record-title"
    >
      {heading}
      {records.length > 0 && (
        <ol className="dc-lines">
          {records.map(record => (
            <li
              key={record.n}
              className="dc-line"
              data-record-unit={record.n}
            >
              <span className="dc-line__n">
                <Cite n={record.n} />
              </span>
              <div className="dc-line__body">
                <span className="dc-ref">{record.recordRef}</span>
              </div>
            </li>
          ))}
        </ol>
      )}
      <p className="dc-meta">This page shows each record’s reference. Its values are on the plain-text card.</p>
    </section>
  );
}
