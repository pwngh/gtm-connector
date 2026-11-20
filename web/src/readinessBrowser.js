export function focusReadinessElement(element) {
  element?.focus({preventScroll: true});
  element?.scrollIntoView({block: 'nearest', behavior: 'instant'});
}

export function readinessFocusTarget(root, key) {
  return [...(root?.querySelectorAll('[data-readiness-target]') || [])].find(element => element.dataset.readinessTarget === key);
}

export function restoreReadinessFocus(root, opener, fallback, heading) {
  const candidates = [...(root?.querySelectorAll('[data-readiness-focus]') || [])];
  focusReadinessElement(candidates.find(element => element.dataset.readinessFocus === opener)
    || candidates.find(element => element.dataset.readinessFocus === fallback) || heading);
}

export function moveReadinessFocus(event, root) {
  if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || !['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return;
  const entries = [...(root?.querySelectorAll('[data-readiness-entry]') || [])];
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
