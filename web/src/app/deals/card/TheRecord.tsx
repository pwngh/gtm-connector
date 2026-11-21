import {Cite} from './Cite.tsx';
import {recordsOf} from './card.ts';
import type {SectionProps} from './sections.ts';

// The record units the card read: each one's number and its reference. The
// values are not shown here.
export function TheRecord({card}: SectionProps) {
  const records = recordsOf(card);
  return (
    <section
      className="dc-section dc-the-record"
      aria-labelledby="dc-record-title"
    >
      <h2
        id="dc-record-title"
        className="dc-section__title"
      >
        The record
      </h2>
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
