import type {CardDetail} from '../../../api/types.ts';
import {notShown, onCard, plural, type Sentence, sentencesOf, usableTime} from './card.ts';

// The Communications tab's own logic, kept apart from the tab so it can be
// tested without a browser. Everything here is read from the card's sentence
// units; nothing reads the card's text.

/** One call or message: the sentence units of one record_ref, in unit order. */
export interface Talk {
  /** The record_ref its units carry, or the empty string when they carry none. */
  ref: string;
  /** The source of its first unit, as recorded. */
  source: string | null;
  /** Its earliest usable time as written, or, when it has none, the first unit's time as written. */
  at: string | null;
  /** Its earliest usable time, or null. */
  time: number | null;
  sentences: Sentence[];
}

/**
 * The card's sentence units, one group per record_ref. The groups with a
 * usable time come oldest first, by the earliest time among their units, and
 * equal times keep the card's order; the groups with none follow in the
 * card's order.
 */
export function talksOf(card: CardDetail): {timed: Talk[]; untimed: Talk[]} {
  const byRef = new Map<string, Sentence[]>();
  for (const sentence of sentencesOf(card)) {
    const ref = typeof sentence.recordRef === 'string' ? sentence.recordRef : '';
    byRef.set(ref, [...(byRef.get(ref) ?? []), sentence]);
  }
  const talks = [...byRef].map(([ref, list]): Talk => {
    const sentences = [...list].sort((a, b) => a.n - b.n);
    let earliest: Sentence | null = null;
    for (const sentence of sentences) {
      const time = usableTime(sentence.at);
      if (time !== null && (earliest === null || time < (usableTime(earliest.at) as number))) {
        earliest = sentence;
      }
    }
    const first = sentences[0] as Sentence;
    return {ref, source: first.source, at: (earliest ?? first).at, time: earliest ? usableTime(earliest.at) : null, sentences};
  });
  return {
    timed: talks.filter(talk => talk.time !== null).sort((a, b) => (a.time as number) - (b.time as number)),
    untimed: talks.filter(talk => talk.time === null),
  };
}

/** What a group is, in a word or two: a call, a Slack message, or its source as recorded. */
export function talkWord(source: string | null): string {
  if (source === 'calls') {
    return 'Call';
  }
  if (source === 'slack') {
    return 'Slack message';
  }
  return source ?? 'Source not recorded';
}

/** Why the card does not show a sentence, in the card page's words, or null for one it shows. */
export const reasonOf = (sentence: Sentence): string | null => (onCard(sentence) ? null : (notShown([sentence])[0]?.label ?? null));

/** The line at the top: how many calls, messages and sentences were read, and how many sentences the card leaves out. */
export function countsOf(talks: readonly Talk[]): string {
  const calls = talks.filter(talk => talk.source === 'calls').length;
  const messages = talks.filter(talk => talk.source === 'slack').length;
  const other = talks.length - calls - messages;
  const sentences = talks.flatMap(talk => talk.sentences);
  const left = sentences.filter(sentence => !onCard(sentence)).length;
  const parts = [plural(calls, 'call', 'calls'), plural(messages, 'Slack message', 'Slack messages')];
  if (other) {
    parts.push(plural(other, 'other record', 'other records'));
  }
  const read = `${parts.join(', ')} and ${plural(sentences.length, 'sentence', 'sentences')} were read.`;
  return left ? `${read} The card does not show ${left} of the sentences.` : `${read} The card shows every sentence.`;
}
