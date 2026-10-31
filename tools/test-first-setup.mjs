import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {isIP} from 'node:net';

const directory=new URL('../htdocs/luci-static/resources/view/resproxy/',import.meta.url);
const draftSource=await readFile(new URL('drafts.js',directory),'utf8');
const staticSource=await readFile(new URL('static_clients.js',directory),'utf8');
const turn=()=>new Promise(resolve=>setImmediate(resolve));

function fixture(storage=new Map()) {
 let value='old', server='old', invalid=false, applying=0, delay=null, reports=0;
 const listeners={}, events={};
 const control={disabled:false,isConnected:true};
 const root={querySelectorAll:()=>[control],addEventListener:(type,fn)=>{events[type]=fn;}};
 const window={addEventListener:(type,fn)=>{listeners[type]=fn;},location:{href:'https://router/routes',pathname:'/routes',origin:'https://router',assign:url=>{window.assigned=url;}}};
 const document={addEventListener:(type,fn)=>{listeners[type]=fn;}};
 const sessionStorage={getItem:key=>storage.get(key)||null,setItem:(key,value)=>storage.set(key,value),removeItem:key=>storage.delete(key)};
 const ui={addNotification:()=>{reports++;},changes:{apply:async()=>{applying++;}}};
 const module=new Function('baseclass','ui','L','safeDOM','_','sessionStorage','document','window',draftSource)(
  {extend:value=>value},ui,{env:{sessionid:'test'}},{create:(...args)=>args},value=>value,sessionStorage,document,window);
 const field={option:'id',cbid:()=> 'id',cfgvalue:()=> 'old',formvalue:()=>value,renderWidget:(_sid,_index,value)=>value};
 let staged;
 const map={children:[{cfgsections:()=>['main'],children:[field]}],findElement:()=>control,checkDepends:()=>{},parse:async()=>{
  if(invalid)throw Error('incomplete selection');
  staged=field.cfgvalue('main')!==value?value:server;
 },data:{save:async()=>{if(delay)await delay;server=staged;}},save:async()=>root};
 const draft=module.attach(map,'routes');
 value=field.renderWidget('main',0,server);draft.bind(root);
 return {module,ui,map,window,storage,invalid:next=>{invalid=next;},set:next=>{value=next;events.input();},get:()=>value,server:()=>server,
  applied:()=>applying,reports:()=>reports,delay:promise=>{delay=promise;},leave:()=>listeners.pagehide(),
  navigate:()=>{
   let prevented=false;
   listeners.click({target:{closest:()=>({href:'https://router/overview',target:'',hasAttribute:()=>false})},button:0,preventDefault:()=>{prevented=true;}});
   return prevented;
  }};
}

// Reopening an untouched page must not queue a save of its default values.
const untouched=fixture();untouched.leave();
const viewedAgain=fixture(untouched.storage);
assert.equal(viewedAgain.navigate(),false,'Viewing unchanged settings queued a draft write');
viewedAgain.leave();
const oldDraft=new Map([['resproxy.draft.test.routes',JSON.stringify({at:Date.now(),value:{fields:{'main.id':{base:'old',value:'legacy edit'}}}})]]);
const legacy=fixture(oldDraft);assert.equal(legacy.get(),'legacy edit');
assert.equal(legacy.navigate(),true);await turn();await turn();assert.equal(legacy.server(),'legacy edit');legacy.leave();

// A half-configured list must not trap the user or apply an invalid route.
let f=fixture();f.set('unfinished');f.invalid(true);
assert.equal(f.navigate(),true);await turn();await turn();
assert.equal(f.window.assigned,'https://router/overview');
assert.equal(f.server(),'old');assert.equal(f.applied(),0);assert.equal(f.reports(),0);
assert.equal(f.module.read('routes').fields['main.id'].value,'unfinished');
await assert.rejects(f.ui.changes.apply(),/incomplete selection/);
assert.equal(f.applied(),0);assert(f.reports()>0);f.leave();
const restored=fixture(f.storage);assert.equal(restored.get(),'unfinished');restored.leave();

// Valid edits still reach the UCI RAM session before normal navigation.
f=fixture();f.set('ready');f.navigate();await turn();await turn();
assert.equal(f.server(),'ready');assert.equal(f.window.assigned,'https://router/overview');assert.equal(f.applied(),0);f.leave();

// A pending server write and a subsequent edit are serialized before Apply.
f=fixture();let release;f.delay(new Promise(resolve=>{release=resolve;}));f.set('first');
const applying=f.ui.changes.apply();await turn();f.set('second');f.delay(null);release();await applying;
assert.equal(f.server(),'second');assert.equal(f.applied(),1);f.leave();

const clients=new Function('baseclass','validation',staticSource)({extend:value=>value},{parseIPv4:value=>isIP(value)===4,parseIPv6:value=>isIP(value)===6});
assert.deepEqual(clients.choices([
 {name:'PC',ip:'192.0.2.20'}, {name:'duplicate',ip:'192.0.2.20'},
 {name:'disabled',ip:'192.0.2.21',disabled:'1'}, {name:'invalid',ip:'ignore'},
 {name:'bad',ip:'192.0.2.999'}, {name:'suffix',ip6_addr:'::10'},
 {name:'IPv6',ip6_addr:'2001:db8::20'}, {mac:['00:11:22:33:44:55'],ip:'192.0.2.22'}
]).map(item=>[item.cidr,item.name]),[
 ['192.0.2.22/32','00:11:22:33:44:55'],['2001:db8::20/128','IPv6'],['192.0.2.20/32','PC']
]);
const defaults=await readFile(new URL('../files/resproxy.config',import.meta.url),'utf8');
assert.doesNotMatch(defaults,/^config policy\b/m);
assert.match(defaults,/option enabled '0'/);
const acl=JSON.parse(await readFile(new URL('../files/luci-app-resproxy-acl.json',import.meta.url),'utf8'))['luci-app-resproxy'];
assert(acl.read.uci.includes('dhcp'));assert(!acl.write.uci.includes('dhcp'));
console.log('FIRST_SETUP_NAVIGATION_DRAFT_APPLY_STATIC_CLIENTS_DEFAULTS_ACL_PASS');
