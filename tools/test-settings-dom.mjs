// Usage: node tools/test-settings-dom.mjs luci.js form.js ui.js
// Uses real LuCI forms/widgets with isolated UCI and RPC fixtures.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import {createServer} from 'node:http';
const require=createRequire(import.meta.url);
const {chromium}=require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const dir=new URL('../htdocs/luci-static/resources/view/resproxy/',import.meta.url);
const names=['rulesets','drafts','safe_dom','interval','dns','routes','location_picker','name_filter','static_clients','monitor','subscription'];
const sources=Object.fromEntries(await Promise.all(names.map(async n=>[n,await readFile(new URL(n+'.js',dir),'utf8')])));
const stock=await readFile(process.argv[2],'utf8');
const formSource=await readFile(process.argv[3],'utf8');
const uiSource=await readFile(process.argv[4],'utf8');
const classStart=stock.search(/const\s+toCamelCase\s*=/),classEnd=stock.search(/const\s+Headers\s*=/);
const domStart=stock.search(/const\s+DOM\s*=/),domEnd=stock.search(/const\s+Session\s*=/);
assert(classStart>=0&&classEnd>classStart&&domStart>=0&&domEnd>domStart,'stock LuCI module boundaries changed');
const classSource=stock.slice(classStart,classEnd),domSource=stock.slice(domStart,domEnd);
const server=createServer((req,res)=>res.end('<!doctype html><body>'));
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const browser=await chromium.launch({headless:true,executablePath:process.env.SBG_CHROMIUM_PATH});
try {
 const page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto('http://127.0.0.1:'+server.address().port);
 const result=await page.evaluate(async({sources,classSource,domSource,formSource,uiSource})=>{
  const LuCI={prototype:{isArguments:v=>Object.prototype.toString.call(v)==='[object Arguments]'}};
  const Class=new Function('LuCI',classSource+';return Class;')(LuCI);
  const dom=new Function('Class','domParser',domSource+';return DOM;')(Class,new DOMParser());
  const baseclass=Class;
  const _=v=>v; const E=dom.create.bind(dom);
  String.prototype.format=function(...args){let i=0;return this.replace(/%[sdh]/g,()=>String(args[i++]));};
  const L=window.L={bind:(fn,obj,...args)=>fn.bind(obj,...args),isObject:v=>v!==null&&typeof v==='object',toArray:v=>v==null?[]:Array.isArray(v)?v:[v],env:{sessionid:'fixture'},error:(t,m)=>{throw Error(t+': '+m);},resolveDefault:(p,d)=>Promise.resolve(p).catch(()=>d),resource:x=>x,url:(...parts)=>'/'+parts.join('/'),naturalCompare:(a,b)=>String(a).localeCompare(String(b))};
  const config={main:{'.name':'main','.type':'main',ipv6_mode:'direct'},geo:{'.name':'geo','.type':'rule',id:'geo',name:'Geo',preset_key:'geoip:ru',preset_variant:'ru',preset_variant_name:'RU',action:'direct',proxy_outage_action:'wan'},pack:{'.name':'pack','.type':'rule',id:'pack',name:'Pack',preset_key:'group:ru-government',preset_variant:'ru',preset_variant_name:'RU',action:'direct',proxy_outage_action:'wan'},torrent:{'.name':'torrent','.type':'rule',id:'torrent',name:'Torrent',preset_key:'traffic:bittorrent',preset_variant:'ru',preset_variant_name:'RU',action:'direct',proxy_outage_action:'wan'}};
  let uciSaves=0;
  const uci={loadPackage:async()=>{},load:async()=>{},get:(_c,s,o)=>o?config[s]?.[o]:config[s],sections:(_c,t)=>Object.values(config).filter(s=>s['.type']===t),resolveSID:(_c,s)=>s,set:(_c,s,o,v)=>{config[s][o]=v;},unset:(_c,s,o)=>{delete config[s][o];},save:async()=>{uciSaves++;}};
  const rpc={declare:()=>async()=>true}; const fs={exec:async()=>({code:0,stdout:JSON.stringify({choices:[{id:'ru-government',name:'Government',country:'ru'}],automatic_ips:[]})})};
  const poll={add:()=>{},remove:()=>{}}; const validation={create:()=>({validate:()=>true}),types:{}};
  const findParent=(node,selector)=>node.closest(selector);
  const ui=new (new Function('validation','baseclass','request','session','poll','dom','rpc','uci','fs','L','E','_','findParent',uiSource)(validation,baseclass,{}, {},poll,dom,rpc,uci,fs,L,E,_,findParent))();
  ui.addValidator=()=>()=>true; ui.tabs.initTabGroup=()=>{};ui.tabs.updateTabs=()=>{};ui.showTooltip=()=>{};
  const form=new (new Function('ui','uci','rpc','dom','baseclass','L','E','_',formSource)(ui,uci,rpc,dom,baseclass,L,E,_))();
  const safeDOM=new (new Function('baseclass','dom',sources.safe_dom)(baseclass,dom))();
  const Drafts=new (new Function('baseclass','ui','safeDOM','L','_',sources.drafts)(baseclass,ui,safeDOM,L,_))();
  let ruleMap;const attachDraft=Drafts.attach;
  Drafts.attach=function(map,page){if(page==='rulesets')ruleMap=map;return attachDraft.apply(this,arguments);};
  const IntervalValue=new (new Function('baseclass','form','safeDOM','_',sources.interval)(baseclass,form,safeDOM,_))();
  const rulesets=new (new Function('sbgTheme','view','Drafts','form','IntervalValue','uci','fs','ui','validation','poll','safeDOM','_',sources.rulesets)({decorate:n=>n},baseclass,Drafts,form,IntervalValue,uci,fs,ui,validation,poll,safeDOM,_))();
  const view=await rulesets.render(await rulesets.load());document.body.append(view);
  const turn=()=>new Promise(r=>setTimeout(r,30));
  const read=()=>({geo:view.querySelector('input[value="geoip:ru"]').checked,torrent:view.querySelector('[data-name="_preset_traffic"] input')?.checked,outage:view.querySelector('select[id$="._preset_outage_action"]')?.value,storage:JSON.parse(sessionStorage.getItem('resproxy.draft.fixture.preset-editor'))});
  const before=read();view.querySelector('.sbg-ready-variant').open=true;await turn();const after=read();await turn();
  const normal={before,after,later:read()};
  // A read-only visit may capture local form values on pagehide, but opening
  // them again must not stage defaults into the rpcd UCI session.
  window.dispatchEvent(new Event('pagehide'));view.remove();
  const savesBeforeViewing=uciSaves;
  const unchanged=await rulesets.render(await rulesets.load());document.body.append(unchanged);
  await new Promise(resolve=>setTimeout(resolve,800));
  if(uciSaves!==savesBeforeViewing)throw Error('Reopening unchanged stock form staged default settings');
  window.dispatchEvent(new Event('pagehide'));unchanged.remove();
  // Restore an open variant before any user edit has created its local draft.
  // A legacy page-wide draft must not override the per-variant editor.
  sessionStorage.setItem('resproxy.draft.fixture.rulesets',JSON.stringify({at:Date.now(),value:{fields:{'main._preset_outage_action':{value:'block',base:'wan'}}}}));
  const reloaded=await rulesets.render(await rulesets.load());document.body.append(reloaded);await turn();
  const restore={geo:reloaded.querySelector('input[value="geoip:ru"]').checked,torrent:reloaded.querySelector('[data-name="_preset_traffic"] input').checked,outage:reloaded.querySelector('select[id$="._preset_outage_action"]').value};
  const changedGeo=reloaded.querySelector('input[value="geoip:ru"]');changedGeo.checked=false;changedGeo.dispatchEvent(new Event('change',{bubbles:true}));
  const changedOutage=reloaded.querySelector('select[id$="._preset_outage_action"]');changedOutage.value='block';changedOutage.dispatchEvent(new Event('change',{bubbles:true}));
  const changedIPv6=reloaded.querySelector('select[id$=".ipv6_mode"]');changedIPv6.value='block';changedIPv6.dispatchEvent(new Event('change',{bubbles:true}));
  window.dispatchEvent(new Event('pagehide'));reloaded.remove();
  const withDraft=await rulesets.render(await rulesets.load());document.body.append(withDraft);
  const edited={geo:withDraft.querySelector('input[value="geoip:ru"]').checked,torrent:withDraft.querySelector('[data-name="_preset_traffic"] input').checked,outage:withDraft.querySelector('select[id$="._preset_outage_action"]').value};
  if(withDraft.querySelector('select[id$=".ipv6_mode"]').value!=='block')throw Error('Generic settings draft was lost');
  if(config.geo.proxy_outage_action!=='wan')throw Error('Viewing or drafting changed saved UCI rules');
  // Rules from UCI or older versions need not carry the preset-editor marker.
  // Saving an unrelated interval must keep their real match conditions.
  withDraft.remove();window.dispatchEvent(new Event('pagehide'));
  config.legacygeo={'.name':'legacygeo','.type':'rule',id:'legacy-geo',name:'Legacy country',action:'direct',country:'cn',geo_mode:'geoip'};
  config.legacypack={'.name':'legacypack','.type':'rule',id:'legacy-pack',name:'Legacy service',action:'direct',service_packs:['ru-government']};
  config.legacydomain={'.name':'legacydomain','.type':'rule',id:'legacy-domain',name:'Legacy domain',action:'direct',domains:['example.com']};
  config.legacycidr={'.name':'legacycidr','.type':'rule',id:'legacy-cidr',name:'Legacy network',action:'direct',cidrs:['203.0.113.0/24']};
  const legacyView=await rulesets.render(await rulesets.load());document.body.append(legacyView);
  await ruleMap.parse();
  if(config.legacygeo.country!=='cn'||config.legacygeo.geo_mode!=='geoip'||config.legacypack.service_packs[0]!=='ru-government')throw Error('Saving erased a legacy rule condition');
  legacyView.remove();window.dispatchEvent(new Event('pagehide'));
  config.emptyrule={'.name':'emptyrule','.type':'rule',id:'empty-rule',name:'Empty rule',action:'direct'};
  const emptyView=await rulesets.render(await rulesets.load());document.body.append(emptyView);
  let rejectedEmpty=false;try{await ruleMap.parse();}catch(error){rejectedEmpty=/Add a domain/.test(error.message);}
  if(!rejectedEmpty)throw Error('A genuinely empty custom rule passed validation');
  delete config.emptyrule;emptyView.remove();window.dispatchEvent(new Event('pagehide'));
  const instantiate=(source,deps)=>new (new Function(...Object.keys(deps),source)(...Object.values(deps)))();
  const monitor={externalDNS:async()=>'',sources:async()=>({})};
  config.main.dns_proxy_provider='quad9'; config.main.dns_direct_provider='yandex';config.main.dns_port='1153';
  const dns=instantiate(sources.dns,{sbgTheme:{decorate:n=>n},view:baseclass,form,uci,monitor,_});
  const dnsView=await dns.render(await dns.load()); document.body.append(dnsView);
  const dnsResult={proxy:dnsView.querySelector('select[id$=".dns_proxy_provider"]').value,direct:dnsView.querySelector('select[id$=".dns_direct_provider"]').value,port:dnsView.querySelector('input[id$=".dns_port"]').value};
  const subscription=instantiate(sources.subscription,{sbgTheme:{decorate:n=>n},view:baseclass,plugins:{render:()=>E('div')},form,IntervalValue,fs,uci,ui,rpc,poll,monitor,safeDOM,_});
  config.sub={'.name':'sub','.type':'subscription',id:'Test',url:'https://example.com/sub',fetch_via:'direct-only',update_mode:'daily',update_time:'12:34',max_nodes:'321'};
  const subView=await subscription.render(await subscription.load());document.body.append(subView);
  const subResult={mode:subView.querySelector('select[id$=".update_mode"]').value,time:subView.querySelector('input[id$=".update_time"]').value,via:subView.querySelector('select[id$=".fetch_via"]').value,limit:subView.querySelector('input[id$=".max_nodes"]').value};
  const nameFilter=instantiate(sources.name_filter,{baseclass,_});
  const locationPicker=instantiate(sources.location_picker,{baseclass,form,uci,safeDOM,nameFilter,_});
  const staticClients=instantiate(sources.static_clients,{baseclass,validation});
  const sharedMonitor=instantiate(sources.monitor,{baseclass,form,uci,_});
  config.route={'.name':'route','.type':'policy',id:'clients',mode:'priority',source_cidrs:['192.0.2.20/32'],selection_order:['all:*'],rule_refs:['geo']};
  fs.exec_direct=async()=>({nodes:[],locations:[],regions:{}});
  const routeRPC={declare:()=>async()=>config};
  const routes=instantiate(sources.routes,{sbgTheme:{decorate:n=>n},sharedMonitor,view:baseclass,Drafts,form,locationPicker,staticClients,uci,fs,ui,rpc:routeRPC,safeDOM,_});
  const routeView=await routes.render(await routes.load());document.body.append(routeView);await turn();
  const routeResult={mode:routeView.querySelector('select[id$=".mode"]').value,scope:routeView.querySelector('select[id$="._device_scope"]').value,refs:routeView.querySelector('.sbg-rule-order').textContent};
  // Every configuration view must wait for the real UCI read. Defaults may
  // never become an editable substitute while that read is still in flight.
  for(const model of [rulesets,dns,subscription,routes]) {
   const loadOriginal=uci.load;let release,rendered=false;
   const gate=new Promise(resolve=>{release=resolve;});uci.load=()=>gate;
   const loading=model.load().then(data=>{rendered=true;return data;});
   await turn();if(rendered)throw Error('Settings loaded before UCI was ready');
   release();await loading;uci.load=loadOriginal;
   uci.load=async()=>{throw Error('fixture UCI read failed');};
   let failed=false;try{await model.load();}catch(_){failed=true;}
   uci.load=loadOriginal;if(!failed)throw Error('Failed UCI read was replaced with defaults');
  }
  window.dispatchEvent(new Event('pagehide'));
  return {normal,restore,edited,dnsResult,subResult,routeResult};
 },{sources,classSource,domSource,formSource,uiSource});
 assert.deepEqual(errors,[]);
 assert.equal(result.normal.after.geo,true);assert.equal(result.normal.after.outage,'wan');
 assert.deepEqual(result.restore,{geo:true,torrent:true,outage:'wan'});
 assert.deepEqual(result.edited,{geo:false,torrent:true,outage:'block'});
 assert.deepEqual(result.dnsResult,{proxy:'quad9',direct:'yandex',port:'1153'});
 assert.deepEqual(result.subResult,{mode:'daily',time:'12:34',via:'direct-only',limit:'321'});
 assert.equal(result.routeResult.mode,'priority');assert.equal(result.routeResult.scope,'selected');
 assert.match(result.routeResult.refs,/Geo.*Direct WAN/);
 console.log('SETTINGS_SAVED_VALUES_REOPEN_OWN_DRAFTS_DELAYED_UCI_PASS');
}finally{await browser.close();await new Promise(r=>server.close(r));}
