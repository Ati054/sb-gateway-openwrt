// Usage: node tools/test-settings-dom.mjs luci.js form.js ui.js
// Uses real LuCI forms/widgets with isolated UCI and RPC fixtures.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import {createServer} from 'node:http';
const require=createRequire(import.meta.url);
const {chromium}=require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const dir=new URL('../htdocs/luci-static/resources/view/resproxy/',import.meta.url);
const names=['rulesets','drafts','safe_dom','interval','dns','routes','location_picker','name_filter','static_clients','monitor','subscription','unicode_text','entity_names','overview','node_health'];
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
  const uci={loadPackage:async()=>{},load:async()=>{},get:(_c,s,o)=>o?config[s]?.[o]:config[s],sections:(_c,t)=>Object.values(config).filter(s=>s['.type']===t),resolveSID:(_c,s)=>s,set:(_c,s,o,v)=>{config[s][o]=v;},unset:(_c,s,o)=>{delete config[s][o];},remove:(_c,s)=>{delete config[s];},save:async()=>{uciSaves++;}};
  let validationReply={code:0,stdout:JSON.stringify({valid:true})};const validationCalls=[];
  const rpc={declare:()=>async()=>true}; const fs={exec:async(path,args)=>{
   if(path==='/usr/libexec/resproxy-validate-rule'){validationCalls.push(JSON.parse(args[0]));return validationReply;}
   return {code:0,stdout:JSON.stringify({choices:[{id:'ru-government',name:'Government',country:'ru'}],automatic_ips:[]})};
  }};
  const poll={add:()=>{},remove:()=>{}}; const validation={create:()=>({validate:()=>true}),types:{}};
  const findParent=(node,selector)=>node.closest(selector);
  const ui=new (new Function('validation','baseclass','request','session','poll','dom','rpc','uci','fs','L','E','_','findParent',uiSource)(validation,baseclass,{}, {},poll,dom,rpc,uci,fs,L,E,_,findParent))();
  ui.addValidator=()=>()=>true; ui.tabs.initTabGroup=()=>{};ui.tabs.updateTabs=()=>{};ui.showTooltip=()=>{};
  const form=new (new Function('ui','uci','rpc','dom','baseclass','L','E','_',formSource)(ui,uci,rpc,dom,baseclass,L,E,_))();
  const safeDOM=new (new Function('baseclass','dom',sources.safe_dom)(baseclass,dom))();
  const unicodeText=new (new Function('baseclass',sources.unicode_text)(baseclass))();
  const entityNames=new (new Function('baseclass','uci','unicodeText','_',sources.entity_names)(baseclass,uci,unicodeText,_))();
  const Drafts=new (new Function('baseclass','ui','safeDOM','L','_',sources.drafts)(baseclass,ui,safeDOM,L,_))();
  let ruleMap,routeMap;const attachDraft=Drafts.attach;
  Drafts.attach=function(map,page){if(page==='rulesets')ruleMap=map;if(page==='routes')routeMap=map;return attachDraft.apply(this,arguments);};
  const IntervalValue=new (new Function('baseclass','form','safeDOM','_',sources.interval)(baseclass,form,safeDOM,_))();
  const rulesets=new (new Function('sbgTheme','view','Drafts','form','IntervalValue','uci','fs','ui','validation','poll','safeDOM','_','unicodeText',sources.rulesets)({decorate:n=>n},baseclass,Drafts,form,IntervalValue,uci,fs,ui,validation,poll,safeDOM,_,unicodeText))();
  // Custom rules show outage behavior for proxy actions. Its stored default
  // is WAN even when a new direct rule initially hides the dropdown.
  const existingRules=Object.fromEntries(Object.entries(config).filter(([,v])=>v['.type']==='rule'));
  for(const key of Object.keys(existingRules))delete config[key];
  let defaultsView=await rulesets.render(await rulesets.load());document.body.append(defaultsView);
  if(defaultsView.querySelector('select[id$="._preset_outage_action"]').value!=='wan')throw Error('New preset did not default to WAN');
  const groups=ruleMap.children.find(s=>s.sectiontype==='rule');
  const outageOption=groups.children.find(o=>o.option==='proxy_outage_action');
  if(outageOption.default!=='wan')throw Error('New custom rule did not default to WAN');
  if(!outageOption.deps?.some(dependency=>dependency.action==='proxy'))throw Error('Custom outage dropdown lost its proxy-action dependency');
  uci.add=(_package,type)=>{config.new_default={'.name':'new_default','.type':type};return 'new_default';};
  await groups.handleAdd();
  if(config.new_default.proxy_outage_action!=='wan')throw Error('Creating a custom rule did not persist its WAN default');
  delete config.new_default;delete uci.add;
  defaultsView.remove();
  Object.assign(config,existingRules);
  config.strict={'.name':'strict','.type':'rule',id:'strict',name:'Strict',action:'proxy',domains:'strict.test',proxy_outage_action:'block'};
  defaultsView=await rulesets.render(await rulesets.load());document.body.append(defaultsView);
  const strictChoice=defaultsView.querySelector('select[id$=".strict.proxy_outage_action"]');
  if(!strictChoice || strictChoice.value!=='block')throw Error('Saved explicit block replaced with WAN');
  const actionChoice=defaultsView.querySelector('select[id$=".strict.action"]');
  const strictOutage=ruleMap.children.find(s=>s.sectiontype==='rule').children.find(o=>o.option==='proxy_outage_action');
  actionChoice.value='direct';ruleMap.checkDepends();
  if(strictOutage.isActive('strict'))throw Error('Direct custom rule displays proxy outage behavior');
  actionChoice.value='proxy';ruleMap.checkDepends();
  if(!strictOutage.isActive('strict') || strictChoice.value!=='block')throw Error('Returning to proxy lost the custom outage choice');
  await ruleMap.save();
  if(config.strict.proxy_outage_action!=='block')throw Error('Saving the form changed explicit block');
  defaultsView.remove();delete config.strict;
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
  // Exercise the asynchronous native validation boundary before UCI save.
  // Native grammar and response serialization are tested in Go; these fixtures
  // deliberately do not implement a second regex/IP parser in JavaScript.
  const ruleInput=(sid,key)=>ruleMap.root.querySelector('textarea[id$=".'+sid+'.'+key+'"]');
  const inputCIDR=ruleInput('legacycidr','cidrs');
  if(inputCIDR.value!=='203.0.113.0/24')throw Error('UCI list did not reopen as one expression per line');
  inputCIDR.value='195.82.146.214\n2001:db8::1';
  ruleInput('legacydomain','domains').value='full:ПРИМЕР.РФ\nregexp:(?i)^Google\\.com$';
  await ruleMap.save();
  if(!validationCalls.some(c=>c.cidrs?.includes('195.82.146.214')&&c.cidrs.includes('2001:db8::1'))||!validationCalls.some(c=>c.domains?.includes('regexp:(?i)^Google\\.com$')))throw Error('Save skipped native validation of IP or Go regex');
  const expectBlockedSave=async(value,reply)=>{
   ruleInput('legacydomain','domains').value=value;validationReply=reply;
   const before=uciSaves;let rejected=false;try{await ruleMap.save();}catch(_){rejected=true;}
   if(!rejected||uciSaves!==before)throw Error('Invalid or unverified rule reached UCI save: '+value);
  };
  await expectBlockedSave('https://example.com',{code:0,stdout:JSON.stringify({valid:false,field:'domains',index:0,error:'invalid expression'})});
  await expectBlockedSave('regexp:(?=google)',{code:0,stdout:JSON.stringify({valid:false,field:'domains',index:0,error:'unsupported syntax'})});
  await expectBlockedSave('keyword:яндекс',{code:0,stdout:'{}'});
  await expectBlockedSave('unverified.test',{code:1,stdout:''});
  await expectBlockedSave('malformed-reply.test',{code:0,stdout:'{}'});
  // A value (or the set of custom rows) may change while the real RPC is
  // pending. The checked snapshot must still be the one reaching UCI save.
  for(const mutation of ['domain','cidr','add-row','remove-row']) {
   ruleInput('legacydomain','domains').value='regexp:^pending-'+mutation+'$';
   ruleInput('legacycidr','cidrs').value='195.82.146.214\n2001:db8::1';
   let releaseValidation;validationReply=new Promise(resolve=>{releaseValidation=resolve;});
   const callsBefore=validationCalls.length,savesBefore=uciSaves;
   const pending=ruleMap.save().then(()=>null,error=>error);
   for(let wait=0;validationCalls.length===callsBefore&&wait<10;wait++)await turn();
   if(validationCalls.length===callsBefore)throw Error('Delayed validation fixture did not reach native RPC');
   const savedCIDR=config.legacycidr;
   if(mutation==='domain')ruleInput('legacydomain','domains').value='regexp:[';
   if(mutation==='cidr')ruleInput('legacycidr','cidrs').value='195.82.146.214/99';
   if(mutation==='add-row')config.pendingrule={'.name':'pendingrule','.type':'rule',id:'pending-rule',name:'Pending',action:'direct',domains:'regexp:['};
   if(mutation==='remove-row')delete config.legacycidr;
   releaseValidation({code:0,stdout:JSON.stringify({valid:true})});
   const error=await pending;
   delete config.pendingrule;config.legacycidr=savedCIDR;
   if(!error||uciSaves!==savesBefore)throw Error('Async native validation saved an unchecked '+mutation);
   if(mutation==='domain'&&ruleInput('legacydomain','domains').value!=='regexp:[')throw Error('Rejected validation race discarded the current domain edit');
   if(mutation==='cidr'&&ruleInput('legacycidr','cidrs').value!=='195.82.146.214/99')throw Error('Rejected validation race discarded the current IP edit');
  }
  validationReply={code:0,stdout:JSON.stringify({valid:true})};
  ruleInput('legacycidr','cidrs').value='195.82.146.214\n2001:db8::1';
  ruleInput('legacydomain','domains').value='domain:яндекс.рф';await ruleMap.save();
  legacyView.remove();window.dispatchEvent(new Event('pagehide'));
  config.emptyrule={'.name':'emptyrule','.type':'rule',id:'empty-rule',name:'Empty rule',action:'direct'};
  const emptyView=await rulesets.render(await rulesets.load());document.body.append(emptyView);
  let rejectedEmpty=false;try{await ruleMap.parse();}catch(error){rejectedEmpty=/Add a domain/.test(error.message);}
  if(!rejectedEmpty)throw Error('A genuinely empty custom rule passed validation');
  delete config.emptyrule;emptyView.remove();window.dispatchEvent(new Event('pagehide'));
  const instantiate=(source,deps)=>new (new Function(...Object.keys(deps),source)(...Object.values(deps)))();
  const monitor={externalDNS:async()=>'',sources:async()=>({})};
  config.main.dns_proxy_provider='quad9'; config.main.dns_direct_provider='yandex';config.main.dns_direct_protocol='doh';config.main.dns_port='1153';
  let dnsMap;
  const dnsForm=Object.assign(Object.create(form),{Map:function(...args){dnsMap=new form.Map(...args);return dnsMap;}});
  const dns=instantiate(sources.dns,{sbgTheme:{decorate:n=>n},view:baseclass,form:dnsForm,uci,monitor,_});
  const dnsView=await dns.render(await dns.load()); document.body.append(dnsView);
  const dnsResult={proxy:dnsView.querySelector('select[id$=".dns_proxy_provider"]').value,direct:dnsView.querySelector('select[id$=".dns_direct_provider"]').value,port:dnsView.querySelector('input[id$=".dns_port"]').value};
  if(Array.from(dnsView.querySelectorAll('option')).some(option=>option.textContent.includes('Plain DNS')))throw Error('DNS protocol choices still contain extra labels');
  if(Array.from(dnsView.querySelector('select[id$=".dns_proxy_provider"]').options).some(option=>option.value==='isp'))throw Error('ISP DNS was offered for the proxy path');
  const ispChoice=dnsView.querySelector('select[id$=".dns_direct_provider"]');
  ispChoice.value='isp';ispChoice.dispatchEvent(new Event('change',{bubbles:true}));dnsMap.checkDepends();await turn();
  const wanProtocol=dnsView.querySelector('[data-name="dns_direct_protocol"]');
  if(!wanProtocol?.classList.contains('hidden'))throw Error('ISP DNS still asks for an incompatible encryption protocol');
  await dnsMap.parse();
  if(config.main.dns_direct_provider!=='isp'||config.main.dns_direct_protocol!=='doh')throw Error('Saving ISP DNS lost its selection or previous protocol');
  dnsView.remove();
  const ispView=await dns.render(await dns.load());document.body.append(ispView);await turn();
  if(ispView.querySelector('select[id$=".dns_direct_provider"]').value!=='isp'||!ispView.querySelector('[data-name="dns_direct_protocol"]').classList.contains('hidden'))throw Error('Saved ISP DNS did not reopen correctly');
  const restoreProvider=ispView.querySelector('select[id$=".dns_direct_provider"]');restoreProvider.value='yandex';restoreProvider.dispatchEvent(new Event('change',{bubbles:true}));dnsMap.checkDepends();await turn();
  if(ispView.querySelector('[data-name="dns_direct_protocol"]').classList.contains('hidden')||ispView.querySelector('select[id$=".dns_direct_protocol"]').value!=='doh')throw Error('Returning from ISP DNS lost the selected protocol');
  const plainProtocol=ispView.querySelector('select[id$=".dns_direct_protocol"]');
  if(Array.from(plainProtocol.options).filter(option=>option.value==='dns'&&option.textContent==='DNS (UDP/TCP)').length!==1||Array.from(plainProtocol.options).some(option=>option.value==='udp'||option.value==='tcp'))throw Error('WAN plain DNS was not combined into one automatic choice');
  plainProtocol.value='dns';plainProtocol.dispatchEvent(new Event('change',{bubbles:true}));await dnsMap.parse();
  if(config.main.dns_direct_protocol!=='dns')throw Error('Automatic WAN DNS choice was not saved');
  ispView.remove();
  for(const legacy of ['tcp','udp']){
   config.main.dns_direct_protocol=legacy;
   const legacyDNS=await dns.render(await dns.load());document.body.append(legacyDNS);await turn();
   if(legacyDNS.querySelector('select[id$=".dns_direct_protocol"]').value!=='dns')throw Error('Saved plain DNS did not reopen with automatic transport: '+legacy);
   legacyDNS.remove();
  }
  let subMap;
  const subForm=Object.assign(Object.create(form),{Map:function(...args){subMap=new form.Map(...args);return subMap;}});
  const subscription=instantiate(sources.subscription,{sbgTheme:{decorate:n=>n},view:baseclass,plugins:{render:()=>E('div')},form:subForm,IntervalValue,fs,uci,ui,rpc,poll,monitor,safeDOM,entityNames,_});
  config.sub={'.name':'sub','.type':'subscription',id:'Test',url:'https://example.com/sub',fetch_via:'direct-only',update_mode:'daily',update_time:'12:34',max_nodes:'321'};
  const subView=await subscription.render(await subscription.load());document.body.append(subView);
  const subResult={mode:subView.querySelector('select[id$=".update_mode"]').value,time:subView.querySelector('input[id$=".update_time"]').value,via:subView.querySelector('select[id$=".fetch_via"]').value,countCap:!!subView.querySelector('input[id$=".max_nodes"]')};
  if(!subView.textContent.includes('All supported nodes are imported'))throw Error('Full-catalogue import is not explained');
  const nameFilter=instantiate(sources.name_filter,{baseclass,unicodeText,_});
  const locationPicker=instantiate(sources.location_picker,{baseclass,form,uci,safeDOM,nameFilter,entityNames,_});
  const staticClients=instantiate(sources.static_clients,{baseclass,validation});
  const sharedMonitor=instantiate(sources.monitor,{baseclass,form,uci,_});
  config.route={'.name':'route','.type':'policy',id:'clients',mode:'priority',source_cidrs:['192.0.2.20/32'],selection_order:['all:*'],rule_refs:['geo']};
  fs.exec_direct=async()=>({nodes:[],locations:[],regions:{}});
  const routeRPC={declare:()=>async()=>config};
  const routes=instantiate(sources.routes,{sbgTheme:{decorate:n=>n},sharedMonitor,view:baseclass,Drafts,form,locationPicker,staticClients,uci,fs,ui,rpc:routeRPC,safeDOM,entityNames,_});
  const routeView=await routes.render(await routes.load());document.body.append(routeView);await turn();
  const routeResult={mode:routeView.querySelector('select[id$=".mode"]').value,scope:routeView.querySelector('select[id$="._device_scope"]').value,refs:routeView.querySelector('.sbg-rule-order').textContent};
  const findOption=(map,type,key)=>map.children.find(s=>s.sectiontype===type).children.find(o=>o.option===key);
  const subName=findOption(subMap,'subscription','name'),routeName=findOption(routeMap,'policy','name');
  const labels=['Рабочая ПОДПИСКА','我的订阅','اشتراك المنزل','اشتراک خانه','मेरा नेटवर्क','Δίκτυο ΣΠΙΤΙ','Straße München','Café 🔒'];
  const subInput=subView.querySelector('input[id$=".name"]'),routeInput=routeView.querySelector('input[id$=".name"]');
  if(subView.querySelector('input[id$=".id"]') || routeView.querySelector('input[id$=".id"]'))throw Error('Technical ID is still exposed as an editable name');
  for(const label of labels) {
   if(subName.validate('sub',label)!==true || routeName.validate('route',label)!==true)throw Error('Human name rejected: '+label);
   subInput.value=label;await subMap.parse();routeInput.value=label;await routeMap.parse();
   if(config.sub.id!=='Test' || config.route.id!=='clients' || config.sub.name!==label || config.route.name!==label || config.route.rule_refs[0]!=='geo')throw Error('Renaming changed identity or references: '+label);
  }
  if(subName.validate('sub','🔒'.repeat(120))!==true || subName.validate('sub','🔒'.repeat(121))===true || subName.validate('sub','bad\u0085name')===true)throw Error('Unicode name length/control validation is inconsistent');
  config.newsub={'.name':'newsub','.type':'subscription',url:'https://example.com/new',name:'Новая подписка'};
  const newSourceView=await subscription.render(await subscription.load());document.body.append(newSourceView);
  const newInput=newSourceView.querySelector('input[id$="newsub.name"]');newInput.value='中国 🔒 Подписка';
  newSourceView.querySelector('input[id$="newsub.url"]').value='HTTPS://ПРИМЕР.РФ/Подписка';
  await subMap.parse();const generated=config.newsub.id;
  if(!/^[a-z][a-z0-9-]{0,31}$/.test(generated) || config.newsub.name!=='中国 🔒 Подписка' || new URL(config.newsub.url).hostname!=='xn--e1afmkfd.xn--p1ai')throw Error('New international source did not get a valid stable ID or URL');
  newInput.value='Другое ИМЯ';await subMap.parse();if(config.newsub.id!==generated)throw Error('New source ID changed on rename');
  const healthUI=instantiate(sources.node_health,{baseclass,safeDOM,nameFilter,entityNames,_});
  const healthView=healthUI.create([{id:'clients',nodes:[],candidate_count:0,healthy_reserves:0}]);
  if(healthView.node.querySelector('option').textContent!==config.route.name)throw Error('Node health exposed a route ID');
  config.route.name='Домашний маршрут';healthView.update([{id:'clients',nodes:[],candidate_count:0,healthy_reserves:0}]);
  if(healthView.node.querySelector('option').textContent!==config.route.name)throw Error('Node health did not update a renamed route');
  const reopenedRoute=await routes.render(await routes.load());document.body.append(reopenedRoute);
  if(!reopenedRoute.querySelector('summary').textContent.includes('Домашний маршрут') || reopenedRoute.querySelector('input[id$=".name"]').value!=='Домашний маршрут')throw Error('Human route name did not survive reopen');
  const sourceSelect=reopenedRoute.querySelector('select[aria-label="Filter subscription"]');
  if(!sourceSelect || !Array.from(sourceSelect.options).some(o=>o.value===generated && o.textContent==='Другое ИМЯ'))throw Error('Subscription picker lost human names or used them as IDs');
  // Source deletion must not broaden a filtered route or alter strict policy.
  config.dep={'.name':'dep','.type':'policy',id:'dep',selection_order:['all:*'],selection_source:generated,source_cidrs:['192.0.2.42/32'],rule_refs:['strict'],proxy_outage_action:'block'};
  config.manual={'.name':'manual','.type':'policy',id:'manual',selection_order:['subscription:'+encodeURIComponent(generated),'subscription:Test','country:fr']};
  const unrelated=JSON.stringify(config.route);
  config.main.provider_id=generated;config.main.subscription_url=config.newsub.url;
  const duplicateView=await subscription.render(await subscription.load());document.body.append(duplicateView);
  if(!subMap.root.querySelector('input[id$=".main.subscription_url"]')?.value)throw Error('Duplicate legacy source fixture has no rendered field');
  await subMap.children.find(s=>s.sectiontype==='subscription').handleRemove('newsub');
  if(config.newsub || config.main.subscription_url || config.dep.selection_source || config.dep.selection_order?.length || config.dep.proxy_outage_action!=='block' || config.dep.source_cidrs[0]!=='192.0.2.42/32' || config.dep.rule_refs[0]!=='strict')throw Error('Deleted source was retained, broadened, or changed strict policy');
  if(JSON.stringify(config.manual.selection_order)!==JSON.stringify(['subscription:Test','country:fr'])||JSON.stringify(config.route)!==unrelated)throw Error('Source deletion changed unrelated selectors or route');
  config.newsub={'.name':'newsub','.type':'subscription',id:generated,url:'https://example.com/modern'};
  config.main.subscription_url='https://example.com/different-legacy';
  const distinctView=await subscription.render(await subscription.load());document.body.append(distinctView);
  await subMap.children.find(s=>s.sectiontype==='subscription').handleRemove('newsub');
  if(config.main.subscription_url!=='https://example.com/different-legacy')throw Error('Deleting a modern source erased a different legacy source');
  delete config.main.subscription_url;delete config.main.provider_id;delete config.dep;delete config.manual;
  monitor.snapshot=async()=>({status:{running:false},health:[]});
  const overview=instantiate(sources.overview,{sbgTheme:{decorate:n=>n},healthUI:{create:()=>({node:E('div'),update:()=>{}}),latency:()=>'—'},view:baseclass,form,fs,uci,poll,safeDOM,monitor,entityNames,_});
  const renderOverview=async()=>{const node=await overview.render(await overview.load());document.body.append(node);return node;};
  const checkbox=node=>node.querySelector('input[type="checkbox"]');
  const savedRoute=config.route;delete config.route;
  let overviewView=await renderOverview();
  const hint=overviewView.querySelector('#'+CSS.escape(checkbox(overviewView).getAttribute('aria-describedby')));
  if(!checkbox(overviewView).disabled || !hint || hint.textContent!=='First add a subscription, a rule and a route list.')throw Error('First activation lacks disabled checkbox or accessible setup hint');
  checkbox(overviewView).click();if(checkbox(overviewView).checked)throw Error('Disabled first activation remained clickable');overviewView.remove();
  config.route={...savedRoute,selection_order:[]};overviewView=await renderOverview();
  if(!checkbox(overviewView).disabled)throw Error('Unfinished route enabled activation');overviewView.remove();
  config.main.enabled='1';overviewView=await renderOverview();
  if(checkbox(overviewView).disabled || !checkbox(overviewView).checked)throw Error('Unfinished setup prevented stopping an enabled service');
  checkbox(overviewView).click();if(checkbox(overviewView).checked)throw Error('Enabled service could not be switched off');overviewView.remove();
  config.main.enabled='0';config.route=savedRoute;overviewView=await renderOverview();
  if(checkbox(overviewView).disabled || !overviewView.textContent.includes('Домашний маршрут'))throw Error('Ready human-named route retained activation block or exposed an ID');overviewView.remove();
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
 assert.deepEqual(result.subResult,{mode:'daily',time:'12:34',via:'direct-only',countCap:false});
 assert.equal(result.routeResult.mode,'priority');assert.equal(result.routeResult.scope,'selected');
 assert.match(result.routeResult.refs,/Geo.*Direct WAN/);
 console.log('SETTINGS_SAVED_VALUES_REOPEN_OWN_DRAFTS_DELAYED_UCI_PASS');
}finally{await browser.close();await new Promise(r=>server.close(r));}
