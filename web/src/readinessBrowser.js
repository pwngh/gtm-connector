function visible(element) {
  if (!element || element.isConnected === false || element.disabled || element.closest?.('[hidden],[inert],[aria-hidden="true"]')) return false;
  const closed = element.closest?.('details:not([open])');
  if (closed && !closed.querySelector('summary')?.contains(element)) return false;
  return !element.getClientRects || element.getClientRects().length > 0;
}

export function focusReadinessElement(element) {
  if (!visible(element) || !element.focus) return false;
  element.focus({preventScroll: true});
  if (element.ownerDocument && element.ownerDocument.activeElement !== element) return false;
  element.scrollIntoView?.({block: 'nearest', behavior: 'instant'});
  return true;
}

export function readinessFocusTarget(root, key) {
  return [...(root?.querySelectorAll('[data-readiness-target]') || [])].find(element => element.dataset.readinessTarget === key && visible(element));
}

export function restoreReadinessFocus(root, opener, fallback, heading) {
  const candidates = [...(root?.querySelectorAll('[data-readiness-focus]') || [])];
  for (const target of [candidates.find(element => element.dataset.readinessFocus === opener), candidates.find(element => element.dataset.readinessFocus === fallback), heading]) {
    if (focusReadinessElement(target)) return;
  }
}

export function moveReadinessFocus(event, root) {
  if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || !['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return;
  const entries = [...(root?.querySelectorAll('[data-readiness-entry]') || [])].filter(visible);
  const index = entries.indexOf(event.target);
  if (index < 0) return;
  event.preventDefault();
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? entries.length - 1
    : Math.max(0, Math.min(entries.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)));
  focusReadinessElement(entries[next]);
}

export function closeReadinessOnEscape(event, close) {
  if (event.defaultPrevented || event.key !== 'Escape' || event.altKey || event.ctrlKey || event.metaKey
    || event.target?.closest?.('input,textarea,select')) return;
  event.preventDefault();
  event.stopPropagation();
  close();
}
