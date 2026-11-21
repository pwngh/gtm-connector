import type {ReactNode} from 'react';
import {basisCounts, groupOf, groups, newestFirst, onCard, plural, sentencesOf} from './card.ts';
import {SentenceLine} from './Sentence.tsx';
import type {SectionProps} from './sections.ts';

// What people said, by who said it: buyer quotes, then sentences whose
// speaker has no established role, then your side. Only the sentences the
// card shows, each whole, newest first.
export function PeopleSaid({account, card}: SectionProps) {
  const sentences = sentencesOf(card);
  const shown = sentences.filter(onCard);
  let body: ReactNode;
  if (!sentences.length) {
    body = <p className="dc-empty">No sentence was read.</p>;
  } else if (!shown.length) {
    body = <p className="dc-empty">{plural(sentences.length, 'sentence was', 'sentences were')} read and none is shown. Not read says why.</p>;
  } else {
    body = groups.map(group => {
      const list = newestFirst(shown.filter(sentence => groupOf(sentence) === group.id));
      const heading = `dc-group-${group.id}`;
      return (
        <section
          key={group.id}
          className="dc-group"
          aria-labelledby={heading}
          data-group={group.id}
        >
          <h3
            id={heading}
            className="dc-group__title"
          >
            {`${group.title} (${list.length})`}
          </h3>
          {group.id === 'unknown' && list.length > 0 && (
            <p className="dc-group__reasons">
              {basisCounts(list)
                .map(({basis, count}) => `${basis} (${count})`)
                .join(' · ')}
            </p>
          )}
          {list.length ? (
            <ol className="dc-lines">
              {list.map(sentence => (
                <SentenceLine
                  key={sentence.n}
                  account={account}
                  card={card.id}
                  sentence={sentence}
                  mark="quote"
                />
              ))}
            </ol>
          ) : (
            <p className="dc-empty">None is shown.</p>
          )}
        </section>
      );
    });
  }
  return (
    <section
      className="dc-section dc-people"
      aria-labelledby="dc-people-title"
    >
      <h2
        id="dc-people-title"
        className="dc-section__title"
      >
        People said
      </h2>
      {body}
    </section>
  );
}
