import {Info} from 'lucide-react';
import {useEffect, useId, useRef, useState} from 'react';
import help from '../help/help.json';

// A help panel: an info button that explains one term, from the fixed copy in
// help/help.json, with a link to the guide it is drawn from. Hover or focus
// shows the panel; a press pins it open so the link can be reached; Escape or
// a press outside closes it and focus stays on the button. One panel is open
// at a time. The panel explains the term; a fact about this deal is always on
// the page beside it.

export type HelpKey = keyof typeof help;

/** Where the guides are read: the public repository's docs. */
export const guideBase = 'https://github.com/pwngh/gtm-connector/blob/main/';

// Each open panel's close function, so opening one closes the others.
const open = new Set<() => void>();

export function Help({term}: {term: HelpKey}) {
  const entry = help[term];
  const id = useId();
  const [state, setState] = useState<'closed' | 'shown' | 'pinned'>('closed');
  const wrapper = useRef<HTMLSpanElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const shown = state !== 'closed';

  useEffect(() => {
    if (!shown) {
      return undefined;
    }
    const close = () => setState('closed');
    for (const other of open) {
      other();
    }
    open.add(close);
    const outside = (event: PointerEvent) => {
      if (!wrapper.current?.contains(event.target as Node)) {
        close();
      }
    };
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        close();
        button.current?.focus();
      }
    };
    document.addEventListener('pointerdown', outside);
    document.addEventListener('keydown', key);
    return () => {
      open.delete(close);
      document.removeEventListener('pointerdown', outside);
      document.removeEventListener('keydown', key);
    };
  }, [shown]);

  const peek = () => setState(now => (now === 'closed' ? 'shown' : now));

  // Hover shows the panel, and the pointer may move onto it; leaving both, or
  // focus leaving both, hides it unless it is pinned.
  useEffect(() => {
    const element = wrapper.current;
    if (!element) {
      return undefined;
    }
    const enter = () => setState(now => (now === 'closed' ? 'shown' : now));
    const unpeek = () => setState(now => (now === 'shown' ? 'closed' : now));
    const left = (event: FocusEvent) => {
      if (!element.contains(event.relatedTarget as Node | null)) {
        unpeek();
      }
    };
    element.addEventListener('mouseenter', enter);
    element.addEventListener('mouseleave', unpeek);
    element.addEventListener('focusout', left);
    return () => {
      element.removeEventListener('mouseenter', enter);
      element.removeEventListener('mouseleave', unpeek);
      element.removeEventListener('focusout', left);
    };
  }, []);

  return (
    <span
      ref={wrapper}
      className="ui-help"
    >
      <button
        ref={button}
        type="button"
        id={`${id}-button`}
        className="ui-help__button"
        aria-label={`About ${entry.term}`}
        aria-expanded={shown}
        aria-controls={`${id}-panel`}
        onFocus={peek}
        onClick={() => setState(now => (now === 'pinned' ? 'closed' : 'pinned'))}
      >
        <Info
          size={16}
          aria-hidden="true"
        />
      </button>
      <span
        id={`${id}-panel`}
        className="ui-help__panel"
        role="note"
        aria-labelledby={`${id}-button`}
        hidden={!shown}
        data-help={term}
      >
        <span className="ui-help__text">{entry.text}</span>
        <a
          className="ui-help__guide"
          href={`${guideBase}${entry.guide.path}#${entry.guide.anchor}`}
          target="_blank"
          rel="noopener noreferrer"
        >
          {`Guide: ${entry.guide.title}`}
        </a>
      </span>
    </span>
  );
}
