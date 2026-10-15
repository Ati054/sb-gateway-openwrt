'use strict';
'require baseclass';
'require fs';
'require poll';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

function read() {
 return fs.exec('/usr/libexec/resproxy-plugins', ['status']).then(function(result) {
  if (result.code !== 0) throw new Error(_('Plugin status unavailable.'));
  return JSON.parse(result.stdout);
 });
}
return baseclass.extend({
 render: function() {
  var source = E('small'), feedback = E('span', { role: 'status' });
  var rows = ['v2ray-plugin', 'obfs-local'].map(function(name) {
   var status = E('span'), button = E('button', { type: 'button', class: 'cbi-button cbi-button-action', disabled: '' }, _('Install'));
   button.addEventListener('click', function() {
    button.disabled = true;
    fs.exec('/usr/libexec/resproxy-plugins', ['install', name]).then(function(result) {
     if (result.code !== 0) throw new Error(result.stderr || _('Plugin installation failed.'));
     feedback.textContent = ''; return update();
    }).catch(function(error) { feedback.textContent = String(error.message || error); button.disabled = false; });
   });
   return { name: name, status: status, button: button, node: E('div', { class: 'sbg-plugin-row' }, [
    E('strong', {}, name === 'obfs-local' ? 'simple-obfs' : name), status, button
   ]) };
  });
  var panel = E('details', { class: 'sbg-plugin-settings' }, [E('summary', {}, _('Shadowsocks plugins')),
   E('div', { class: 'sbg-plugin-content' }, [source].concat(rows.map(function(row) { return row.node; }), [feedback]))]);
  var phases = { queued: _('Waiting…'), downloading: _('Downloading…'), installing: _('Installing…') };
  function update() {
   if (!document.contains(panel)) return Promise.resolve();
   return read().then(function(result) {
    source.textContent = result.source + ' · TCP';
    var state = result.state || {}, busy = !!phases[state.phase];
    rows.forEach(function(row) {
     var installed = !!(result.installed || {})[row.name];
     row.status.textContent = installed ? _('Installed') : state.name === row.name && busy ? phases[state.phase] : _('Not installed');
     row.button.disabled = installed || busy || !result.supported;
     row.button.textContent = installed ? _('Installed') : _('Install');
    });
    if (state.phase === 'error') feedback.textContent = _('Plugin installation failed.');
    else if (!result.supported) feedback.textContent = _('Unsupported router architecture.');
    else feedback.textContent = '';
   }).catch(function() { feedback.textContent = _('Plugin status unavailable.'); });
  }
  // Wait for mounting: only this optional panel has a loading/error state.
  window.setTimeout(update, 0); poll.add(function() { return panel.open ? update() : Promise.resolve(); }, 3);
  panel.addEventListener('toggle', function() { if (panel.open) update(); });
  return panel;
 }
});
