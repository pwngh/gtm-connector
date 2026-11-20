import {visibleElement as visible} from './focusElement.js';
import {focusReadinessElement, closeReadinessOnEscape} from './readinessBrowser.js';

export const focusHistoryElement = focusReadinessElement;
export const closeHistoryOnEscape = closeReadinessOnEscape;

export function historyFocusTarget(root, key) {
  return [...(root?.querySelectorAll('[data-history-target]') || [])].find(element => element.dataset.historyTarget === key && visible(element));
}

export function restoreHistoryFocus(root, opener, fallback, heading) {
  const candidates = [...(root?.querySelectorAll('[data-history-focus]') || [])];
  for (const candidate of [opener, fallback]) {
    const target = candidates.find(element => element.dataset.historyFocus === candidate);
    if (target && target.isConnected !== false && !target.disabled && !target.closest?.('[hidden],[inert],[aria-hidden="true"]')) {
      const seen = new Set();
      for (let disclosure = target.closest?.('details'); disclosure && !seen.has(disclosure); disclosure = disclosure.parentElement?.closest('details')) {
        seen.add(disclosure);
        if (!disclosure.querySelector('summary')?.contains(target)) disclosure.open = true;
      }
    }
    if (focusHistoryElement(target)) return;
  }
  focusHistoryElement(heading);
}

export function moveHistoryFocus(event, root) {
  if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || !['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return;
  const entries = [...(root?.querySelectorAll('[data-history-entry]') || [])].filter(visible);
  const index = entries.indexOf(event.target);
  if (index < 0) return;
  event.preventDefault();
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? entries.length - 1 : Math.max(0, Math.min(entries.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)));
  focusHistoryElement(entries[next]);
}
