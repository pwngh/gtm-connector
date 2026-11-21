import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

// The measured contrast of the colour roles in tokens.css decides, not the hex.

const css = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8');

function theme(selector: string): Record<string, string> {
  const start = css.indexOf(selector);
  assert.ok(start >= 0, `${selector} is in tokens.css`);
  const block = css.slice(css.indexOf('{', start) + 1, css.indexOf('}', start));
  return Object.fromEntries([...block.matchAll(/--gc-([\w-]+):\s*(#[0-9a-f]{6})\b/gi)].map(([, name = '', value = '']) => [name, value]));
}

const channel = (value: number) => {
  const c = value / 255;
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
};
const luminance = (hex: string) => {
  const [r = 0, g = 0, b = 0] = [1, 3, 5].map(i => channel(Number.parseInt(hex.slice(i, i + 2), 16)));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const contrast = (a: string, b: string) => {
  const [x, y] = [luminance(a), luminance(b)];
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
};

const themes = {light: theme(':root[data-theme="light"]'), dark: theme(':root[data-theme="dark"]')};
const surfaces = ['canvas', 'surface', 'sunken'];
const texts = ['ink', 'ink-2', 'accent', 'red', 'amber', 'green'];

test('every text colour reaches 4.5 to 1 and the control border 3 to 1 on every surface, in both themes', () => {
  for (const [name, roles] of Object.entries(themes)) {
    for (const surface of surfaces) {
      const ground = roles[surface];
      assert.ok(ground, `${name} ${surface}`);
      for (const text of texts) {
        const ink = roles[text];
        assert.ok(ink, `${name} ${text}`);
        const ratio = contrast(ink, ground);
        assert.ok(ratio >= 4.5, `${name}: ${text} on ${surface} is ${ratio.toFixed(2)} to 1`);
      }
      const border = contrast(roles.control ?? '', ground);
      assert.ok(border >= 3, `${name}: the control border on ${surface} is ${border.toFixed(2)} to 1`);
    }
    const onAccent = contrast(roles['on-accent'] ?? '', roles.accent ?? '');
    assert.ok(onAccent >= 4.5, `${name}: text on the accent is ${onAccent.toFixed(2)} to 1`);
  }
});

test('the light theme is also the root default, so a page with no theme set is light', () => {
  assert.match(css, /:root,\s*:root\[data-theme="light"\] \{/);
});
