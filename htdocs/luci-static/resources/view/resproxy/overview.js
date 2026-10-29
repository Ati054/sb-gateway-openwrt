'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view.resproxy.node_health as healthUI';
'require view.resproxy.runtime_status as monitor';
'require view';
'require form';
'require fs';
'require uci';
'require poll';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

function snapshot(keys) {
 var requests=[monitor.snapshot().then(function(value) {
  return [{key:'status',value:value.status},{key:'health',value:value.health}];
 }).catch(function(error) {return [{key:'status',error:String(error)},{key:'health',error:String(error)}];})];
 if (!keys || keys.indexOf('rules')>=0) requests.push(fs.exec('/usr/libexec/resproxy-rulesets').then(function(result) {
  if (result.code!==0) throw new Error(result.stderr || 'Read failed');
  return [{key:'rules',value:JSON.parse(result.stdout)}];
 }).catch(function(error) {return [{key:'rules',error:String(error)}];}));
 return Promise.all(requests).then(function(result) {return [].concat.apply([],result);});
}
function displayTable(headers, body) {
 return E('div', { 'class': 'sbg-table-scroll', tabindex: '0' }, E('table', { 'class': 'table' }, [
  E('thead', {}, E('tr', {}, headers.map(function(title) { return E('th', { scope:'col' }, title); }))),body
 ]));
}
function cell(value) { return E('td', { 'class':'td' }, value); }
function dnsMode(status) {
 var interop=status && status.dns_interop;
 if (!interop) return _('No data');
 return interop.effective_mode === 'off' ? _('LAN capture disabled') :
  interop.effective_mode === 'system' ? _('OpenWrt DNS') : _('Package DNS');
}
return view.extend({
 load: function() { return Promise.all([uci.load('resproxy'),snapshot()]); },
 render: function(data) {
  var values={}, failed={}, refreshing=false, nextRules=0;
  var health=healthUI.create([]);
  var info=E('dl', { 'class':'sbg-overview-info' }), routes=E('tbody'), dns=E('dl', { 'class':'sbg-overview-counters' });
  var memoryWarning=E('p', { 'class':'alert-message warning', hidden:'', role:'status' });
  var dnsWarning=E('p', { 'class':'alert-message warning', hidden:'' });
  var errors=E('p', { 'class':'alert-message warning', hidden:'', role:'status' });
  var checked=E('span', { 'class':'sbg-muted', 'aria-live':'off' });
  var dbSummary=E('p'), dbBody=E('tbody');
  var dbError=E('p', { 'class':'alert-message warning', hidden:'', role:'status' });
  function addPair(list, title, value) { list.appendChild(E('div', {}, [E('dt', {}, title),E('dd', {}, value)])); }
  function paint(result) {
   result.forEach(function(item) {
    if (item.error) failed[item.key]=true;
    else { values[item.key]=item.value; delete failed[item.key]; }
   });
   var labels={status:_('Service'),health:_('Node health'),rules:_('Rule databases')};
   errors.hidden=!Object.keys(failed).length;
   errors.textContent=_('No fresh data: %s').format(Object.keys(failed).map(function(key) { return labels[key]; }).join(', '));
   info.replaceChildren(); dns.replaceChildren();
   var routeRows=document.createDocumentFragment();
   var status=values.status, policies=values.health || [];
   addPair(info,_('Service'),status ? status.running ? status.runtime_pending ? _('Starting') : status.runtime_stale ? _('Status data is stale') : _('Running') : _('Stopped') : _('No data'));
   addPair(info,_('Active catalog'),status ? _('%d nodes').format(status.nodes) : '—');
   addPair(info,_('DNS mode'),dnsMode(status));
   var resources=status && status.resources;
   var resourceFresh=status && status.resources_fresh && resources;
   function ram(process) {
    if (!resourceFresh || !process) return '—';
    return E('span', { title:_('RssAnon: %s · Files: %s / %s').format((process.anon_kib/1024).toFixed(1)+' MiB',process.fds,process.fd_limit || '—') },(process.rss_kib/1024).toFixed(1)+' MiB');
   }
   addPair(info,_('Controller RAM'),ram(resources && resources.controller));
   addPair(info,_('Xray RAM'),ram(resources && resources.core));
   var sessions=resourceFresh && resources.tcp_sessions != null && resources.udp_sessions != null;
   addPair(info,_('Xray sessions'),sessions ? _('%s TCP · %s UDP').format(resources.tcp_sessions,resources.udp_sessions) : '—');
   addPair(info,_('Available RAM'),resourceFresh ? (resources.available_kib/1024).toFixed(1)+' MiB' : '—');
   var memoryMessages=[];
   if (resourceFresh && resources.low_memory) memoryMessages.push(_('Low available router memory. Reduce connection load or selected rule sets.'));
   if (resourceFresh && resources.core && resources.core.fd_limit>0 && resources.core.fds>=resources.core.fd_limit*0.8) memoryMessages.push(_('Xray is near its open-file limit.'));
   if (resources && resources.last_oom && status.now-Date.parse(resources.last_oom)>=0 && status.now-Date.parse(resources.last_oom)<600000) memoryMessages.push(_('Xray was stopped by the kernel for lack of memory. See the journal.'));
   memoryWarning.hidden=!memoryMessages.length; memoryWarning.textContent=memoryMessages.join(' ');
   var routeLists=uci.sections('resproxy','policy');
   // Saved lists remain selectable before the controller's first measurement.
   policies=routeLists.map(function(config) {
    var id=config.id || config['.name'];
    return policies.find(function(p) { return p.id === id; }) ||
     {id:id,mode:config.mode || 'best',pending:true,candidate_count:0,healthy_reserves:0,nodes:[]};
   });
   routeLists.forEach(function(config) {
    var id=config.id || config['.name'], state=status && (status.policies || {})['resproxy-'+id];
    var policy=policies.find(function(p) { return p.id === id; });
    var selected=state && (state.runtime_selected || state.selected);
    var node=policy && policy.nodes.find(function(n) { return n.id === selected; });
    var ruleWAN=state && state.runtime_confirmed && state.wan_fallback_active && selected === 'block';
    var label=node ? node.label : ruleWAN ? _('WAN by rules') : selected === 'block' ? _('Blocked') : '—';
    var routeState=!status ? _('No data') : !status.running ? _('Stopped') : !state ? status.runtime_stale ? _('Status data is stale') : _('Starting') :
     state.runtime_error ? _('Error') : ruleWAN ? _('Fallback WAN') : selected === 'block' ? _('Blocked') :
     state.runtime_confirmed ? _('Active') : _('Not confirmed');
    var row=E('tr', { 'class':'tr' }, [cell(id),cell(label),cell(routeState),
     cell(healthUI.latency(node)),cell(policy ? String(policy.healthy_reserves) : '—')]);
    if (state && state.runtime_error) row.children[2].appendChild(E('small', { 'class':'sbg-runtime-error' },state.runtime_error));
    routeRows.appendChild(row);
   });
   if (!routeLists.length) routeRows.appendChild(E('tr', {},E('td',{colspan:6},_('No route lists.'))));
   routes.replaceChildren(routeRows);
   var counters=status && status.dns;
   [[_('Queries'),'queries'],[_('Through proxy'),'proxy'],[_('Direct WAN'),'direct'],[_('Fallback WAN'),'fallback'],
    [_('Cache'),'cache_hits'],[_('Local'),'local'],[_('Errors'),'failures']].forEach(function(pair) {
     addPair(dns,pair[0],counters ? String(counters[pair[1]] || 0) : '—');
    });
   var interop=status && status.dns_interop;
   var warning={
    'https-dns-proxy-running':_('https-dns-proxy is running. Configure DNS takeover on the DNS page.'),
    'other-dns-service':_('Another DNS service is running; DNS capture is disabled.'),
    'other-dns-redirect':_('Another DNS redirect is present; DNS capture is disabled.'),
    'firewall-inspection-failed':_('Unable to inspect firewall; DNS capture is disabled.'),
    'listener-failed':_('DNS listener failed. See the journal.'),
    'router-dns-handoff-failed':_('DNS takeover failed; previous DNS restored. See the journal.')
   };
   dnsWarning.hidden=!(interop && interop.reason);
   dnsWarning.textContent=interop && interop.reason ? warning[interop.reason] || interop.reason : '';
   health.update(policies);
   if (result.some(function(item) { return item.key === 'rules' && !item.error; })) {
    var catalog=values.rules, database=catalog && catalog.status;
    dbBody.replaceChildren();
    var packs=database && database.packs || [], names={};
    if (catalog) catalog.choices.forEach(function(c) { names[c.id]=c.name; });
    var loaded=packs.filter(function(p) { return p.state==='updated' || p.state==='unchanged'; }).length;
    var stale=packs.filter(function(p) { return p.state==='stale'; }).length;
    var unavailable=packs.filter(function(p) { return p.state==='unavailable'; }).length;
    dbSummary.textContent=!catalog ? _('No data') : !database ? _('No catalog update yet.') :
     _('%d downloaded · %d cached · %d unavailable').format(loaded,stale,unavailable) +
      (database.checked_at ? ' · '+new Date(database.checked_at).toLocaleString() : '');
    packs.forEach(function(p) {
     var state={ updated:_('Updated'),unchanged:_('Unchanged'),stale:_('Last good copy'),unavailable:_('Not loaded') }[p.state] || p.state;
     dbBody.appendChild(E('tr', {},[cell(names[p.id] || p.id),cell(state),cell(String(p.entries || 0)),cell(p.error || '—')]));
    });
    var failedPacks=packs.filter(function(p) { return !!p.error; }).length;
    dbError.hidden=!(database && database.error) && !failedPacks;
    dbError.textContent=database && database.error ?
     _('Rule database update was not applied; previous rules remain active. %s').format(database.error) :
     failedPacks ? _('Some rule databases could not be updated. See Rule databases for details.') : '';
   }
   if (result.some(function(item) { return !item.error; })) checked.textContent=_('Updated at %s').format(new Date().toLocaleTimeString());
  }
  var refresh=E('button', { type:'button', 'class':'cbi-button cbi-button-action', click:function() { return update(true); } },_('Refresh'));
  function update(force) {
   if (refreshing) return Promise.resolve();
   refreshing=true; refresh.disabled=true;
   var keys=['status','health'];
   if (force || Date.now() >= nextRules) { keys.push('rules'); nextRules=Date.now()+60000; }
   return snapshot(keys).then(paint).finally(function() { refreshing=false; refresh.disabled=false; });
  }
  var dashboard=E('div', { 'class':'sbg-overview' },[
   E('div', { 'class':'sbg-overview-toolbar' },[refresh,checked]),errors,dbError,
   info,memoryWarning,E('h3',{},_('Route lists')),
   displayTable([_('Route list'),_('Selected node'),_('Status'),_('Latency'),_('Healthy reserves')],routes),
   health.node,
   E('h3',{},_('DNS requests')),E('small', { 'class':'sbg-muted' },_('Since service start')),dns,dnsWarning,
   E('details', { 'class':'sbg-overview-detail' },[E('summary',{},_('Rule databases')),dbSummary,
    displayTable([_('List'),_('Status'),_('Rules'),_('Error')],dbBody)])
  ]);
  var map=new form.Map('resproxy');
  var service=map.section(form.NamedSection,'main','main');
  service.addremove=false;
  var OverviewFlag=form.Flag.extend({
   renderWidget:function() {
    var widget=this.super('renderWidget',arguments);
    widget.querySelector('input[type=checkbox]').setAttribute('aria-label',this.title);
    return widget;
   }
  });
  var enabled=service.option(OverviewFlag,'enabled',_('Enable SB Gateway'));
  enabled.default='0'; enabled.rmempty=false;
  enabled.validate=function(section,value) {
   if(value!=='1')return true;
   var policies=uci.sections('resproxy','policy');
   return policies.length>0 && policies.every(function(policy) {
    return /^[a-z][a-z0-9-]{0,31}$/.test(policy.id || '') &&
     (Array.isArray(policy.selection_order) ? policy.selection_order.length>0 : !!String(policy.selection_order || '').trim());
   }) || _('Finish configuring route lists before enabling SB Gateway.');
  };
  // One user action controls both the service and client routing. Keep the old
  // UCI key synchronized for existing firewall/init consumers.
  enabled.forcewrite=true;
  enabled.write=function(section,value) {
   uci.set('resproxy',section,'enabled',value);
   uci.set('resproxy',section,'route_enabled',value);
  };
  paint(data[1]); nextRules=Date.now()+60000;
  return map.render().then(function(formNode) {
   function autoRefresh() { return document.hidden ? Promise.resolve() : update(); }
   poll.add(autoRefresh,5);
   function resume() { if (!document.hidden) autoRefresh(); }
   document.addEventListener('visibilitychange',resume);
   window.addEventListener('pageshow',function(event) { if (event.persisted) resume(); });
   window.addEventListener('pagehide',function(event) {
    if (!event.persisted) { document.removeEventListener('visibilitychange',resume); poll.remove(autoRefresh); }
   });
   formNode.classList.add('sbg-overview-settings');
   return sbgTheme.decorate(E('div',{},[E('h2',{},_('SB Gateway for OpenWrt')),formNode,dashboard]));
  });
 }
});
