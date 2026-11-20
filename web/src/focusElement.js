export function visibleElement(element) {
  if (!element || element.isConnected === false || element.disabled || element.closest?.('[hidden],[inert],[aria-hidden="true"]')) return false;
  const closed = element.closest?.('details:not([open])');
  if (closed && !closed.querySelector('summary')?.contains(element)) return false;
  return !element.getClientRects || element.getClientRects().length > 0;
}

export function focusElement(element, {block = 'nearest'} = {}) {
  if (!visibleElement(element) || !element.focus) return false;
  element.focus({preventScroll: true});
  if (element.ownerDocument && element.ownerDocument.activeElement !== element) return false;
  element.scrollIntoView?.({block, behavior: 'instant'});
  return true;
}

// Focus has no useful place: nothing, the body, a removed element, or a holding spot.
export function focusIsUnplaced(doc, container) {
  const active = doc?.activeElement;
  if (!active || active === doc.body || active.isConnected === false) return true;
  return active === container || active === doc.querySelector('main h1');
}

// The spot that holds focus while content loads: the open dialog or the page heading.
export function focusHoldingSpot(doc, container) {
  const target = container || doc?.querySelector('main h1');
  if (!target) return false;
  if (target !== container) target.tabIndex = -1;
  target.focus({preventScroll: true});
  return true;
}

// Move focus there only if it was lost, for example when its element was removed.
export function holdPendingFocus(doc, container) {
  const active = doc?.activeElement;
  if (active && active !== doc.body && active.isConnected !== false) return false;
  return focusHoldingSpot(doc, container);
}
