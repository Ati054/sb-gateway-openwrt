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
const appearanceCSS = await readFile(path.join(directory, 'style.css'), 'utf8');
const nameCases = JSON.parse(await readFile(path.join(root, 'internal/selection/testdata/name_filters.json'), 'utf8'));
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
 const result = await page.evaluate(async ({ sources, stockDOM, nameCases, appearanceCSS }) => {
  document.documentElement.lang = 'ru';
  const baseclass = { extend: value => value };
  const Class = { singleton: value => value };
  const dom = new Function('Class', 'domParser', stockDOM + ';return DOM;')(Class, new DOMParser());
  const safeDOM = new Function('baseclass', 'dom', sources.safe_dom)(baseclass, dom);
  // Reproduce an existing browser session from before the XRoute rename.
  // Bootstrap also styles every footer, including footers inside package views.
  const themeStyle = document.createElement('style');
  themeStyle.id = 'sbg-ui-style';
  themeStyle.textContent = 'footer { padding:20px 120px; margin-inline-start:30px; text-align:center; width:60%; }\n' + appearanceCSS;
  document.head.append(themeStyle);
  for (const language of ['en', 'ru', 'fa', 'zh-cn', 'zh-tw']) {
   document.documentElement.lang = language;
   const values = { 'sbg-menu-version':'r105-i18n:' + language, menu:{ title:'SB Gateway for OpenWrt' } };
   let flushed = 0, reloaded = 0;
   const session = { getLocalData:key=>values[key], setLocalData:(key,value)=>{ values[key]=value; return true; } };
   const ui = { menu:{ flushCache:()=>{ flushed++; values.menu=null; } } };
   const appearance = new Function('baseclass', 'session', 'ui', 'safeDOM', 'L', 'window', sources.appearance)(
    baseclass, session, ui, safeDOM, { resource:value=>value }, { location:{ reload:()=>{ reloaded++; } } });
   const root = document.createElement('div'); root.style.width = '340px';
   document.body.append(appearance.decorate(root));
   if (flushed !== 1 || reloaded !== 1 || values.menu !== null) throw Error('Legacy menu survived the package rename');
   appearance.decorate(root);
   const footers = root.querySelectorAll('.xroute-contact'), link = root.querySelector('.xroute-contact a');
   if (flushed !== 1 || reloaded !== 1 || footers.length !== 1) throw Error('Appearance repeats reloads or footers');
   if (link.href !== 'https://t.me/xroute_dev' || link.target !== '_blank' || link.rel !== 'noopener noreferrer') throw Error('Community link changed');
   const bounds = root.getBoundingClientRect(), contact = link.getBoundingClientRect();
   const rtl = language === 'fa';
   if (Math.abs((rtl ? contact.right : contact.left) - (rtl ? bounds.right : bounds.left)) > 1) throw Error('Theme footer padding displaced the community link');
   root.remove();
  }
  themeStyle.remove(); document.documentElement.lang = 'ru';
  const _ = value => value;
  const unicodeText=new Function('baseclass',sources.unicode_text)(baseclass);
  const configuredSources=[];
  const entityNames=new Function('baseclass','uci','unicodeText','_',sources.entity_names)(baseclass,{sections:(_c,t)=>t==='subscription'?configuredSources:[],get:()=>null},unicodeText,_);
  const nameFilter = new Function('baseclass', '_','unicodeText', sources.name_filter)(baseclass, _,unicodeText);
  String.prototype.format = function(...args) { let i = 0; return this.replace(/%[sd]/g, () => String(args[i++])); };
  const payload = '<img src="data:," onerror="window.__sbgXSS=(window.__sbgXSS||0)+1">';
  configuredSources.push({id:payload,name:payload});
  const negative = dom.create('div', {}, payload);
  if (!negative.querySelector('img')) throw Error('Negative control did not reproduce HTML parsing');
  negative.querySelector('img').removeAttribute('onerror');
  window.__sbgXSS = 0;
  const node = { id:'n', value:'location:fr:test', label:payload, subscription:payload, protocol:'vless', country:'fr', city:'Test', selected:true, available:true, last_probe_at:1,
   period_stats:{today:{samples:1, median_ms:20, availability_percent:100}} };
  const health = new Function('baseclass', 'safeDOM', 'nameFilter', '_','entityNames', sources.node_health)(baseclass, safeDOM, nameFilter, _,entityNames);
  const healthView = health.create([{id:payload, nodes:[node], candidate_count:1, healthy_reserves:0}]);
  document.body.append(healthView.node);
  if (!healthView.node.textContent.includes(payload)) throw Error('Health did not preserve literal label');
  const filterNodes = [node,
   {...node,id:'amsterdam',label:'Амстердам Premium',selected:false,latency_delta_percent:-25,latency_comparison:{active_at:1000,candidate_at:1001}},
   {...node,id:'sweden',label:'Швеция Premium',selected:false,latency_delta_percent:25},
   {...node,id:'trial',label:'Швеция тестовый',selected:false,latency_delta_percent:50},
   {...node,id:'berlin',label:'Берлин',selected:false,latency_delta_percent:149.6}
  ];
  healthView.update([{id:payload,nodes:filterNodes,candidate_count:5,healthy_reserves:4}]);
  const healthSearch = healthView.node.querySelector('input[type="search"]');
  const healthRows = () => Array.from(healthView.node.querySelectorAll('tbody tr'));
  healthSearch.value=' амстердам , ШВЕЦИЯ, , '; healthSearch.dispatchEvent(new Event('input'));
  if(healthRows().length!==4 || healthRows().some(row=>row.textContent.includes('Берлин')))throw Error('Health comma search did not use case-insensitive alternatives');
  const deltaCell=label=>healthRows().find(row=>row.cells[0].textContent===label).cells[4];
  if(deltaCell('Амстердам Premium').textContent!=='-25.0%' || deltaCell('Швеция Premium').textContent!=='+25.0%')throw Error('Delta signs rendered incorrectly');
  if(!deltaCell('Амстердам Premium').title.includes('Negative means lower latency'))throw Error('Delta tooltip contradicts signs');
  if(!deltaCell('Амстердам Premium').title.includes(new Date(1000*1000).toLocaleString('ru', {calendar:'gregory'})) || !deltaCell('Амстердам Premium').title.includes(new Date(1001*1000).toLocaleString('ru', {calendar:'gregory'})))throw Error('Delta tooltip lost either completion time in the UI locale');
  document.documentElement.lang='fa'; healthView.node.dir='rtl';
  healthView.update([{id:payload,nodes:filterNodes,candidate_count:5,healthy_reserves:4}]);
  const persianDelta=deltaCell('Амстердам Premium');
  if(persianDelta.textContent!=='-25.0%' || getComputedStyle(persianDelta).direction!=='ltr')throw Error('RTL Delta changed numeric reading order');
  if(!persianDelta.title.includes(new Date(1000*1000).toLocaleString('fa', {calendar:'gregory'})) || !persianDelta.title.includes(new Date(1001*1000).toLocaleString('fa', {calendar:'gregory'})))throw Error('Persian tooltip lost Gregorian completion times');
  document.documentElement.lang='ru'; healthView.node.dir='ltr';
  healthView.update([{id:payload,nodes:filterNodes,candidate_count:5,healthy_reserves:4}]);
  healthSearch.value='"Швеция Premium", "Берлин"'; healthSearch.dispatchEvent(new Event('input'));
  if(healthRows().length!==3 || healthRows().some(row=>row.textContent.includes('Швеция тестовый')))throw Error('Health full-name search matched a partial label');
  healthSearch.value='"Швеция';healthSearch.dispatchEvent(new Event('input'));
  if(healthSearch.getAttribute('aria-invalid')!=='true' || healthView.node.querySelector('[role="alert"]').hidden)throw Error('Malformed search quotes have no visible error');
  healthSearch.value=' , , ';healthSearch.dispatchEvent(new Event('input'));
  if(healthRows().length!==5 || deltaCell('Берлин').textContent!=='+149.6%')throw Error('Empty fragments or slower-than-double delta lost');
  healthSearch.value='';healthSearch.dispatchEvent(new Event('input'));
  // A fast historical median must not hide currently available reserves.
  const rankedNodes = [{...node,id:'active',label:'Active country',period_stats:{today:{median_ms:1425},'7d':{median_ms:2000}}},
   ...Array.from({length:9},(_,i)=>({...node,id:'ready-'+i,label:'Ready country '+i,selected:false,
    period_stats:{today:{median_ms:i===8?null:1500+i},'7d':{median_ms:i===8?null:1800-i}}})),
   ...Array.from({length:11},(_,i)=>({...node,id:'down-'+i,label:'Down country '+i,selected:false,available:false,
    period_stats:{today:{median_ms:600+i},'7d':{median_ms:500+i}}})),
   {...node,id:'unchecked',label:'Unchecked country',selected:false,available:false,last_probe_at:0,period_stats:{}}
  ];
  const rankedPolicy=()=>[{id:payload,nodes:rankedNodes,candidate_count:rankedNodes.length,healthy_reserves:9}];
  healthView.update(rankedPolicy());
  const healthPeriod=healthView.node.querySelector('select[aria-label="Rating period"]');
  for(const ratingPeriod of ['today','7d']) {
   healthPeriod.value=ratingPeriod;healthPeriod.dispatchEvent(new Event('change'));
   if(healthRows().length!==10 || healthRows()[0].cells[0].textContent!=='Active country' ||
    healthRows().slice(1).some(row=>!row.cells[0].textContent.startsWith('Ready country ')))throw Error('Unavailable historical medians displaced healthy reserves');
   if(!healthView.node.textContent.includes('22 candidates · 9 healthy reserves') || !healthView.node.textContent.includes('Showing 10 of 22 nodes'))throw Error('Health summary disagrees with limited table');
   healthView.update(rankedPolicy());
   if(healthRows().slice(1).some(row=>!row.cells[0].textContent.startsWith('Ready country ')))throw Error('Health refresh lost availability ordering');
  }
  healthSearch.value='Down country 10';healthSearch.dispatchEvent(new Event('input'));
  if(healthRows().length!==2 || healthRows()[0].cells[0].textContent!=='Active country' || healthRows()[1].cells[0].textContent!=='Down country 10')throw Error('Unavailable nodes cannot be found after prioritizing reserves');
  healthSearch.value='';healthSearch.dispatchEvent(new Event('input'));
  rankedNodes[1].available=false;
  rankedNodes.push({...node,id:'replacement',label:'Ready replacement',selected:false,period_stats:{today:{median_ms:9999},'7d':{median_ms:9999}}});
  healthView.update(rankedPolicy());
  if(healthRows().length!==10 || !healthRows().some(row=>row.cells[0].textContent==='Ready replacement') || healthRows().some(row=>row.cells[0].textContent==='Ready country 0'))throw Error('Health change did not replace failed reserve in limited table');
  healthPeriod.value='today';healthPeriod.dispatchEvent(new Event('change'));
  window.localStorage.removeItem('sb-gateway.node-health.v1');
  // Exercise the picker body directly; form/UCI persistence is outside this DOM test.
  const pickerSource = sources.location_picker.replace('return baseclass.extend({ Option: Option });', 'return picker;');
  if (pickerSource === sources.location_picker) throw Error('Picker test boundary changed');
  const picker = new Function('baseclass', 'form', 'uci', 'safeDOM', 'nameFilter', '_','entityNames', pickerSource)(baseclass, {Value:{extend:value=>value}}, {get:(_c,_s,key)=>key==='mode'?'best':null, sections:(_c,t)=>t==='subscription'?configuredSources:[]}, safeDOM, nameFilter, _,entityNames);
  const state = {kind:'filter', order:[], view:'countries', source:'*', countries:[], protocols:[], include:'', exclude:''};
  const option = {map:{readonly:false}, formvalue:()=>JSON.stringify(state), cbid:()=> 'fixture',getUIElement:()=>null};
  const pickerView = picker(option, 'fixture', document.createElement('input'), state, {nodes:filterNodes, regions:{europe:{fr:true}}, locations:[]});
  document.body.append(pickerView);
  if (!pickerView.textContent.includes(payload)) throw Error('Picker did not preserve literal label/subscription');
  const include=pickerView.querySelector('input[aria-label="Include names"]');
  const exclude=pickerView.querySelector('input[aria-label="Exclude names"]');
  if(pickerView.querySelector('select[aria-label="Match exclusion as"]'))throw Error('Draft match-mode selector remains');
  const setInput=(input,value)=>{input.value=value;input.dispatchEvent(new Event('input'));};
  const preview=()=>pickerView.querySelector('.sbg-location-preview strong').textContent;
  setInput(include,' амстердам , ШВЕЦИЯ, , ');
  if(preview()!=='Matching nodes: 3')throw Error('Picker preview still requires all fragments');
  setInput(exclude,'ТЕСТ, trial');
  if(preview()!=='Matching nodes: 2')throw Error('Substring exclusion did not override inclusion');
  setInput(exclude,'"ШВЕЦИЯ ТЕСТОВЫЙ", unused');
  if(preview()!=='Matching nodes: 2')throw Error('Full-name mode failed');
  setInput(exclude,'"тестовый"');
  if(preview()!=='Matching nodes: 3')throw Error('Full-name mode treated a fragment as full name');
  const staleID='sub-cfg02ef91';
  const staleState={kind:'filter',order:[],view:'countries',source:staleID,countries:[],protocols:[],include:'',exclude:''};
  const stalePicker=picker(option,'fixture',document.createElement('input'),staleState,{nodes:[{...node,subscription:staleID}],regions:{},locations:[]});
  if(stalePicker.textContent.includes(staleID)||!stalePicker.textContent.includes('Unavailable subscription')||stalePicker.querySelector('.sbg-location-preview strong').textContent!=='Matching nodes: 0')throw Error('Retired inventory source exposed an ID or remained selectable');
  const nameModule=sources.location_picker.replace('return baseclass.extend({ Option: Option });','return {Option:Option,eligible:eligible};');
  const savedUCI={};
  const pickerModule=new Function('baseclass','form','uci','safeDOM','nameFilter','_','entityNames',nameModule)(baseclass,{Value:{extend:value=>value}},
   {get:(_c,_s,key)=>savedUCI[key],set:(_c,_s,key,value)=>{savedUCI[key]=value;},unset:(_c,_s,key)=>{delete savedUCI[key];}},safeDOM,nameFilter,_,entityNames);
  pickerModule.Option.write('fixture',JSON.stringify(state));
  const restoredFilter=JSON.parse(pickerModule.Option.cfgvalue('fixture'));
  if(restoredFilter.include!==state.include.trim() || restoredFilter.exclude!==state.exclude.trim())throw Error('Quoted filters lost on UCI save/reload');
  for(const c of nameCases) {
   const base={kind:'filter',source:'*',countries:[],protocols:[],include:'',exclude:''};
   const parsed=nameFilter.parse(c.pattern);
   if(!!parsed.error!==!!c.invalid)throw Error('Quote validation differs from backend: '+JSON.stringify(c));
   if(c.invalid) {
    if(pickerModule.Option.validate('fixture',JSON.stringify({...base,include:c.pattern}))===true || pickerModule.Option.validate('fixture',JSON.stringify({...base,exclude:c.pattern}))===true)throw Error('Malformed quotes can be saved');
    if(pickerModule.eligible({...base,include:c.pattern},{label:c.label||''}) || pickerModule.eligible({...base,exclude:c.pattern},{label:c.label||''}))throw Error('Malformed quotes admitted candidates');
    continue;
   }
   if(pickerModule.eligible({...base,include:c.pattern},{label:c.label})!==c.match ||
    pickerModule.eligible({...base,exclude:c.pattern},{label:c.label})===c.match)throw Error('Picker matching differs from backend: '+JSON.stringify(c));
  }
  for(const tooLong of ['x'.repeat(513),'я'.repeat(513)])if(!nameFilter.parse(tooLong).error)throw Error('Oversized Unicode filter accepted');
  for(const supported of ['я'.repeat(512),'中'.repeat(512),'🔒'.repeat(512)])if(nameFilter.parse(supported).error)throw Error('Valid Unicode filter has a smaller character limit');
  for (const field of ['runtime_error','error.message','failure','version','rule.name','variant.name']) {
   const element = safeDOM.create('p', {}, payload); element.dataset.fixture=field; document.body.append(element);
  }
  for (const element of [safeDOM.create('p', payload), safeDOM.create('p', {}, () => payload), safeDOM.create([payload])]) {
   if (element.textContent !== payload) throw Error('Scalar/callback text lost'); document.body.append(element);
  }
  // Exercise the actual overview against the prepared-view reader. Polling
  // and manual refresh must use RAM snapshots, including failover and stop.
  let alive=true, monitorReads=0, ruleCommands=0, poller;
  const stamp=()=>new Date().toISOString();
  const sample=(id,label,delay)=>({id,label,available:true,latest_delay_ms:delay,latest_quality_at:Math.floor(Date.now()/1000),period_stats:{today:{samples:17,availability_percent:100,median_ms:delay}}});
  const runtime={updated_at:stamp(),updated_uptime_seconds:999,policies:{'resproxy-all':{selected:'a',runtime_selected:'a',runtime_confirmed:true},'resproxy-pc':{selected:'block',runtime_selected:'block',runtime_confirmed:true,wan_fallback_active:true}}};
  const ruleStatus={error:'unsupported GeoSite expression',packs:[{id:'cn-all',state:'updated',entries:10}]};
  const files={
   'health-state/selector-runtime.json':runtime,'rulesets-status.json':ruleStatus,'configuration-recovery.json':null,
   'health-state/monitor.json':{updated_at:stamp(),health:[{id:'all',mode:'best',healthy_reserves:1,candidate_count:2,probe_limits:{active_seconds:60},nodes:[sample('a',payload,450),sample('b','Backup',300)]},{id:'pc',mode:'priority',healthy_reserves:0,candidate_count:1,nodes:[sample('b','Backup',300)]}]},
   'catalog-summary.json':{nodes:842,counts:{source:842}},'subscription-status.json':{},'dns-status.json':{queries:42},'dns-interop.json':{effective_mode:'proxy-only'},'resources.json':{updated_at:stamp(),updated_uptime_seconds:998,controller:{rss_kib:49152,anon_kib:20000,fds:20,fd_limit:8192},core:{rss_kib:40960,anon_kib:20000,fds:30,fd_limit:8192},available_kib:90000,tcp_sessions:10,udp_sessions:2}
  };
  function read(path) {monitorReads++;return structuredClone(files[path.replace('/var/run/resproxy/','')]);}
  const fs={read:async path=>path==='/proc/uptime' ? '1000 1500' : JSON.stringify(read(path)),read_direct:async path=>read(path),exec:async command=>{
   if(command!='/usr/libexec/resproxy-rulesets')throw Error('Overview launched a status/health CLI');
   ruleCommands++;
   return {code:0,stdout:JSON.stringify({choices:[],status:ruleStatus})};
  }};
  const rpc={declare:()=>async()=>({resproxy:{instances:{gateway:{running:alive}}}})};
  const monitor=new Function('baseclass','fs','rpc',sources.runtime_status)(baseclass,fs,rpc);
  let configured=[{id:'all',mode:'best',selection_order:['all:*']},{id:'pc',mode:'priority',selection_order:['country:FR']}],enabledOption;
  const uci={load:async()=>{},sections:()=>configured};
  function Map() {this.section=()=>({option:(_type,name)=>{const option={};if(name==='enabled')enabledOption=option;return option;}});this.render=async()=>document.createElement('div');}
  const overview=new Function('sbgTheme','healthUI','view','form','fs','uci','poll','safeDOM','monitor','_','L','entityNames',sources.overview)(
   {decorate:node=>node},health,{extend:value=>value},{Map,NamedSection:{},Flag:{extend:value=>value}},fs,uci,
   {add:fn=>{poller=fn;},remove:()=>{}},safeDOM,monitor,_,{resource:path=>'/luci-static/resources/'+path},entityNames);
  const dashboard=await overview.render(await overview.load());document.body.append(dashboard);
  if(enabledOption.validate('main','1')!==true)throw Error('Configured routes could not be enabled');
  const savedPolicies=configured;configured=[];
  if(enabledOption.validate('main','1')===true || enabledOption.validate('main','0')!==true)throw Error('Missing routes did not prevent first activation');
  configured=[{id:'main'}];
  if(enabledOption.validate('main','1')===true)throw Error('Empty route did not prevent activation');
  configured=savedPolicies;
  const routeRows=()=>dashboard.querySelector('.sbg-overview > .sbg-table-scroll tbody').rows;
  if(!dashboard.textContent.includes('842 nodes') || !routeRows()[1].textContent.includes('Fallback WAN'))throw Error('Prepared metadata or WAN state lost in overview');
  const databaseWarning=Array.from(dashboard.querySelectorAll('.alert-message')).find(node=>node.textContent.includes('Rule database update was not applied'));
  if(!databaseWarning || databaseWarning.hidden || databaseWarning.closest('details') || !databaseWarning.getBoundingClientRect().height)throw Error('Database failure hidden in collapsed details');
  if(dashboard.querySelector('details[open]'))throw Error('Regression fixture must keep database details collapsed');
  const previousCommands=ruleCommands;
  files['configuration-recovery.json']={active:true};await poller();
  const recoveryMessage='New settings could not be applied. The last working configuration is active. Check the journal.';
  const recoveryWarning=()=>Array.from(dashboard.querySelectorAll('.alert-message')).find(node=>node.textContent===recoveryMessage);
  if(!recoveryWarning() || recoveryWarning().hidden)throw Error('Last-good configuration recovery is invisible');
  files['configuration-recovery.json']=null;await poller();
  if(recoveryWarning())throw Error('Recovered configuration warning remained after a valid Apply');
  const dnsValue=label=>Array.from(dashboard.querySelectorAll('.sbg-overview-counters > div')).find(node=>node.querySelector('dt').textContent===label)?.querySelector('dd').textContent;
  const dnsHelp=()=>Array.from(dashboard.querySelectorAll('.sbg-overview-counters dt')).find(node=>node.textContent==='Errors')?.querySelector('[title]');
  files['dns-status.json']={queries:100,failures:14,failure_invalid_query:13,dns_failures:1};
  await poller();
  if(dnsValue('Rejected input')!==undefined || dnsValue('Errors')!=='1' || dashboard.querySelectorAll('.sbg-overview-counters > div').length!==7)throw Error('DNS errors gained a separate rejected-input counter');
  if(dnsHelp()?.title!=='DNS errors: 1. Failed DNS resolution or routing.\nRejected input: 13.' || dnsHelp().tabIndex!==0 || dnsHelp().getAttribute('aria-label')!=='Errors. '+dnsHelp().title)throw Error('DNS Errors help lost the rejected-input count or accessibility');
  files['dns-status.json']={queries:100,failures:14,failure_invalid_query:13};
  await poller();
  if(dnsValue('Errors')!=='1' || dnsHelp().title!=='DNS errors: 1. Failed DNS resolution or routing.\nRejected input: 13.')throw Error('Legacy detailed DNS counters lost their meaning');
  files['dns-status.json']={queries:100,failures:0,failure_invalid_query:0,dns_failures:0};
  await poller();
  if(dnsValue('Errors')!=='0' || dnsHelp().title!=='DNS errors: 0. Failed DNS resolution or routing.\nRejected input: 0.')throw Error('Zero rejected-input count disappeared from the tooltip');
  files['dns-status.json']={queries:100,failures:14};
  await poller();
  if(dnsValue('Errors')!=='14' || dnsHelp().title!=='DNS errors: 14. Failed DNS resolution or routing.')throw Error('Legacy DNS totals lost their meaning');
  files['dns-interop.json']={effective_mode:'system',reason:'https-dns-proxy-running'};
  await poller();
  const takeoverWarning=()=>Array.from(dashboard.querySelectorAll('.alert-message')).find(node=>!node.hidden && (node.textContent.includes('Configure DNS takeover') || node.textContent.includes('DNS takeover failed')));
  if(takeoverWarning()?.textContent!=='https-dns-proxy is running. Configure DNS takeover on the DNS page.')throw Error('Unconfirmed DNS takeover lost its instruction');
  files['dns-interop.json'].handoff_confirmed=true;
  await poller();
  if(takeoverWarning()?.textContent!=='DNS takeover failed; previous DNS restored. See the journal.')throw Error('Confirmed failed DNS takeover incorrectly asks for confirmation again');
  files['dns-interop.json'].reason='wan-dns-preflight-failed';
  await poller();
  if(!Array.from(dashboard.querySelectorAll('.alert-message')).some(node=>!node.hidden && node.textContent==='WAN DNS is unavailable. OpenWrt DNS remains active.'))throw Error('Unavailable WAN DNS hid the actual reason behind a repeated consent instruction');
  files['dns-interop.json']={effective_mode:'proxy-only',handoff_confirmed:true};
  await poller();
  if(takeoverWarning())throw Error('Recovered DNS takeover retained its failure warning');
  for(const [phase,label] of [['starting','Waiting for service startup'],['queued','waiting for its turn'],['downloading','Downloading rule databases'],['applying','Applying rule databases'],['unused','No downloadable rule databases']]) {
   files['rulesets-status.json']={phase,packs:[{id:'youtube',state:'pending',entries:0}]};
   await poller();
   const summary=dashboard.querySelector('.sbg-overview-detail > p[role="status"]');
   if(!summary || !summary.textContent.includes(label))throw Error('Database progress did not refresh from prepared status: '+phase);
  }
  if(ruleCommands!==previousCommands)throw Error('Progress polling launched an unnecessary rules CLI');
  files['rulesets-status.json']=ruleStatus;
  const realNow=Date.now;
  for(const skew of [-7*3600000,7*3600000]) {
   Date.now=()=>realNow()+skew;await poller();
   if(!routeRows()[0].textContent.includes('Active') || !dashboard.textContent.includes('48.0 MiB'))throw Error('PC clock hid active route or RAM');
  }
  Date.now=realNow;
  const trustedLogo=dashboard.querySelector('.xroute-brand > img');
  if(!trustedLogo || trustedLogo.getAttribute('src')!=='/luci-static/resources/view/resproxy/xroute.svg')throw Error('Packaged brand asset missing');
  if(dashboard.querySelector('.sbg-overview img'))throw Error('Prepared label parsed as HTML');
  runtime.policies['resproxy-pc']={selected:'b',runtime_selected:'b',runtime_confirmed:true};runtime.updated_at=stamp();
  await poller();
  if(routeRows()[1].textContent.includes('Fallback WAN'))throw Error('Overview retained stale fallback after recovery');
  alive=false;await poller();
  if(!dashboard.textContent.includes('Stopped'))throw Error('Overview ignored procd stop');
  const refresh=dashboard.querySelector('.sbg-overview-toolbar button');refresh.click();
  await new Promise(resolve=>setTimeout(resolve,20));
  if(!monitorReads)throw Error('Prepared reader was not exercised');
  // A transient initial RPC failure must leave a usable page that recovers on
  // the next poll, with or without an available rule-database response.
  for (const rulesUnavailable of [false,true]) {
   let unavailable=true,recoveryPoll;
   const transientMonitor={snapshot:()=>unavailable ? Promise.reject(Error('temporary monitoring RPC failure')) : monitor.snapshot()};
   const transientFS={exec:async command=>{
    if(unavailable && rulesUnavailable)throw Error('temporary rules RPC failure');
    return fs.exec(command);
   }};
   const recoveringOverview=new Function('sbgTheme','healthUI','view','form','fs','uci','poll','safeDOM','monitor','_','L','entityNames',sources.overview)(
    {decorate:node=>node},health,{extend:value=>value},{Map,NamedSection:{},Flag:{extend:value=>value}},transientFS,uci,
    {add:fn=>{recoveryPoll=fn;},remove:()=>{}},safeDOM,transientMonitor,_,{resource:path=>'/luci-static/resources/'+path},entityNames);
   const recoveringDashboard=await recoveringOverview.render(await recoveringOverview.load());
   document.body.append(recoveringDashboard);
   const warning=Array.from(recoveringDashboard.querySelectorAll('.alert-message')).find(node=>node.textContent.startsWith('No fresh data:'));
   if(!warning || warning.hidden || !recoveringDashboard.querySelector('.sbg-overview-toolbar button'))throw Error('Initial monitoring failure hid the usable overview or its warning');
   unavailable=false;alive=true;await recoveryPoll();
   if(!warning.hidden || !recoveringDashboard.textContent.includes('842 nodes'))throw Error('Overview did not recover after a transient monitoring failure: rules='+rulesUnavailable+', warning='+warning.textContent);
   recoveringDashboard.remove();
  }
	// Category changes must issue distinct requests and ignore older replies.
	const journalRequests=[];
	const journalFS={exec:(command,args)=>{
	 if(command!=='/usr/libexec/resproxy-log')throw Error('Unexpected journal command');
	 return new Promise((resolve,reject)=>journalRequests.push({args,resolve,reject}));
	}};
	const journal=new Function('sbgTheme','view','fs','ui','safeDOM','_','unicodeText',sources.log)(
	 {decorate:node=>node},{extend:value=>value},journalFS,{addNotification:()=>{}},safeDOM,_,unicodeText);
	const journalView=journal.render('initial events');document.body.append(journalView);
	const category=journalView.querySelector('select[aria-label="Journal category"]');
	const lineCount=journalView.querySelector('select[aria-label="Maximum lines"]');
	const journalOutput=journalView.querySelector('pre');
	category.value='errors';category.dispatchEvent(new Event('change'));
	category.value='xray';category.dispatchEvent(new Event('change'));
	if(journalRequests.map(r=>r.args[0]).join(',')!=='errors,xray')throw Error('Journal category did not reach helper');
	journalRequests[1].resolve({code:0,stdout:'core warning'});
	await new Promise(resolve=>setTimeout(resolve,0));
	journalRequests[0].resolve({code:0,stdout:'old errors'});
	await new Promise(resolve=>setTimeout(resolve,0));
	if(journalOutput.textContent!=='core warning')throw Error('Older journal reply overwrote selected category');
	lineCount.value='500';lineCount.dispatchEvent(new Event('change'));
	if(journalRequests[2].args.join(',')!=='xray,500' || journalOutput.getAttribute('aria-busy')!=='true')throw Error('Journal line count or loading state ignored');
	journalRequests[2].resolve({code:0,stdout:payload});
	await new Promise(resolve=>setTimeout(resolve,0));
	if(journalOutput.textContent!==payload || journalOutput.querySelector('img'))throw Error('Journal HTML was executed');
	category.value='all';category.dispatchEvent(new Event('change'));
	journalRequests[3].resolve({code:0,stdout:'system-only entry'});
	await new Promise(resolve=>setTimeout(resolve,0));
	if(journalRequests[3].args[0]!=='all' || journalOutput.textContent!=='system-only entry')throw Error('System journal did not switch');
	category.value='events';category.dispatchEvent(new Event('change'));
	journalRequests[4].reject(Error('fixture read failed'));
	await new Promise(resolve=>setTimeout(resolve,0));
	if(journalOutput.textContent!=='Unable to read the journal.' || journalOutput.hasAttribute('aria-busy'))throw Error('Journal failure retained misleading old category');
	const journalSearch=journalView.querySelector('input[type="search"]');
	journalSearch.value='system-only';journalSearch.dispatchEvent(new Event('input'));
	if(journalOutput.textContent.includes('system-only entry'))throw Error('Search restored lines from a failed old category');
  await new Promise(resolve => setTimeout(resolve, 50));
  if (document.querySelector('svg,script') || Array.from(document.images).some(img=>img!==trustedLogo) || window.__sbgXSS) throw Error('Untrusted HTML entered DOM or executed');
  return {negativeControl:true, appearanceCacheMigration:true, footerThemeAlignment:true, health:true, picker:true, commaAlternatives:true, quotedNames:true, sharedNameCases:nameCases.length, exclusionPrecedence:true, deltaSigns:true, scalarAndCallback:true, overviewMonitoring:true, overviewTransientErrors:true, dnsTakeoverStatus:true, clockIndependentStatus:true, visibleDatabaseFailure:true, journalCategories:true, images:0, executed:0};
 }, { sources, stockDOM, nameCases, appearanceCSS });
 assert.deepEqual(errors, []);
 console.log(JSON.stringify(result));
} finally {
 if (browser) await browser.close();
 await new Promise(resolve => server.close(resolve));
}
