'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view';
'require fs';
'require ui';
'require poll';

var helper = '/usr/libexec/sb-gateway-upgrade';
function values(stdout) {
 var result = {};
 (stdout || '').split('\n').forEach(function(line) {
  var split = line.indexOf('=');
  if (split > 0) result[line.slice(0, split)] = line.slice(split + 1);
 });
 return result;
}
function command(action, args) {
 return fs.exec(helper, [action].concat(args || [])).then(function(result) {
  if (result.code !== 0) throw new Error((result.stderr || result.stdout || _('Package update failed.')).trim());
  return values(result.stdout);
 });
}
return view.extend({
 load: function() { return command('status'); },
 render: function(initial) {
  var status = E('dl', { 'class':'cbi-section' });
  var detail = E('p', { 'aria-live':'polite', 'role':'status', 'style':'overflow-wrap:anywhere' });
  var releaseLine = E('p');
  var pending = false, offered = null, actionError = '', showResult = false;
  var checkButton, installButton, uploadButton;
  function display(info) {
   initial = info;
   try { offered = info.release ? JSON.parse(info.release) : null; } catch(e) { offered = null; }
   var busy = pending || info.busy === 'yes';
   checkButton.disabled = busy || !L.hasViewPermission();
   uploadButton.disabled = busy || !L.hasViewPermission();
   installButton.disabled = busy || !offered || !offered.available || !L.hasViewPermission();
   installButton.hidden = !offered || !offered.available;
   installButton.style.display = installButton.hidden ? "none" : "";
   status.replaceChildren(E('dt', {}, _('Installed version')), E('dd', {}, info.version || '\u2014'));
   releaseLine.replaceChildren();
   if (offered && offered.tag) {
    var label = offered.available ? _('Available: %s').format(offered.tag) : _('Latest version is installed.');
    releaseLine.appendChild(E('span', {}, label + ' '));
    releaseLine.appendChild(E('a', { href:'https://github.com/Ati054/sb-gateway-openwrt/releases', target:'_blank', rel:'noopener noreferrer' }, _('Release notes')));
   } else if (info.remote_phase === 'checked') releaseLine.textContent = _('No updates available.');
   var progress = { checking:_('Checking GitHub…'), downloading:_('Downloading APK…') }[info.remote_phase];
   if (!progress && info.busy === 'yes') progress = ({ staged:_('Preparing update'), preparing:_('Preparing update'), installing:_('Installing package'), activating:_('Starting service') })[info.phase] || _('Preparing update');
   if (info.remote_phase === 'failed') detail.textContent = info.remote_error || _('Package update failed.');
   else if (progress) detail.textContent = progress;
   else if (info.phase === 'failed') detail.textContent = info.error || _('Update failed. Check the system log for details.');
   else if (actionError) detail.textContent = actionError;
   else if (showResult && info.phase === 'completed') detail.textContent = _('Package updated successfully.');
   else detail.textContent = '';
  }
  function refresh() { return command('status').then(display).catch(function(error) { detail.textContent = String(error); }); }
  function act(fn) {
   pending = true; actionError = ''; showResult = false; display(initial);
   return fn().then(refresh).catch(function(error) { actionError = String(error); })
    .finally(function() { pending = false; display(initial); });
  }
  checkButton = E('button', { type:'button', 'class':'btn cbi-button cbi-button-action', click:function() { return act(function() { return command('check'); }); } }, _('Check for updates'));
  installButton = E('button', { type:'button', 'class':'btn cbi-button cbi-button-positive', click:function() { return act(function() { return command('github-start').then(function(info) { showResult = true; return info; }); }); } }, _('Update from GitHub'));
  uploadButton = E('button', { type:'button', 'class':'btn cbi-button cbi-button-action', click:function() {
   return act(function() { return ui.uploadFile('/tmp/sb-gateway-upgrade.apk').then(function() { return command('inspect'); }).then(function(info) { return command('start', [info.sha256]).then(function(result) { showResult = true; return result; }); }); });
  } }, _('Choose APK and update'));
  display(initial); poll.add(refresh, 3);
  return sbgTheme.decorate(E('div', {}, [
   E('h2', {}, _('Package update')), status,
   E('div', { 'class':'cbi-section', 'style':'display:flex;flex-wrap:wrap;gap:12px' }, [checkButton, installButton]),
   releaseLine, detail,
   E('details', { 'class':'cbi-section' }, [E('summary', {}, _('Install an APK file')), E('p', {}, uploadButton)])
  ]));
 }, handleSaveApply:null, handleSave:null, handleReset:null
});
