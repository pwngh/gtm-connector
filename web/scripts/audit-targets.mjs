// Browser audit for the Work plan and Evidence pages: target sizes, text sizes and
// overlapping hit areas, measured in Chrome at several widths and walkthrough states.
//
// It is not part of `npm test`, which runs without a browser. To run it:
//   1. Start the dev server:  npm run dev   (prints its URL, for example http://127.0.0.1:5173)
//   2. Make playwright-core loadable. It is not a dependency of this package: install it
//      anywhere and point PLAYWRIGHT_CORE at its package folder, for example
//        PLAYWRIGHT_CORE=/path/to/node_modules/playwright-core
//   3. node scripts/audit-targets.mjs --url http://127.0.0.1:5173 [--widths 1440,390] [--channel chrome]
// It uses an installed Chrome by default. The exit code is 1 when any check fails.
//
// Rules: every control is at least 44px in each dimension (a checkbox or radio may rely on
// a 44px label); text is at least 14px; control text is 16px, except labels inside whole-card
// controls; no two controls share hit area unless one contains the other (the sticky action
// bar is compared only with itself, since content scrolls under it).
import {createRequire} from 'node:module';
import {parseArgs} from 'node:util';

const {values: options} = parseArgs({options: {
  url: {type: 'string', default: 'http://127.0.0.1:5173'},
  widths: {type: 'string', default: '1440,1024,768,390,320'},
  channel: {type: 'string', default: 'chrome'},
}});
const {chromium} = createRequire(import.meta.url)(process.env.PLAYWRIGHT_CORE || 'playwright-core');
const base = options.url.replace(/\/$/, '') + '/renewals/thornfield';
const content = '.workflow-progress,.work-grid,.action-bar,.evidence-flow,.evidence-library';
// Whole cards that are buttons: their inner labels are captions, not control text.
const cards = '.step-header,.progress-node,.evidence-flow-node,.flow-context';

// Runs in the page. Measures the open dialog when there is one, otherwise the page content.
function measure({content, cards}) {
  const dialog = document.querySelector('.dialog');
  const roots = dialog ? [dialog] : [...document.querySelectorAll(content)];
  const shown = element => element.getClientRects().length > 0 && getComputedStyle(element).visibility !== 'hidden';
  const name = element => (element.getAttribute('aria-label') || element.textContent || element.tagName).trim().replace(/\s+/g, ' ').slice(0, 40);
  const controls = roots.flatMap(root => [...root.querySelectorAll('button,a[href],summary,input,select,textarea')]).filter(shown);
  const problems = [];
  const boxes = controls.map(control => {
    let box = control.getBoundingClientRect();
    const label = control.closest('label');
    if (control.matches('input[type=checkbox],input[type=radio]') && label) box = label.getBoundingClientRect();
    return {control, box};
  });
  for (const {control, box} of boxes) {
    if (Math.min(box.width, box.height) < 44) problems.push(`target ${name(control)} is ${Math.round(box.width)}x${Math.round(box.height)}`);
  }
  for (const root of roots) {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode, parent = node.parentElement;
      if (!node.textContent.trim() || !shown(parent) || parent.closest('svg')) continue;
      const size = parseFloat(getComputedStyle(parent).fontSize);
      const control = parent.closest('button,a[href],summary');
      const min = control && !control.matches(cards) && !control.closest(cards) ? 16 : 14;
      if (size < min) problems.push(`text "${node.textContent.trim().slice(0, 30)}" is ${size}px, under ${min}px`);
    }
  }
  for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
    const a = boxes[i], b = boxes[j];
    // The sticky action bar floats over the content that scrolls under it.
    if (a.control.contains(b.control) || b.control.contains(a.control) || Boolean(a.control.closest('.action-bar')) !== Boolean(b.control.closest('.action-bar'))) continue;
    const width = Math.min(a.box.right, b.box.right) - Math.max(a.box.left, b.box.left);
    const height = Math.min(a.box.bottom, b.box.bottom) - Math.max(a.box.top, b.box.top);
    if (width > 0.5 && height > 0.5) problems.push(`"${name(a.control)}" and "${name(b.control)}" overlap by ${Math.round(width)}x${Math.round(height)}`);
  }
  return {count: controls.length, problems: [...new Set(problems)]};
}

