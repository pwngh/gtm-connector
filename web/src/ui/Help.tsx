import {Info} from 'lucide-react';
import {useEffect, useId, useLayoutEffect, useRef, useState} from 'react';
import help from '../help/help.json';

// Help on the page: an info button that explains one term, from the fixed
// copy in help/help.json. An entry with no guide is a hint: one sentence that
// is the button's description. An entry with a guide is a help panel with a
// link to the guide it is drawn from. Hover, focus or a tap shows either; a
// press pins it open, so a panel's link can be reached by Tab; Escape or a
// press outside closes it. One is open at a time. Help explains the term; a
// fact about this deal is always on the page beside it.

export type HelpKey = keyof typeof help;

interface Guide {
  label: string;
  path: string;
}

interface Entry {
  title: string;
  text: string;
  guide?: Guide;
}

/** The public repository the guides are read from. */
export const repository = 'https://github.com/pwngh/gtm-connector';

declare const __GTM_REVISION__: string | undefined;

/** The revision the app was built from, set at build time from GTM_REVISION, or the default branch. */
export const revision = typeof __GTM_REVISION__ === 'string' && __GTM_REVISION__ ? __GTM_REVISION__ : 'main';

/** Where a guide path, with its anchor, is read at this build's revision. */
export function guideHref(path: string): string {
  return `${repository}/blob/${revision}/${path}`;
}

/** The entry of a key, with its guide when it has one. */
export function entryOf(term: HelpKey): Entry {
  return help[term] as Entry;
}

// Each open help's close function, so opening one closes the others.
const open = new Set<() => void>();

// At this width and wider the help is placed beside its line; narrower, it opens in the flow.
const placedQuery = '(min-width: 720px)';

// How long the pointer may be off both the button and the help before it hides.
const leaveMs = 300;

// The space kept between the help and its line or the viewport's edge.
const spacing = 4;
const edge = 16;

/** The element that holds the line the help explains. */
function lineOf(wrapper: HTMLElement): HTMLElement {
  return wrapper.parentElement?.closest<HTMLElement>('p, li, h1, h2, h3, h4, div') ?? wrapper;
}

export function Help({term}: {term: HelpKey}) {
  const entry = entryOf(term);
  const guide = entry.guide ?? null;
  const id = useId();
  const [state, setState] = useState<'closed' | 'shown' | 'pinned'>('closed');
  const wrapper = useRef<HTMLSpanElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLSpanElement>(null);
  const shown = state !== 'closed';

  // Opening one closes any other in the same commit, so two are never drawn open together.
  useLayoutEffect(() => {
    if (!shown) {
      return undefined;
    }
    const close = () => setState('closed');
    for (const other of open) {
      other();
    }
    open.add(close);
    // Closing with focus inside the help puts focus back on its button.
    const closeHere = () => {
      if (wrapper.current?.contains(document.activeElement)) {
        button.current?.focus();
      }
      close();
    };
    const outside = (event: PointerEvent) => {
      if (!wrapper.current?.contains(event.target as Node)) {
        closeHere();
      }
    };
    // Escape closes the help before anything else that listens for it, such as a drawer.
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        closeHere();
      }
    };
    document.addEventListener('pointerdown', outside);
    window.addEventListener('keydown', key, true);
    return () => {
      open.delete(close);
      document.removeEventListener('pointerdown', outside);
      window.removeEventListener('keydown', key, true);
    };
  }, [shown]);

  // Hover shows the help, and the pointer may move onto it; leaving both for
  // a moment hides it unless it is pinned or holds focus. Focus moving to
  // another part of the page hides it, pinned or not; the window losing focus,
  // as when the guide opens in a new tab, does not.
  useEffect(() => {
    const element = wrapper.current;
    if (!element) {
      return undefined;
    }
    let timer: ReturnType<typeof setTimeout> | undefined;
    const enter = () => {
      clearTimeout(timer);
      setState(now => (now === 'closed' ? 'shown' : now));
    };
    const leave = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (!element.contains(document.activeElement)) {
          setState(now => (now === 'shown' ? 'closed' : now));
        }
      }, leaveMs);
    };
    const left = (event: FocusEvent) => {
      const to = event.relatedTarget as Node | null;
      if (to && !element.contains(to) && !element.matches(':hover')) {
        setState('closed');
      }
    };
    element.addEventListener('mouseenter', enter);
    element.addEventListener('mouseleave', leave);
    element.addEventListener('focusout', left);
    return () => {
      clearTimeout(timer);
      element.removeEventListener('mouseenter', enter);
      element.removeEventListener('mouseleave', leave);
      element.removeEventListener('focusout', left);
    };
  }, []);

  // At 720 and wider the help is placed under its whole line, or over it when
  // there is no room below, and kept inside the viewport's sides. Narrower it
  // is in the flow under its line, and scrolled into view.
  useLayoutEffect(() => {
    const node = panel.current;
    const holder = wrapper.current;
    if (!shown || !node || !holder) {
      return;
    }
    node.style.removeProperty('top');
    node.style.removeProperty('left');
    delete node.dataset.place;
    if (!window.matchMedia(placedQuery).matches) {
      node.scrollIntoView({block: 'nearest'});
      return;
    }
    const own = holder.getBoundingClientRect();
    const line = lineOf(holder).getBoundingClientRect();
    const box = node.getBoundingClientRect();
    const below = window.innerHeight - line.bottom;
    const above = line.top;
    const over = box.height + spacing > below && above >= box.height + spacing;
    node.dataset.place = over ? 'above' : 'below';
    node.style.top = `${over ? line.top - own.top - box.height - spacing : line.bottom - own.top + spacing}px`;
    const right = own.left + box.width;
    const shift = right > window.innerWidth - edge ? Math.max(edge - own.left, window.innerWidth - edge - right) : 0;
    node.style.left = `${shift}px`;
  }, [shown]);

  const peek = () => setState(now => (now === 'closed' ? 'shown' : now));
  const press = () => setState(now => (now === 'pinned' ? 'closed' : 'pinned'));
  const panelId = `${id}-help`;
  const name = `About ${entry.title}`;

  return (
    <span
      ref={wrapper}
      className="ui-help"
    >
      {guide ? (
        <button
          ref={button}
          type="button"
          id={`${id}-button`}
          className="ui-help__button"
          aria-label={name}
          aria-expanded={shown}
          aria-controls={panelId}
          onFocus={peek}
          onClick={press}
        >
          <Info
            size={16}
            aria-hidden="true"
          />
        </button>
      ) : (
        <button
          ref={button}
          type="button"
          className="ui-help__button"
          aria-label={name}
          aria-describedby={panelId}
          onFocus={peek}
          onClick={press}
        >
          <Info
            size={16}
            aria-hidden="true"
          />
        </button>
      )}
      {guide ? (
        <span
          ref={panel}
          id={panelId}
          className="ui-help__panel"
          role="note"
          aria-labelledby={`${id}-button`}
          hidden={!shown}
          data-help={term}
        >
          <span className="ui-help__text">{entry.text}</span>
          <a
            className="ui-help__guide"
            href={guideHref(guide.path)}
            target="_blank"
            rel="noopener noreferrer"
          >
            {`Guide: ${guide.label}`}
          </a>
        </span>
      ) : (
        <span
          ref={panel}
          id={panelId}
          className="ui-help__panel"
          role="tooltip"
          hidden={!shown}
          data-help={term}
        >
          <span className="ui-help__text">{entry.text}</span>
        </span>
      )}
    </span>
  );
}
