# Third-party component provenance

## Planes — free Segmented Control

- Author/publisher: Ashish Gogula, Planes.
- Component page: <https://useplanes.com/components/segmented-control>
- Official public registry source: <https://useplanes.com/r/segmented-control.json>
- Retrieved: 2026-11-19 (America/Chicago), using the public URL without an account or license key.
- Registry response SHA-256: `a8a1d5c8504d48100547f469284b6f52750f75acb2629e878e0aff66885df1ae`.
- Source member: `components/interactions/SegmentedControl.tsx`.
- Local adaptation: `src/vendor/PlanesSegmentedControl.jsx` and `src/vendor/planes.css`.

Planes explicitly lists this component in its free tier and documents direct installation of the public source. The registry response carries no separate SPDX identifier or license text; no MIT or other unprovided license is asserted here. Publisher terms: <https://useplanes.com/terms>. Free component catalog and installation guidance: <https://useplanes.com/llms.txt>.

This adaptation retains the original measured selection pill, spring constants, velocity-driven stretch/blur, separators, spring press feedback, and controlled selection. TypeScript and Tailwind classes were converted to JSX and scoped plain CSS. The dashboard filter uses radio-group semantics with roving keyboard focus; labels remain visible at every width. Reduced-motion handling, font/layout measurement, and local light/dark tokens were adapted to this application.

No gated Planes Pro source was obtained or included. Any other custom visual components inspired by publicly visible Pro demonstrations are separately written application code, not Planes component imports.
