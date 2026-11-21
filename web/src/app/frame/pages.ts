import {FileText, GitBranch, type LucideIcon} from 'lucide-react';
import type {ComponentType} from 'react';
import {ApiEvidence, ApiReadiness} from '../../lazyPages.js';
import type {PaletteItem} from '../../ui/Palette.tsx';
import {serverPaths} from '../routes.ts';

// The app's two areas and the pages under each, in the order the frame
// lists them. An area with no page is not shown.

export interface Page {
  id: string;
  label: string;
  path: string;
  sub: string;
  icon: LucideIcon;
  Page: ComponentType;
}

export interface Area {
  id: 'deals' | 'ops';
  label: string;
  path: string;
  pages: Page[];
}

export const areas: Area[] = [
  {id: 'deals', label: 'Deals', path: '/deals', pages: []},
  {
    id: 'ops',
    label: 'Ops',
    path: '/ops',
    pages: [
      {
        id: 'evidence',
        label: 'Evidence',
        path: serverPaths.evidence,
        icon: FileText,
        sub: 'The chosen account’s cards, units, receipts and gaps',
        Page: ApiEvidence,
      },
      {
        id: 'readiness',
        label: 'Readiness',
        path: serverPaths.readiness,
        icon: GitBranch,
        sub: 'The open card’s gaps; the server has no workflow rules yet',
        Page: ApiReadiness,
      },
    ],
  },
];

/** The areas that have a page. */
export const shownAreas = areas.filter(area => area.pages.length > 0);

const allPages = areas.flatMap(area => area.pages);

/** The page the app opens for "/" and for an area's own address. */
export const home = allPages[0]?.path ?? '/';

export const pageAt = (pathname: string): Page | undefined => allPages.find(page => page.path === pathname);

/** The area an address is in, whether or not a page is there. */
export const areaAt = (pathname: string): Area | undefined => areas.find(area => pathname === area.path || pathname.startsWith(`${area.path}/`));

/** The address of an area: its first page. */
export const areaHome = (area: Area): string => area.pages[0]?.path ?? area.path;

// The query a page link keeps: the chosen account and the open card, never the open unit.
export function keptQuery(search: string): string {
  const params = new URLSearchParams(search);
  const keep = new URLSearchParams();
  for (const key of ['account', 'card']) {
    const value = params.get(key);
    if (value) {
      keep.set(key, value);
    }
  }
  const query = keep.toString();
  return query ? `?${query}` : '';
}

// The palette's places: every page, under its area, with the chosen account and open card kept.
export function pageItems(go: (to: string) => void, search: string): PaletteItem[] {
  return shownAreas.flatMap(area =>
    area.pages.map(page => ({
      group: area.label,
      label: page.label,
      sub: page.sub,
      icon: page.icon,
      onSelect: () => go(page.path + keptQuery(search)),
    })),
  );
}
