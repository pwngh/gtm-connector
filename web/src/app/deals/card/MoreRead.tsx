import {notShown, sentencesOf} from './card.ts';
import {SentenceLine} from './Sentence.tsx';
import type {SectionProps} from './sections.ts';

// Every sentence that was read and is not on the card, behind one
// disclosure, grouped by why it is not shown and in the card's order. A whole
// sentence is set as a quotation; a cut one is listed as cut.
export function MoreRead({card}: SectionProps) {
  const reasons = notShown(sentencesOf(card));
  if (!reasons.length) {
    return null;
  }
  const count = reasons.reduce((sum, reason) => sum + reason.sentences.length, 0);
  return (
    <details className="dc-more">
      <summary className="dc-more__summary">{`More that was read (${count})`}</summary>
      {reasons.map(reason => (
        <section
          key={reason.shown}
          className="dc-group"
          aria-label={reason.label}
          data-reason={reason.shown}
        >
          <h3 className="dc-group__title">{`${reason.label} (${reason.sentences.length})`}</h3>
          <ol className="dc-lines">
            {reason.sentences.map(sentence => (
              <SentenceLine
                key={sentence.n}
                sentence={sentence}
                mark="more"
              />
            ))}
          </ol>
        </section>
      ))}
    </details>
  );
}
