// Where focus goes when the page changes under it.

/** Whether an element is on screen and can take focus: connected, enabled, not hidden, and not in a closed disclosure. */
export function visibleElement(element: Element | null | undefined): element is HTMLElement {
  if (
    !(element instanceof HTMLElement) ||
    !element.isConnected ||
    (element as HTMLButtonElement).disabled ||
    element.closest('[hidden],[inert],[aria-hidden="true"]')
  ) {
    return false;
  }
  const closed = element.closest('details:not([open])');
  if (closed && !closed.querySelector('summary')?.contains(element)) {
    return false;
  }
  return element.getClientRects().length > 0;
}

/** Focus an element that is on screen and bring it into view; false when it could not take focus. */
export function focusElement(element: Element | null | undefined, {block = 'nearest'}: {block?: ScrollLogicalPosition} = {}): boolean {
  if (!visibleElement(element)) {
    return false;
  }
  element.focus({preventScroll: true});
  if (element.ownerDocument.activeElement !== element) {
    return false;
  }
  element.scrollIntoView({block, behavior: 'instant'});
  return true;
}

/** Focus has no useful place: nothing, the body, a removed element, or a holding spot. */
export function focusIsUnplaced(doc: Document, container: Element | null): boolean {
  const active = doc.activeElement;
  if (!active || active === doc.body || !active.isConnected) {
    return true;
  }
  return active === container || active === doc.querySelector('main h1');
}

/** The spot that holds focus while content loads: the open dialog or the page heading. */
export function focusHoldingSpot(doc: Document, container: Element | null): boolean {
  const target = container instanceof HTMLElement ? container : doc.querySelector<HTMLElement>('main h1');
  if (!target) {
    return false;
  }
  if (target !== container) {
    target.tabIndex = -1;
  }
  target.focus({preventScroll: true});
  return true;
}

/** Moves focus to the holding spot only if it was lost, for example when its element was removed. */
export function holdPendingFocus(doc: Document, container: Element | null = null): boolean {
  const active = doc.activeElement;
  if (active && active !== doc.body && active.isConnected) {
    return false;
  }
  return focusHoldingSpot(doc, container);
}
