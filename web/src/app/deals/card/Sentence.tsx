import {formatUtc} from '../../../api/client.ts';
import {Help} from '../../../ui/Help.tsx';
import {Cite} from './Cite.tsx';
import {groupOf, roleOf, type Sentence as SentenceUnit, sourceWord, usableTime} from './card.ts';

interface SentenceProps {
  sentence: SentenceUnit;
  /** The title on the line of the contact the sentence's contact number points to, if the card shows one. */
  title?: string | null;
  /** Whether the role word carries its help. */
  help?: boolean;
}

// Who said it, as recorded: the speaker or "No speaker label", for a customer
// the title from the contact's line and the contact's number, the role with
// how the speaker was matched and, for another party, its domain, and the
// role's help where asked; then the source and the time.
export function Attribution({sentence, title = null, help = false}: SentenceProps) {
  const group = groupOf(sentence);
  const time = usableTime(sentence.at);
  const contact = group === 'customer' && sentence.contact !== null ? <Cite n={sentence.contact} /> : null;
  return (
    <p className="dc-attr">
      <span className="dc-attr__part">
        <span className="dc-attr__speaker">{sentence.speaker || 'No speaker label'}</span>
        {!title && contact}
      </span>
      {contact && title && (
        <span className="dc-attr__part">
          <span className="dc-attr__title">{title}</span>
          {contact}
        </span>
      )}
      {sentence.quoted && <span className="dc-attr__part">Quoted in the author's message</span>}
      <span className="dc-attr__part">
        {roleOf(sentence)}
        {help && <Help term={`role.${group}`} />}
      </span>
      <span className="dc-attr__part">{sourceWord(sentence.source)}</span>
      <span className="dc-attr__part">
        {time === null ? formatUtc(sentence.at) : <time dateTime={sentence.at ?? undefined}>{formatUtc(sentence.at)}</time>}
      </span>
    </p>
  );
}

// The sentence itself: whole, as a quotation of its text and nothing else. A
// cut unit is never a quotation: it is listed in the text face under its label.
export function Words({sentence}: {sentence: SentenceUnit}) {
  if (!sentence.partial && typeof sentence.text === 'string') {
    return (
      <blockquote
        className="dc-quote"
        data-unit={sentence.n}
      >
        {sentence.text}
      </blockquote>
    );
  }
  return (
    <p className="dc-cut">
      <span className="dc-cut__label">{sentence.partial ? 'Cut, not a whole quotation' : 'Text not recorded'}</span>
      {sentence.partial && sentence.text !== null && <span className="dc-cut__text">{sentence.text}</span>}
    </p>
  );
}

// One sentence on a ledger line: its number in the gutter, then the sentence
// and its attribution.
export function SentenceLine({sentence, title = null, help = false, mark}: SentenceProps & {mark: 'quote' | 'more'}) {
  return (
    <li
      className="dc-line"
      {...(mark === 'quote' ? {'data-quote-unit': sentence.n} : {'data-more-unit': sentence.n})}
    >
      <span className="dc-line__n">
        <Cite n={sentence.n} />
      </span>
      <div className="dc-line__body">
        <Words sentence={sentence} />
        <Attribution
          sentence={sentence}
          title={title}
          help={help}
        />
      </div>
    </li>
  );
}
