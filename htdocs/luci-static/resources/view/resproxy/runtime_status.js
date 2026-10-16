'use strict';
'require baseclass';
'require fs';
'require rpc';

var root='/var/run/resproxy/';
var services=rpc.declare({object:'service',method:'list',params:['name'],expect:{'':{}},reject:true});
function read(name,large) {
 var request=large ? fs.read_direct(root+name,'json') : fs.read(root+name).then(JSON.parse);
 return request.catch(function(error) {
  if (error.name==='NotFoundError') return null;
  throw error;
 });
}
function serviceRunning(result) {
 var instances=result.resproxy && result.resproxy.instances || {};
 return Object.keys(instances).some(function(id) { return instances[id].running===true; });
}
function deltas(policies,now) {
 policies.forEach(function(policy) {
  var limits=policy.probe_limits || {}, active=limits.active_seconds>0 ? limits.active_seconds : 60;
  var window=Math.max(active*2,120);
  if (limits.backup_seconds>0) window=Math.min(window,limits.backup_seconds);
  function fresh(node) {
   var age=now-node.latest_quality_at;
   return node.available && node.latest_delay_ms!=null && node.latest_delay_ms>=0 && node.latest_quality_at>0 && age>=0 && age<=window;
  }
  var selected=policy.nodes.find(function(node) {return node.selected;});
  policy.nodes.forEach(function(node) {
   node.latency_delta_percent=null;
   if (!selected || !fresh(selected) || selected.latest_delay_ms<=0) node.latency_delta_reason='active-measurement-missing-or-stale';
   else if (!fresh(node)) node.latency_delta_reason='node-measurement-missing-or-stale';
   else {node.latency_delta_percent=100*(selected.latest_delay_ms-node.latest_delay_ms)/selected.latest_delay_ms;node.latency_delta_reason='';}
  });
 });
 return policies;
}
// A disabled service after a cold boot has no RAM summary yet. Preserve its
// persisted catalog count with one lazy read per page lifetime, never per poll.
var coldCatalog;
function catalog(summary) {
 if (summary) return Promise.resolve(summary);
 if (!coldCatalog) coldCatalog=fs.exec('/usr/libexec/resproxy-status').then(function(result) {
  if (result.code!==0) throw new Error(result.stderr || 'Status unavailable');
  var status=JSON.parse(result.stdout),counts={};
  Object.keys(status.subscriptions || {}).forEach(function(id) {counts[id]=status.subscriptions[id].nodes || 0;});
  return {nodes:status.nodes || 0,counts:counts};
 });
 return coldCatalog;
}
function sources(summary,updates) {
 var result=updates || Object.create(null),counts=summary && summary.counts || {};
 Object.keys(result).forEach(function(id) {result[id].nodes=counts[id] || 0;});
 Object.keys(counts).forEach(function(id) {if (!Object.prototype.hasOwnProperty.call(result,id)) result[id]={};result[id].nodes=counts[id];});
 return result;
}
return baseclass.extend({
 // Routine polling reads only prepared RAM views; procd supplies liveness.
 snapshot:function() {
  return Promise.all([read('health-state/monitor.json',true),read('health-state/selector-runtime.json'),
   read('catalog-summary.json'),read('subscription-status.json'),read('resources.json'),
   read('dns-status.json'),read('dns-interop.json'),services('resproxy')]).then(function(data) {
   return catalog(data[2]).then(function(summary) {data[2]=summary;return data;});
  }).then(function(data) {
   var view=data[0],runtime=data[1],running=serviceRunning(data[7]),now=Date.now();
   var age=runtime ? now-Date.parse(runtime.updated_at) : Infinity;
   var health=view && view.health || [];
   var lease=health.reduce(function(seconds,policy) {return Math.max(seconds,((policy.probe_limits || {}).liveness_seconds || 0)+5);},20);
   var valid=running && age>=-1000 && age<=lease*1000;
   // Runtime publication precedes history/view publication. Use the newest
   // confirmed selector even when a view read straddles an atomic replacement.
   health.forEach(function(policy) {
    var state=valid && runtime.policies['resproxy-'+policy.id];
    policy.nodes.forEach(function(node) {node.selected=!!(state && state.runtime_confirmed && state.runtime_selected===node.id);});
   });
   return {status:{running:running,nodes:data[2] ? data[2].nodes : 0,
    policies:valid ? runtime.policies : {},subscriptions:sources(data[2],data[3]),
    resources:data[4],dns:data[5],dns_interop:data[6]},health:deltas(health,Math.floor(now/1000))};
  });
 },
 sources:function() {return Promise.all([read('catalog-summary.json').then(catalog),read('subscription-status.json')]).then(function(data) {return sources(data[0],data[1]);});},
 externalDNS:function() {
  return services().then(function(result) {
   return ['https-dns-proxy','smartdns','mosdns','adguardhome','AdGuardHome','unbound','dnscrypt-proxy','dnscrypt-proxy2'].find(function(name) {
    var item=result[name],instances=item && item.instances || {};
    return Object.keys(instances).some(function(id) {return instances[id].running===true;});
   }) || '';
  });
 },
 freshDeltas:deltas
});
