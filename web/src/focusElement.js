export function visibleElement(element) {
  if (!element || element.isConnected === false || element.disabled || element.closest?.('[hidden],[inert],[aria-hidden="true"]')) return false;
  const closed = element.closest?.('details:not([open])');
  if (closed && !closed.querySelector('summary')?.contains(element)) return false;
  return !element.getClientRects || element.getClientRects().length > 0;
}

export function focusElement(element) {
  if (!visibleElement(element) || !element.focus) return false;
  element.focus({preventScroll: true});
  if (element.ownerDocument && element.ownerDocument.activeElement !== element) return false;
  element.scrollIntoView?.({block: 'nearest', behavior: 'instant'});
  return true;
}

