import {X} from 'lucide-react';
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
// it, the page behind does not scroll, and focus goes back on close.
export function Dialog({title, children, onClose, variant = 'dialog', returnFocus, focusInput = false}: DialogProps) {
  const panel = useRef<HTMLElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  const titleId = useId();
  // biome-ignore lint/correctness/useExhaustiveDependencies: set up once for the life of the dialog.
  useLayoutEffect(() => {
    const node = panel.current;
    if (!node) {
      return undefined;
    }
    const previous = returnFocus?.current ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    const input = focusInput ? node.querySelector('input') : null;
    (input ?? node).focus({preventScroll: true});
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const handle = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        close.current();
        return;
      }
      if (event.key !== 'Tab') {
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
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
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
      document.body.style.overflow = overflow;
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
        const heading = document.querySelector<HTMLElement>('main h1') ?? document.querySelector<HTMLElement>('main');
        if (heading) {
          heading.tabIndex = -1;
          heading.focus({preventScroll: true});
        }
      });
    };
  }, []);
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: a press on the backdrop closes the dialog; Escape and Close do the same from the keyboard.
    <div
      className={`ui-dialog-backdrop ui-dialog-backdrop--${variant}`}
      onMouseDown={event => {
        if (event.target === event.currentTarget) {
          close.current();
        }
      }}
    >
      <section
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={`ui-dialog ui-dialog--${variant}`}
      >
        <div className="ui-dialog__head">
          <h2
            id={titleId}
            className="ui-dialog__title"
          >
            {title}
          </h2>
          <button
            type="button"
            className="ui-dialog__close"
            aria-label="Close"
            onClick={() => close.current()}
          >
            <X
              size={20}
              aria-hidden="true"
            />
          </button>
        </div>
        <div className="ui-dialog__body">{children}</div>
      </section>
    </div>
  );
}
