import {Building2, FileText, List, type LucideIcon} from 'lucide-react';
import type {ComponentType} from 'react';
import type {AccountView} from '../../api/types.ts';
import {ApiEvidence, Deals} from '../../lazyPages.js';
import type {PaletteItem} from '../../ui/Palette.tsx';
import {dealAt, dealHref} from '../deals/deals.ts';
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
  /** The page is also at every address one segment below its own, as /deals/{account}. */
  nested?: boolean;
}

export interface Area {
  id: 'deals' | 'ops';
  label: string;
  path: string;
  /** Whether the frame lists the area's pages under the top bar and in the palette. */
  subnav: boolean;
  pages: Page[];
}

export const areas: Area[] = [
  {
    id: 'deals',
    label: 'Deals',
    path: serverPaths.deals,
    subnav: false,
    pages: [{id: 'deals', label: 'Deals', path: serverPaths.deals, icon: List, sub: 'Every deal the server grants you', Page: Deals, nested: true}],
  },
  {
    id: 'ops',
    label: 'Ops',
    path: '/ops',
    subnav: true,
    pages: [
      {
        id: 'evidence',
        label: 'Evidence',
        path: serverPaths.evidence,
        icon: FileText,
        sub: 'The chosen account’s cards, units, receipts and gaps',
        Page: ApiEvidence,
      },
    ],
  },
];

/** The areas that have a page. */
export const shownAreas = areas.filter(area => area.pages.length > 0);

const allPages = areas.flatMap(area => area.pages);

/** The page the app opens for "/". */
export const home = serverPaths.deals;

export const pageAt = (pathname: string): Page | undefined =>
  allPages.find(
    page => page.path === pathname || (page.nested && pathname.startsWith(`${page.path}/`) && /^[^/]+$/.test(pathname.slice(page.path.length + 1))),
  );

/** The area an address is in, whether or not a page is there. */
export const areaAt = (pathname: string): Area | undefined => areas.find(area => pathname === area.path || pathname.startsWith(`${area.path}/`));

/** The address of an area: its first page. */
export const areaHome = (area: Area): string => area.pages[0]?.path ?? area.path;

const ops = areas.find(area => area.id === 'ops');

/** The page the app opens for /ops: its first page. */
export const opsHome = ops ? areaHome(ops) : '/ops';

// The query an Ops page link keeps: the chosen account and the open card,
// never the open unit. An open deal's address names its account.
export function keptQuery(search: string, pathname = ''): string {
  const params = new URLSearchParams(search);
  const keep = new URLSearchParams();
  const deal = dealAt(pathname);
  if (deal) {
    keep.set('account', deal);
  }
  for (const key of ['account', 'card']) {
    const value = params.get(key);
    if (value && !keep.has(key)) {
      keep.set(key, value);
    }
  }
  const query = keep.toString();
  return query ? `?${query}` : '';
}

// The palette's pages: every listed page, under its area, with the chosen account and open card kept.
export function pageItems(go: (to: string) => void, search: string, pathname = ''): PaletteItem[] {
  return shownAreas
    .filter(area => area.subnav)
    .flatMap(area =>
      area.pages.map(page => ({
        group: area.label,
        label: page.label,
        sub: page.sub,
        icon: page.icon,
        onSelect: () => go(page.path + keptQuery(search, pathname)),
      })),
    );
}

// The palette's deals: each granted account by the name the server sent, in the order given.
export function dealItems(go: (to: string) => void, deals: readonly AccountView[]): PaletteItem[] {
  return deals.map(deal => ({
    key: `deal:${deal.account}`,
    group: 'Deals',
    label: deal.name || deal.account,
    sub: deal.account,
    icon: Building2,
    onSelect: () => go(dealHref(deal.account)),
  }));
}
