import {formatUtc} from '../../../api/client.ts';
import {Cite} from './Cite.tsx';
import {groupOf, roleWords, type Sentence as SentenceUnit, sourceWord, usableTime} from './card.ts';

interface SentenceProps {
  account: string;
  card: string;
  sentence: SentenceUnit;
}

// Who said it, as recorded: the speaker or "No speaker label", the contact's
// number for a customer, the role and, for a role not established, its basis;
// then the source and the time.
export function Attribution({account, card, sentence}: SentenceProps) {
  const group = groupOf(sentence);
  const time = usableTime(sentence.at);
  return (
    <p className="dc-attr">
      <span className="dc-attr__part">
        <span className="dc-attr__speaker">{sentence.speaker || 'No speaker label'}</span>
        {group === 'customer' && sentence.contact !== null && (
          <Cite
            account={account}
            card={card}
            n={sentence.contact}
          />
        )}
      </span>
      <span className="dc-attr__part">
        {roleWords[group]}
        {group === 'unknown' && ` (${sentence.basis ?? 'basis not recorded'})`}
      </span>
      <span className="dc-attr__part">{sourceWord(sentence.source)}</span>
      <span className="dc-attr__part">
        {time === null ? formatUtc(sentence.at) : <time dateTime={sentence.at ?? undefined}>{formatUtc(sentence.at)}</time>}
      </span>
    </p>
  );
}

// One sentence on a ledger line: its number in the gutter, then the sentence
// whole as a quotation and its attribution. A cut unit is never a quotation:
// it is listed in the text face under its label.
export function SentenceLine({account, card, sentence, mark}: SentenceProps & {mark: 'quote' | 'more'}) {
  const whole = !sentence.partial && typeof sentence.text === 'string';
  return (
    <li
      className="dc-line"
      {...(mark === 'quote' ? {'data-quote-unit': sentence.n} : {'data-more-unit': sentence.n})}
    >
      <span className="dc-line__n">
        <Cite
          account={account}
          card={card}
          n={sentence.n}
        />
      </span>
      <div className="dc-line__body">
        {whole ? (
          <blockquote
            className="dc-quote"
            data-unit={sentence.n}
          >
            {sentence.text}
          </blockquote>
        ) : (
          <p className="dc-cut">
            <span className="dc-cut__label">{sentence.partial ? 'Cut, not a whole quotation' : 'Text not recorded'}</span>
            {sentence.partial && sentence.text !== null && <span className="dc-cut__text">{sentence.text}</span>}
          </p>
        )}
        <Attribution
          account={account}
          card={card}
          sentence={sentence}
        />
      </div>
    </li>
  );
}
