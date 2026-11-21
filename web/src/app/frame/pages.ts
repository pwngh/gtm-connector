import {Building2, History, List, ListChecks, type LucideIcon} from 'lucide-react';
import type {ComponentType} from 'react';
import type {AccountView} from '../../api/types.ts';
import type {PaletteItem} from '../../ui/Palette.tsx';
import {dealAt, dealHref} from '../deals/deals.ts';
import {Activity, Coverage, Deals} from '../lazyPages.ts';
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
  /** The page is also at the addresses below its own that this accepts, as /deals/{account}. */
  nested?: (pathname: string) => boolean;
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
    pages: [
      {
        id: 'deals',
        label: 'Deals',
        path: serverPaths.deals,
        icon: List,
        sub: 'Every deal the server grants you',
        Page: Deals,
        nested: pathname => dealAt(pathname) !== null,
      },
    ],
  },
  {
    id: 'ops',
    label: 'Ops',
    path: '/ops',
    subnav: true,
    pages: [
      {
        id: 'coverage',
        label: 'Coverage',
        path: serverPaths.coverage,
        icon: ListChecks,
        sub: 'Which deals have a card, and whether the server is healthy',
        Page: Coverage,
      },
      {
        id: 'activity',
        label: 'Activity',
        path: serverPaths.activity,
        icon: History,
        sub: 'What ran to make a card, with each receipt in full',
        Page: Activity,
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
  allPages.find(page => page.path === pathname || (pathname.startsWith(`${page.path}/`) && page.nested?.(pathname) === true));

/** The area an address is in, whether or not a page is there. */
export const areaAt = (pathname: string): Area | undefined => areas.find(area => pathname === area.path || pathname.startsWith(`${area.path}/`));

/** The address of an area: its first page. */
export const areaHome = (area: Area): string => area.pages[0]?.path ?? area.path;

const ops = areas.find(area => area.id === 'ops');

/** The page the app opens for /ops: its first page. */
export const opsHome = ops ? areaHome(ops) : '/ops';

// The query an Ops page link keeps: the open card, never the open receipt or unit.
export function keptQuery(search: string): string {
  const card = new URLSearchParams(search).get('card');
  return card ? `?${new URLSearchParams({card})}` : '';
}

// The palette's pages: every listed page, under its area, with the open card kept.
export function pageItems(go: (to: string) => void, search: string): PaletteItem[] {
  return shownAreas
    .filter(area => area.subnav)
    .flatMap(area =>
      area.pages.map(page => ({
        group: area.label,
        label: page.label,
        sub: page.sub,
        icon: page.icon,
        onSelect: () => go(page.path + keptQuery(search)),
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
