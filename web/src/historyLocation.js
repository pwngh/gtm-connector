import {historyFixture} from './renewalFixtures.js';

export function historySearch(search, changes) {
  const params = new URLSearchParams(search);
  for (const [key, value] of Object.entries(changes)) {
    params.delete(key);
    if (value != null && value !== '') params.set(key, String(value));
  }
  return params;
}

export function historyHref(search = '', changes = {}, paths = historyFixture.paths) {
  const params = historySearch(search, changes);
  return `${paths.history}${params.size ? `?${params}` : ''}`;
}

export function safeHistoryReturn(search, paths = historyFixture.paths) {
  const value = new URLSearchParams(search).get('historyReturn');
  return value && !value.includes('#') && value.split('?')[0] === paths.history ? value : null;
}
