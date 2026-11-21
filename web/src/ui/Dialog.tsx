import {type ReactNode, type RefObject, useId, useLayoutEffect, useRef} from 'react';
import './dialog.css';

export interface DialogProps {
  title: string;
  children: ReactNode;
  onClose: () => void;
  /** dialog sits in the middle, drawer at the right edge, sheet fills the screen. */
  variant?: 'dialog' | 'drawer' | 'sheet';
  /** Where focus goes back to on close; without it, what had focus on open. */
  returnFocus?: RefObject<HTMLElement | null>;
  /** Focus the first text input on open, for a dialog whose input is the action. */
  focusInput?: boolean;
  /** Focus the title on open, and again whenever the title changes. */
  focusTitle?: boolean;
  /**
   * False leaves the page beside the panel in use: no backdrop, no focus
   * trap, and the page still scrolls. Escape and Close still close it.
   */
  modal?: boolean;
}

const focusable = 'button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),a[href],summary,[tabindex="0"]';

function shown(element: Element | null | undefined): element is HTMLElement {
  return (
    element instanceof HTMLElement &&
    element.isConnected &&
    !element.closest('[inert]') &&
    element.getClientRects().length > 0 &&
    getComputedStyle(element).visibility !== 'hidden'
  );
}

// A modal dialog: focus moves in and stays in, Escape and the backdrop close
// it, the page behind does not scroll, and focus goes back on close. A
// dialog that is not modal keeps only Escape, Close and the focus return.
export function Dialog({title, children, onClose, variant = 'dialog', returnFocus, focusInput = false, focusTitle = false, modal = true}: DialogProps) {
  const panel = useRef<HTMLElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  const titleId = useId();
  // biome-ignore lint/correctness/useExhaustiveDependencies: set up once for the life of the dialog.
  useLayoutEffect(() => {
    const node = panel.current;
    if (!node) {
      return undefined;
    }
    const opened = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const input = focusInput ? node.querySelector('input') : null;
    (input ?? (focusTitle ? heading.current : null) ?? node).focus({preventScroll: true});
    const overflow = document.body.style.overflow;
    if (modal) {
      document.body.style.overflow = 'hidden';
    }
    const handle = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        close.current();
        return;
      }
      if (event.key !== 'Tab' || !modal) {
        return;
      }
      const nodes = [...node.querySelectorAll<HTMLElement>(focusable)].filter(item => item.tabIndex >= 0 && shown(item));
      const first = nodes[0];
      const last = nodes.at(-1);
      const active = document.activeElement;
      if (!first || !last) {
        event.preventDefault();
        node.focus({preventScroll: true});
      } else if (!(active instanceof HTMLElement) || !nodes.includes(active)) {
        // Focus on something the list does not hold, such as a scrolling box
        // Firefox lets Tab stop on, moves to the next control after it.
        event.preventDefault();
        const inside = active instanceof HTMLElement && node.contains(active);
        const after = inside ? nodes.filter(item => active.compareDocumentPosition(item) & Node.DOCUMENT_POSITION_FOLLOWING) : [];
        const before = inside ? nodes.filter(item => active.compareDocumentPosition(item) & Node.DOCUMENT_POSITION_PRECEDING) : [];
        (event.shiftKey ? (before.at(-1) ?? last) : (after[0] ?? first)).focus();
      } else if (event.shiftKey && active === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handle);
    return () => {
      document.removeEventListener('keydown', handle);
      if (modal) {
        document.body.style.overflow = overflow;
      }
      // Read on close, so a caller can name the element that opened it last.
      const previous = returnFocus?.current ?? opened;
      requestAnimationFrame(() => {
        // A replacement dialog, or a control that took focus since, keeps it.
        if (document.querySelector('[role="dialog"][aria-modal="true"]')) {
          return;
        }
        const active = document.activeElement;
        if (active && active !== document.body && active !== previous && active.isConnected) {
          return;
        }
        if (shown(previous)) {
          previous.focus({preventScroll: true});
          return;
        }
        const main = document.querySelector<HTMLElement>('main h1') ?? document.querySelector<HTMLElement>('main');
        if (main) {
          main.tabIndex = -1;
          main.focus({preventScroll: true});
        }
      });
    };
  }, []);
  // A new title means new content: with focusTitle, focus moves to the title again.
  const titled = useRef(title);
  useLayoutEffect(() => {
    if (titled.current === title) {
      return;
    }
    titled.current = title;
    if (focusTitle) {
      heading.current?.focus({preventScroll: true});
    }
  }, [title, focusTitle]);
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: a press on the backdrop closes the dialog; Escape and Close do the same from the keyboard.
    <div
      className={`ui-dialog-backdrop ui-dialog-backdrop--${variant}${modal ? '' : ' ui-dialog-backdrop--modeless'}`}
      onMouseDown={event => {
        if (modal && event.target === event.currentTarget) {
          close.current();
        }
      }}
    >
      <section
        ref={panel}
        role="dialog"
        aria-modal={modal}
        aria-labelledby={titleId}
        tabIndex={-1}
        className={`ui-dialog ui-dialog--${variant}`}
      >
        <div className="ui-dialog__head">
          <h2
            ref={heading}
            id={titleId}
            className="ui-dialog__title"
            tabIndex={focusTitle ? -1 : undefined}
          >
            {title}
          </h2>
          <button
            type="button"
            className="ui-dialog__close"
            aria-label="Close"
            onClick={() => close.current()}
          >
            {/* The cross is drawn here, not imported from lucide-react: with that import, a page chunk that uses this dialog makes the build move the shell's shared modules and styles into a second chunk. */}
            <svg
              width="20"
              height="20"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              aria-hidden="true"
            >
              <path d="M18 6 6 18M6 6l12 12" />
            </svg>
          </button>
        </div>
        <div className="ui-dialog__body">{children}</div>
      </section>
    </div>
  );
}
