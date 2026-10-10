'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view.resproxy.node_health as healthUI';
'require view';
'require form';
'require fs';
'require uci';
'require poll';

var helpers = { status: '/usr/libexec/resproxy-status', health: '/usr/libexec/resproxy-health', rules: '/usr/libexec/resproxy-rulesets' };
function snapshot(keys) {
 return Promise.all((keys || Object.keys(helpers)).map(function(key) {
  // Health can exceed rpcd's buffered exec limit for large candidate pools.
  var read = key === 'health' ? fs.exec_direct(helpers[key], [], 'json') :
   fs.exec(helpers[key]).then(function(result) {
    if (result.code !== 0) throw new Error(result.stderr || 'Read failed');
    return JSON.parse(result.stdout);
   });
  return read.then(function(value) { return { key:key, value:value }; })
   .catch(function(error) { return { key:key, error:String(error) }; });
 }));
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
  var dnsWarning=E('p', { 'class':'alert-message warning', hidden:'' });
  var errors=E('p', { 'class':'alert-message warning', hidden:'', role:'status' });
  var checked=E('span', { 'class':'sbg-muted', 'aria-live':'off' });
  var dbSummary=E('p'), dbBody=E('tbody');
  var dbError=E('p', { 'class':'alert-message warning', hidden:'' });
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
   addPair(info,_('Service'),status ? status.running ? _('Running') : _('Stopped') : _('No data'));
   addPair(info,_('Active catalog'),status ? _('%d nodes').format(status.nodes) : '—');
   addPair(info,_('DNS mode'),dnsMode(status));
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
    var routeState=!status ? _('No data') : !status.running ? _('Stopped') : !state ? _('Waiting for checks') :
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
    'listener-failed':_('DNS listener failed. See the journal.')
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
     _('%d loaded · %d cached · %d unavailable').format(loaded,stale,unavailable) +
      (database.checked_at ? ' · '+new Date(database.checked_at).toLocaleString() : '');
    packs.forEach(function(p) {
     var state={ updated:_('Updated'),unchanged:_('Unchanged'),stale:_('Last good copy'),unavailable:_('Not loaded') }[p.state] || p.state;
     dbBody.appendChild(E('tr', {},[cell(names[p.id] || p.id),cell(state),cell(String(p.entries || 0)),cell(p.error || '—')]));
    });
    dbError.hidden=!(database && database.error); dbError.textContent=database && database.error || '';
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
   E('div', { 'class':'sbg-overview-toolbar' },[refresh,checked]),errors,
   info,E('h3',{},_('Route lists')),
   displayTable([_('Route list'),_('Selected node'),_('Status'),_('Latency'),_('Healthy reserves')],routes),
   health.node,
   E('h3',{},_('DNS requests')),E('small', { 'class':'sbg-muted' },_('Since service start')),dns,dnsWarning,
   E('details', { 'class':'sbg-overview-detail' },[E('summary',{},_('Rule databases')),dbSummary,dbError,
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
