import type {ReactNode} from 'react';
import {Help} from '../../../ui/Help.tsx';
import {basisCounts, groupOf, groups, newestFirst, onCard, plural, sentencesOf} from './card.ts';
import {linesOf, titlesOf} from './lines.ts';
import {SentenceLine} from './Sentence.tsx';
import type {SectionProps} from './sections.ts';

// What people said, by who said it: buyer quotes, then sentences whose
// speaker has no established role, then your side, then other organisations
// when the card shows a sentence of one. Only the sentences the
// card shows, each whole, newest first. A buyer quote carries the title from
// the line of the contact its speaker matched. The first line of each group
// carries the help for its role word.
export function PeopleSaid({card}: SectionProps) {
  const sentences = sentencesOf(card);
  const shown = sentences.filter(onCard);
  const titles = titlesOf(linesOf(card));
  let body: ReactNode;
  if (!sentences.length) {
    body = <p className="dc-empty">No sentence was read.</p>;
  } else if (!shown.length) {
    body = <p className="dc-empty">{plural(sentences.length, 'sentence was', 'sentences were')} read and none is shown. Not read says why.</p>;
  } else {
    body = groups.map(group => {
      const list = newestFirst(shown.filter(sentence => groupOf(sentence) === group.id));
      if (!group.always && !list.length) {
        return null;
      }
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
              {list.map((sentence, at) => (
                <SentenceLine
                  key={sentence.n}
                  sentence={sentence}
                  help={at === 0}
                  title={group.id === 'customer' && sentence.contact !== null ? (titles.get(sentence.contact) ?? null) : null}
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
      <div className="ui-help-head">
        <h2
          id="dc-people-title"
          className="dc-section__title"
        >
          People said
        </h2>
        <Help term="quotation" />
      </div>
      {body}
    </section>
  );
}
