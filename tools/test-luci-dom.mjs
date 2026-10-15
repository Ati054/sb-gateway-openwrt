// Usage: node tools/test-luci-dom.mjs /path/to/stock/luci.js
// Requires Playwright and Chromium; pass PLAYWRIGHT_MODULE for an external install.
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const directory = path.join(root, 'htdocs/luci-static/resources/view/resproxy');
const sources = {};
for (const name of await readdir(directory)) {
 if (!name.endsWith('.js')) continue;
 const source = await readFile(path.join(directory, name), 'utf8');
 new Function(source); // LuCI modules are function bodies with require directives.
 if (name !== 'safe_dom.js' && /\bE\(/.test(source)) {
  assert.match(source, /'require view\.resproxy\.safe_dom as safeDOM';/, name);
  assert.match(source, /var E = safeDOM\.create;/, name);
 }
 assert.doesNotMatch(source, /\b(?:innerHTML|outerHTML|insertAdjacentHTML)\b/, name);
 sources[name.replace(/\.js$/, '')] = source;
}
const stock = await readFile(process.argv[2], 'utf8');
const start = stock.search(/const\s+DOM\s*=\s*Class\.singleton/);
assert(start >= 0, 'stock LuCI DOM declaration missing');
const tail = stock.slice(start);
const end = tail.search(/const\s+Session\s*=/);
assert(end > 0, 'stock LuCI DOM boundary missing');
const stockDOM = tail.slice(0, end);
const server = createServer((req, res) => { res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }); res.end('<!doctype html><title>DOM regression</title><body>'); });
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
let browser;
try {
 browser = await chromium.launch({ headless: true, executablePath: process.env.SBG_CHROMIUM_PATH });
 const page = await browser.newPage();
 const errors = [];
 page.on('pageerror', error => errors.push(error.message));
 await page.goto(`http://127.0.0.1:${server.address().port}/`);
 const result = await page.evaluate(async ({ sources, stockDOM }) => {
  const baseclass = { extend: value => value };
  const Class = { singleton: value => value };
  const dom = new Function('Class', 'domParser', stockDOM + ';return DOM;')(Class, new DOMParser());
  const safeDOM = new Function('baseclass', 'dom', sources.safe_dom)(baseclass, dom);
  const _ = value => value;
  String.prototype.format = function(...args) { let i = 0; return this.replace(/%[sd]/g, () => String(args[i++])); };
  const payload = '<img src="data:," onerror="window.__sbgXSS=(window.__sbgXSS||0)+1">';
  const negative = dom.create('div', {}, payload);
  if (!negative.querySelector('img')) throw Error('Negative control did not reproduce HTML parsing');
  negative.querySelector('img').removeAttribute('onerror');
  window.__sbgXSS = 0;
  const node = { id:'n', value:'location:fr:test', label:payload, subscription:payload, protocol:'vless', country:'fr', city:'Test', selected:true, available:true, last_probe_at:1,
   period_stats:{today:{samples:1, median_ms:20, availability_percent:100}} };
  const health = new Function('baseclass', 'safeDOM', '_', sources.node_health)(baseclass, safeDOM, _);
  const healthView = health.create([{id:payload, nodes:[node], candidate_count:1, healthy_reserves:0}]);
  document.body.append(healthView.node);
  if (!healthView.node.textContent.includes(payload)) throw Error('Health did not preserve literal label');
  // Exercise the picker body directly; form/UCI persistence is outside this DOM test.
  const pickerSource = sources.location_picker.replace('return baseclass.extend({ Option: Option });', 'return picker;');
  if (pickerSource === sources.location_picker) throw Error('Picker test boundary changed');
  const picker = new Function('baseclass', 'form', 'uci', 'safeDOM', '_', pickerSource)(baseclass, {Value:{extend:value=>value}}, {get:()=> 'best', sections:()=>[]}, safeDOM, _);
  const state = {kind:'filter', order:[], view:'countries', source:'*', countries:[], protocols:[], include:'', exclude:''};
  const option = {map:{readonly:false}, formvalue:()=>JSON.stringify(state), cbid:()=> 'fixture'};
  const pickerView = picker(option, 'fixture', document.createElement('input'), state, {nodes:[node], regions:{europe:{fr:true}}, locations:[]});
  document.body.append(pickerView);
  if (!pickerView.textContent.includes(payload)) throw Error('Picker did not preserve literal label/subscription');
  for (const field of ['runtime_error','error.message','failure','version','rule.name','variant.name']) {
   const element = safeDOM.create('p', {}, payload); element.dataset.fixture=field; document.body.append(element);
  }
  for (const element of [safeDOM.create('p', payload), safeDOM.create('p', {}, () => payload), safeDOM.create([payload])]) {
   if (element.textContent !== payload) throw Error('Scalar/callback text lost'); document.body.append(element);
  }
  await new Promise(resolve => setTimeout(resolve, 50));
  if (document.querySelector('img,svg,script') || window.__sbgXSS) throw Error('Untrusted HTML entered DOM or executed');
  return {negativeControl:true, health:true, picker:true, scalarAndCallback:true, images:0, executed:0};
 }, { sources, stockDOM });
 assert.deepEqual(errors, []);
 console.log(JSON.stringify(result));
} finally {
 if (browser) await browser.close();
 await new Promise(resolve => server.close(resolve));
}
