import {formatUtc} from '../../../api/client.ts';
import type {SectionProps} from './sections.ts';

// Which sealed card this is: when it was published and its ID.
export function Seal({card}: SectionProps) {
  return (
    <section
      className="dc-section dc-seal"
      aria-label="Seal"
    >
      <p className="dc-seal__line">
        <span className="dc-seal__part">
          Sealed <time dateTime={card.published_at}>{formatUtc(card.published_at)}</time>
        </span>
        <span className="dc-seal__part">Card {card.id}</span>
      </p>
    </section>
  );
}
