import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const css = readFileSync(new URL('./chrome.css', import.meta.url), 'utf8');
const cssRules = [...css.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(match => ({selector: match[1].trim(), declarations: Object.fromEntries(match[2].split(';').filter(part => part.includes(':')).map(part => {const index = part.indexOf(':'); return [part.slice(0, index).trim(), part.slice(index + 1).trim()];}))}));
const rule = part => cssRules.find(rule => rule.selector.includes(part))?.declarations;

test('one shared CSS contract gives every chrome control a 44px target and 16px control text', () => {
  const targets = rule(':is(.sidebar,.topbar,.page-heading,.detail-nav) :is(a,button),.skip');
  assert.equal(targets['min-height'], '44px'); assert.equal(targets['min-width'], '44px');
  const header = rule('.page-heading .context-link,.page-heading .heading-actions .text-button');
  assert.equal(header['min-height'], '44px'); assert.equal(header['min-width'], '44px');
  const text = rule('.sidebar .nav-item,.sidebar .reset-link');
  assert.equal(text['font-size'], '1rem'); assert.equal(text['line-height'], '1.5');
  for (const selector of ['.topbar :is(.mobile-menu,.theme-toggle,.profile)', '.sidebar .mobile-nav-close']) {
    assert.equal(rule(selector).width, '44px'); assert.equal(rule(selector).height, '44px');
  }
  assert.equal(cssRules.find(rule => rule.selector === '.detail-nav nav a').declarations['min-height'], '44px');
  const legacy = ['styles', 'refinements', 'modern', 'atelier'].map(name => readFileSync(new URL('./' + name + '.css', import.meta.url), 'utf8')).join('\n');
  assert.doesNotMatch(legacy, /\.mobile-menu\s*\{[^}]*!important/);
});

test('page styles cannot resize the shared shell, renewal heading or tabs, and the loading state holds the viewport', () => {
  const readiness = readFileSync(new URL('./readiness.css', import.meta.url), 'utf8');
  assert.doesNotMatch(readiness, /\.(sidebar|topbar|main-content|main-shell|page-heading|detail-nav|nav-item|brand)(?=[\s.:>{,#])/);
  assert.equal(rule('.main-shell,.main-content')['min-width'], '0');
  assert.equal(cssRules.find(rule => rule.selector === '.detail-nav').declarations['flex-wrap'], 'wrap');
  assert.ok(css.includes('.main-content>.route-loading:not(.route-error) {min-height:max(160px, calc(100dvh - var(--route-top, 0px)))}'));
});

test('focus outlines use visible theme tokens and reduced motion covers the navigation overlay', () => {
  assert.equal(rule(':root')['--chrome-focus'], '#315ad9');
  assert.equal(rule("html[data-theme='dark']")['--chrome-focus'], '#a9c1ff');
  assert.equal(rule('.sidebar')['--chrome-focus'], '#a9c1ff');
  assert.equal(rule(':focus-visible').outline, '3px solid var(--chrome-focus)');
  assert.equal(rule(':focus-visible')['outline-offset'], '3px');
  const reduced = css.slice(css.indexOf('@media(prefers-reduced-motion:reduce)'));
  for (const selector of ['.sidebar', '.topbar', '.page-heading', '.detail-nav']) assert.ok(reduced.includes(selector));
  assert.ok(reduced.includes('transition:none!important'));
  assert.ok(reduced.includes('animation:none!important'));
});
