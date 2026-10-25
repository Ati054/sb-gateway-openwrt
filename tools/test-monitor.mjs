import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
const source=await readFile(new URL('../htdocs/luci-static/resources/view/resproxy/runtime_status.js',import.meta.url),'utf8');
const now=Date.now(),second=Math.floor(now/1000);
const root='/var/run/resproxy/';
const node=(id,delay,at,extra={})=>({id,available:true,latest_delay_ms:delay,latest_quality_at:at,...extra});
let fixtures={
 'health-state/monitor.json':{updated_at:new Date(now).toISOString(),health:[{id:'main',probe_limits:{active_seconds:60,backup_seconds:180},nodes:[node('a',450,second-10),node('b',300,second-20),node('stale',100,second-180),node('future',100,second+10),node('failed',200,second-10,{available:false})]}]},
 'health-state/selector-runtime.json':{updated_at:new Date(now).toISOString(),policies:{'resproxy-main':{selected:'a',runtime_selected:'a',runtime_confirmed:true}}},
 'catalog-summary.json':{nodes:842,counts:{real:842}},'subscription-status.json':{real:{nodes:0,state:'success'},removed:{nodes:99}},
 'resources.json':{available_kib:90000},'dns-status.json':{queries:42},'dns-interop.json':{effective_mode:'proxy-only'}
};
let running=true,reads=[],coldReads=0;
let boot=1000;
const missing=()=>Object.assign(new Error('not found'),{name:'NotFoundError'});
const get=path=>{assert(path.startsWith(root));const name=path.slice(root.length);reads.push(name);if (!(name in fixtures))throw missing();return structuredClone(fixtures[name]);};
const fs={read:async path=>path==='/proc/uptime' ? String(boot)+' 1234' : JSON.stringify(get(path)),read_direct:async(path,type)=>{assert.equal(type,'json');return get(path);},exec:async command=>{assert.equal(command,'/usr/libexec/resproxy-status');coldReads++;return {code:0,stdout:JSON.stringify({nodes:0,subscriptions:{}})};},exec_direct:()=>assert.fail('CLI execution')};
const rpc={declare:options=>{assert.equal(options.object,'service');assert.equal(options.method,'list');return async()=>({resproxy:{instances:{gateway:{running}}},'https-dns-proxy':{instances:{main:{running:true}}}});}};
const monitor=new Function('baseclass','fs','rpc',source)({extend:v=>v},fs,rpc);
let result=await monitor.snapshot();
assert.equal(result.status.running,true);assert.equal(result.status.nodes,842);assert.equal(result.status.dns.queries,42);assert.equal(result.status.subscriptions.real.nodes,842);assert.equal(result.status.subscriptions.removed.nodes,0);
assert(!reads.some(name=>/selector-health|secrets|catalog\.json/.test(name)));
let nodes=result.health[0].nodes;
assert.equal(nodes[0].latency_delta_percent,0);assert(Math.abs(nodes[1].latency_delta_percent-100/3)<1e-10);
for(const n of nodes.slice(2))assert.equal(n.latency_delta_percent,null);
fixtures['health-state/selector-runtime.json'].policies['resproxy-main'].runtime_selected='b';
result=await monitor.snapshot();assert.equal(result.health[0].nodes[0].latency_delta_percent,-50);assert.equal(result.health[0].nodes[1].selected,true);
fixtures['health-state/selector-runtime.json'].updated_at=new Date(now-21000).toISOString();
result=await monitor.snapshot();assert.deepEqual(result.status.policies,{});assert(result.health[0].nodes.every(n=>!n.selected && n.latency_delta_percent===null));
fixtures['health-state/selector-runtime.json'].updated_at=new Date(now).toISOString();running=false;
result=await monitor.snapshot();assert.equal(result.status.running,false);assert.deepEqual(result.status.policies,{});assert(result.health[0].nodes.every(n=>!n.selected));
assert.equal(await monitor.externalDNS(),'https-dns-proxy');assert.equal((await monitor.sources()).real.nodes,842);
running=true;
const runtime=fixtures['health-state/selector-runtime.json'];
runtime.updated_uptime_seconds=999;
fixtures['resources.json']={updated_at:runtime.updated_at,updated_uptime_seconds:998};
const originalNow=Date.now;
for(const skew of [-7*3600000,7*3600000]) {
 Date.now=()=>now+skew;
 result=await monitor.snapshot();
 assert.equal(result.status.policies['resproxy-main'].runtime_selected,'b','PC clock hid a confirmed selector');
 assert.equal(result.status.resources_fresh,true,'PC clock hid process memory');
 assert.equal(result.health[0].nodes[1].latency_delta_percent,0,'PC clock invalidated current measurements');
 assert(Math.abs(result.status.now-(now+1000))<2);
}
Date.now=originalNow;
runtime.updated_at=new Date(now-86400000).toISOString();
result=await monitor.snapshot();assert(result.status.policies['resproxy-main'],'NTP clock step revoked monotonic lease');
boot=1021;
result=await monitor.snapshot();assert.deepEqual(result.status.policies,{});assert.equal(result.status.runtime_stale,true);
boot=1040;
result=await monitor.snapshot();assert.equal(result.status.resources_fresh,false,'hung sampler stayed fresh');
boot=900;
result=await monitor.snapshot();assert.deepEqual(result.status.policies,{});assert.equal(result.status.resources_fresh,false,'future boot timestamp accepted');
assert.equal(coldReads,0);
fixtures={};result=await monitor.snapshot();assert.equal(result.status.nodes,0);assert.deepEqual(result.health,[]);
for(let i=0;i<5;i++)await monitor.snapshot();
await monitor.sources();assert.equal(coldReads,1,'cold disabled service reloaded catalog during polling');
for(const name of ['overview','subscription','dns']) {const page=await readFile(new URL('../htdocs/luci-static/resources/view/resproxy/'+name+'.js',import.meta.url),'utf8');assert(!/fs\.exec(?:_direct)?\(['"]\/usr\/libexec\/resproxy-(?:status|health)/.test(page),name+' still spawns status/health');}
const acl=JSON.parse(await readFile(new URL('../files/luci-app-resproxy-acl.json',import.meta.url),'utf8'))['luci-app-resproxy'];
assert.deepEqual(acl.read.ubus.service,['list']);
assert.deepEqual(acl.read.file['/proc/uptime'],['read']);
for(const name of reads) {assert.deepEqual(acl.read.file[root+name],['read']);assert.deepEqual(acl.read.file['/tmp/run/resproxy/'+name],['read']);}
assert(!Object.keys(acl.read.file).some(path=>path.includes('selector-health') || path.includes('secrets')));
console.log('MONITOR_NO_CLI_HISTORY_ACCESS_FRESH_DELTAS_SELECTION_LEASE_STOP_COUNTS_ACL_PASS');