async function check(page, label, results) {
  await page.waitForTimeout(250);
  const {count, problems} = await page.evaluate(measure, {content, cards});
  results.push({label, count, problems});
}

async function withDialog(page, open, label, results) {
  const opener = typeof open === 'string' ? page.locator(open).first() : open;
  if (!await opener.count() || !await opener.isVisible() || await opener.isDisabled()) return;
  await opener.click(); await page.waitForSelector('.dialog');
  await check(page, label, results);
  await page.keyboard.press('Escape'); await page.waitForSelector('.dialog', {state: 'detached'});
}

async function expandSteps(page, label, results) {
  for (const step of await page.locator('.work-step').evaluateAll(steps => steps.map(step => step.id))) {
    if (!await page.locator(`#${step}.expanded`).count()) await page.click(`#${step} .step-header`);
    await check(page, `${label}, ${step} open`, results);
  }
}

// Moves the walkthrough one step with the action bar, accepting each dialog it opens.
async function advance(page) {
  const action = page.locator('.action-bar .button.primary');
  if (!await action.count() || await action.isDisabled()) return false;
  await action.click(); await page.waitForTimeout(300);
  if (await page.locator('.dialog').count()) {
    for (const field of await page.locator('.dialog textarea, .dialog input[type=text], .dialog input:not([type])').all()) {
      if (!await field.inputValue()) await field.fill('Audit input');
    }
    for (const box of await page.locator('.dialog input[type=checkbox]').all()) if (!await box.isChecked()) await box.check();
    await page.locator('.dialog .button.primary, .dialog button[type=submit]').last().click();
    await page.waitForTimeout(300);
    if (await page.locator('.dialog').count()) await page.keyboard.press('Escape');
  }
  if (!page.url().includes('/workplan')) await page.goto(base + '/workplan', {waitUntil: 'networkidle'});
  return true;
}

async function audit(browser, width) {
  const context = await browser.newContext({viewport: {width, height: 900}, reducedMotion: 'reduce'});
  const page = await context.newPage();
  const errors = []; page.on('pageerror', error => errors.push(String(error)));
  const results = [];
  await page.goto(base + '/workplan', {waitUntil: 'networkidle'});
  await check(page, 'Work plan', results);
  await expandSteps(page, 'Work plan', results);
  for (const [selector, label] of [['.quote-card.customer .icon-button', 'source'], ['.help-link', 'rider'], ['.gap-actions .secondary-gap-link', 'Sources checked'], ['.version-help', 'plan version'], ['.action-bar .button.ghost', 'preview']]) {
    await withDialog(page, selector, `Work plan ${label} dialog`, results);
  }
  await page.goto(base + '/evidence', {waitUntil: 'networkidle'});
  await check(page, 'Evidence', results);
  await withDialog(page, '.evidence-library .library-row .button', 'Evidence source dialog', results);
  await withDialog(page, '.evidence-library .panel-header .button', 'Evidence Sources checked dialog', results);
  await page.goto(base + '/workplan', {waitUntil: 'networkidle'});
  for (let moves = 0; moves < 12 && await advance(page); moves++) await check(page, `Work plan after action ${moves + 1}`, results);
  await expandSteps(page, 'Work plan at the end of the walkthrough', results);
  await withDialog(page, '.resolution-receipt .button', 'Work plan approval dialog', results);
  await page.goto(base + '/evidence', {waitUntil: 'networkidle'});
  await check(page, 'Evidence at the end of the walkthrough', results);
  await context.close();
  return {results, errors};
}

const browser = await chromium.launch({channel: options.channel, headless: true});
let failed = 0;
try {
  for (const width of options.widths.split(',').map(Number)) {
    const {results, errors} = await audit(browser, width);
    console.log(`\n${width}px`);
    for (const {label, count, problems} of results) {
      console.log(`  ${problems.length ? 'FAIL' : 'ok  '} ${label} (${count} controls)`);
      for (const problem of problems) console.log(`         ${problem}`);
      failed += problems.length;
    }
    for (const error of errors) {console.log(`  page error: ${error}`); failed++;}
  }
} finally {
  await browser.close();
}
console.log(failed ? `\n${failed} problems` : '\nNo problems');
process.exitCode = failed ? 1 : 0;
