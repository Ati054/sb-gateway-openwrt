'use strict';
'require baseclass';
'require view.resproxy.safe_dom as safeDOM';
'require view.resproxy.name_filter as nameFilter';

var E = safeDOM.create;

var preferenceKey = 'sb-gateway.node-health.v1';
function preferences() {
 try {
  var saved = JSON.parse(window.localStorage.getItem(preferenceKey));
  return saved && typeof saved === 'object' && !Array.isArray(saved) ? saved : {};
 } catch (error) { return {}; }
}
function stamp(value) { return value ? new Date(value * 1000).toLocaleString() : '—'; }
function metric(value, suffix) { return value == null ? '—' : String(value) + (suffix || ''); }
function measured(value, suffix, at) {
 if (value == null || !at) return '—';
 return E('span', {}, [metric(value, suffix), E('small', { 'class':'sbg-measurement-at', title:_('Last successful measurement') }, stamp(at))]);
}
function nodeState(node) {
 return node.selected ? (node.available ? _('Active') : _('Active · unavailable')) :
  node.last_probe_at ? (node.available ? _('Available') : _('Unavailable')) : _('Not checked');
}
return baseclass.extend({
 latency: function(node) { return node ? measured(node.latest_delay_ms, ' '+_('ms'), node.latest_quality_at) : '—'; },
 create: function(initial) {
  var saved = preferences(), policies = initial || [], rowLimit = 10;
  var picker = E('select', { 'class': 'cbi-input-select', 'aria-label': _('Route list') });
  var period = E('select', { 'class': 'cbi-input-select', 'aria-label': _('Rating period') }, [
   E('option', { value: 'today' }, _('Today (UTC)')), E('option', { value: '7d' }, _('7 days'))
  ]);
  var search = E('input', { 'class': 'cbi-input-text', type: 'search', placeholder: _('Name fragments, separated by commas'), 'aria-label': _('Find a node') });
  period.value = saved.period === '7d' ? '7d' : 'today';
  search.value = typeof saved.search === 'string' ? saved.search : '';
  var body = E('tbody'), summary = E('p', { 'class': 'sbg-muted' });
  var searchError = E('p', { role:'alert', hidden:'' });
  var table = E('table', { 'class': 'table sbg-health-table' }, [
   E('thead', {}, E('tr', {}, [_('Node'), _('Status'), _('Availability / HTTPS errors'), _('Median latency'), _('Delta, %'), _('Checks'), _('Last check')]
    .map(function(title) { return E('th', { scope: 'col' }, title); }))), body
  ]);
  function fill(keep) {
   picker.replaceChildren();
   policies.forEach(function(p) { picker.appendChild(E('option', { value: p.id }, p.id)); });
   if (policies.some(function(p) { return p.id === keep; })) picker.value = keep;
  }
  function rows() {
   var fragment = document.createDocumentFragment();
   var policy = policies.find(function(p) { return p.id === picker.value; });
   if (!policy) {
    summary.textContent = _('No health data yet.');
    body.replaceChildren();
    return;
   }
   summary.textContent = policy.pending ? _('Waiting for checks') : _('%d candidates · %d healthy reserves').format(policy.candidate_count, policy.healthy_reserves);
   if (policy.history_budget_kib) summary.textContent += ' · '+_('History budget: %d / %d KiB across all route lists').format(policy.history_usage_kib || 0, policy.history_budget_kib);
   if (policy.history_truncated) summary.textContent += ' · '+_('Older history removed to stay within the RAM budget');
   var query = nameFilter.parse(search.value);
   searchError.textContent = nameFilter.errorMessage(query.error); searchError.hidden = !query.error;
   search.setAttribute('aria-invalid', query.error ? 'true' : 'false');
   var visible = policy.nodes.filter(function(n) { return n.selected || nameFilter.matches(n.label, query, true); });
   visible.sort(function(a,b) {
    var aa = (a.period_stats || {})[period.value] || {}, bb = (b.period_stats || {})[period.value] || {};
    if (!!a.selected !== !!b.selected) return a.selected ? -1 : 1;
    var diff = (aa.median_ms == null ? Infinity : aa.median_ms) - (bb.median_ms == null ? Infinity : bb.median_ms);
    return diff || a.label.localeCompare(b.label);
   });
   if (visible.length > rowLimit) summary.textContent += ' · '+_('Showing %d of %d nodes').format(rowLimit, visible.length);
   visible.slice(0, rowLimit).forEach(function(n) {
    var s = (n.period_stats || {})[period.value] || {};
    var state = nodeState(n);
    var deltaTitle = n.latency_delta_percent != null ? _('Compared with the active node using fresh HTTPS measurements. Negative means lower latency; positive means higher latency.') :
     n.latency_delta_reason === 'active-measurement-missing-or-stale' ? _('No fresh successful HTTPS measurement for the active node.') : _('No fresh successful HTTPS measurement for this node.');
    if (n.latency_comparison && n.latency_delta_percent != null) {
     deltaTitle = _('Last paired HTTPS comparison. Negative means lower latency; positive means higher latency.') + '\n' +
      _('Active node') + ': ' + new Date(n.latency_comparison.active_at*1000).toLocaleString() + '\n' +
      _('Candidate') + ': ' + new Date(n.latency_comparison.candidate_at*1000).toLocaleString();
    } else if (n.latency_delta_reason === 'paired-measurement-missing' || n.latency_delta_reason === 'measurements-too-far-apart')
     deltaTitle = _('Waiting for HTTPS measurements completed within 10 seconds of each other.');
    var availability = s.availability_percent == null ? '—' : Number(s.availability_percent).toFixed(1)+'%';
    var loss = s.https_error_percent == null ? '' : ' / '+Number(s.https_error_percent).toFixed(1)+'%';
    fragment.appendChild(E('tr', { 'class': n.selected ? 'tr sbg-node-active' : 'tr' }, [
     E('td', { 'class': 'td' }, n.label),
     E('td', { 'class': 'td' }, E('span', { 'class': 'sbg-node-state '+(n.last_probe_at ? n.available ? 'sbg-up' : 'sbg-down' : 'sbg-unknown') }, state)),
     E('td', { 'class': 'td' }, availability+loss),
     E('td', { 'class': 'td' }, metric(s.median_ms, ' '+_('ms'))),
     E('td', { 'class': 'td', title: deltaTitle }, n.latency_delta_percent == null ? '—' : (n.latency_delta_percent > 0 ? '+' : '')+Number(n.latency_delta_percent).toFixed(1)+'%'),
     E('td', { 'class': 'td' }, metric(s.samples)), E('td', { 'class': 'td' }, stamp(n.last_probe_at))
    ]));
   });
   if (!visible.length) fragment.appendChild(E('tr', {}, E('td', { colspan: 7 }, policy.pending ? _('No health data yet.') : _('No matching nodes.'))));
   var scroll = table.parentElement, left = scroll && scroll.scrollLeft, top = scroll && scroll.scrollTop;
   body.replaceChildren(fragment);
   if (scroll) { scroll.scrollLeft = left; scroll.scrollTop = top; }
  }
  [picker,period].forEach(function(control) { control.addEventListener('change', function() { rows(); remember(); }); });
  search.addEventListener('input', function() { rows(); remember(); });
  var root = E('details', { 'class': 'sbg-overview-detail sbg-node-health', open: saved.open === true ? '' : null }, [
   E('summary', {}, _('Node health')),
   E('div', { 'class': 'sbg-health-controls' }, [
    [ _('Route list'), picker ], [ _('Rating period'), period ], [ _('Find a node'), search ]
   ].map(function(pair) { return E('label', {}, [E('span', {}, pair[0]),pair[1]]); })), searchError, summary,
   E('p', { 'class':'sbg-muted' }, _('Delta: negative means lower latency; positive means higher latency than the active node.')),
   E('div', { 'class': 'sbg-table-scroll', tabindex: '0', 'aria-label': _('Node health') }, table)
  ]);
  function remember() {
   saved = { policy:picker.value || saved.policy || '', period:period.value, search:search.value, open:root.open };
   try { window.localStorage.setItem(preferenceKey, JSON.stringify(saved)); } catch (error) {}
  }
  root.addEventListener('toggle', remember);
  fill(typeof saved.policy === 'string' ? saved.policy : ''); rows();
  return { node: root, update: function(data) {
   var keep=picker.value || saved.policy, ids=policies.map(function(p) { return p.id; }).join('\n');
   policies=data || [];
   if (ids !== policies.map(function(p) { return p.id; }).join('\n')) fill(keep);
   rows();
  } };
 }
});
