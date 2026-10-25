'use strict';
'require baseclass';
'require validation';

function values(value) { return Array.isArray(value) ? value : String(value || '').trim().split(/\s+/); }
function choices(hosts) {
 var seen = Object.create(null), result = [];
 (hosts || []).forEach(function(host) {
  if (String(host.disabled || '0') === '1') return;
  values(host.ip).concat(values(host.ip6_addr)).forEach(function(ip) {
   ip = String(ip).trim();
   var ipv4 = validation.parseIPv4(ip), ipv6 = !ipv4 && validation.parseIPv6(ip);
   if ((!ipv4 && !ipv6) || ip === '0.0.0.0' || (ipv6 && ip.indexOf('::') === 0) || seen[ip]) return;
   seen[ip] = true;
   result.push({ip:ip, cidr:ip+(ipv4 ? '/32' : '/128'), name:host.name || values(host.mac).filter(Boolean).join(', ') || ip});
  });
 });
 return result.sort(function(a, b) { return a.name.localeCompare(b.name) || a.ip.localeCompare(b.ip); });
}
return baseclass.extend({ choices:choices });
