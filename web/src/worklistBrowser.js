export function focusWorklistElement(element) {
  element?.focus({preventScroll: true});
  element?.scrollIntoView({block: 'nearest', behavior: 'instant'});
}

export function moveWorklistFocus(event, root) {
  if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
  const entries = [...(root?.querySelectorAll('[data-row-entry]') || [])];
  const index = entries.indexOf(event.target);
  if (index < 0) return;
  event.preventDefault();
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? entries.length - 1
    : Math.max(0, Math.min(entries.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)));
  focusWorklistElement(entries[next]);
}

export function activateWorklistRow(event) {
  if (event.defaultPrevented || event.button !== 0 || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
  if (event.target.closest('a,button,input,select,summary')) return;
  event.currentTarget.querySelector('[data-row-entry]')?.click();
}

export function restoreWorklistFocus(root, key, fallbackKey, fallback) {
  const candidates = [...(root?.querySelectorAll('[data-worklist-focus]') || [])];
  const target = candidates.find(element => element.dataset.worklistFocus === key)
    || candidates.find(element => element.dataset.worklistFocus === fallbackKey) || fallback;
  focusWorklistElement(target);
}
