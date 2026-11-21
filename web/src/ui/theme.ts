import {useCallback, useEffect, useState} from 'react';

// The theme follows the system until the person chooses one. The choice is
// the one thing the app keeps in browser storage, under themeKey, and the
// inline script in index.html reads the same key before the first paint.

export type Theme = 'light' | 'dark';

export const themeKey = 'gtm-connector-theme';
const darkQuery = '(prefers-color-scheme: dark)';
const themeColors: Record<Theme, string> = {light: '#f5f6f8', dark: '#0f141a'};

// Browser storage for display choices only. It answers null, and stores
// nothing, when storage is blocked or missing.
export const displayStorage = {
  get(key: string): string | null {
    try {
      return globalThis.localStorage?.getItem(key) ?? null;
    } catch {
      return null;
    }
  },
  set(key: string, value: string): void {
    try {
      globalThis.localStorage?.setItem(key, value);
    } catch {
      // Blocked storage keeps the choice for this page only.
    }
  },
};

export function chosenTheme(): Theme | null {
  const stored = displayStorage.get(themeKey);
  return stored === 'light' || stored === 'dark' ? stored : null;
}

export function systemTheme(): Theme {
  return globalThis.matchMedia?.(darkQuery).matches ? 'dark' : 'light';
}

function apply(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', themeColors[theme]);
}

/** The theme in use and a way to choose one. */
export function useTheme(): [Theme, (theme: Theme) => void] {
  const [chosen, setChosen] = useState<Theme | null>(() => (typeof window === 'undefined' ? null : chosenTheme()));
  const [system, setSystem] = useState<Theme>(() => (typeof window === 'undefined' ? 'light' : systemTheme()));
  const theme = chosen ?? system;
  useEffect(() => {
    const media = window.matchMedia(darkQuery);
    const follow = () => setSystem(media.matches ? 'dark' : 'light');
    media.addEventListener('change', follow);
    return () => media.removeEventListener('change', follow);
  }, []);
  useEffect(() => apply(theme), [theme]);
  const choose = useCallback((next: Theme) => {
    displayStorage.set(themeKey, next);
    setChosen(next);
  }, []);
  return [theme, choose];
}
