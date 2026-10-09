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
		if (split > 0)
			result[line.slice(0, split)] = line.slice(split + 1);
	});
	return result;
}

function command(action, args) {
	return fs.exec(helper, [action].concat(args || [])).then(function(result) {
		if (result.code !== 0)
			throw new Error((result.stderr || result.stdout || _('Package update failed.')).trim());
		return values(result.stdout);
	});
}

return view.extend({
	load: function() {
		return command('status');
	},

	render: function(initial) {
		var status = E('dl', { 'class': 'cbi-section' });
		var detail = E('p', { 'aria-live': 'polite' });
		var hash = E('code', { 'style': 'overflow-wrap:anywhere' });
		var uploadButton;
		var pendingAction = false;

		function display(info) {
			initial = info;
			status.replaceChildren(
				E('dt', {}, _('Installed version')), E('dd', {}, info.version || '—'),
				E('dt', {}, _('Update state')), E('dd', {}, ({
					idle: _('No update in progress'),
					staged: _('Preparing update'),
     preparing: _('Preparing update'),
					installing: _('Installing package'),
					activating: _('Starting service'),
					completed: _('Completed'),
					failed: _('Update failed')
				})[info.phase] || info.phase || _('Unknown'))
			);
			uploadButton.disabled = pendingAction || info.busy === 'yes';
			if (info.phase === 'completed' && !pendingAction)
				detail.textContent = _('Package updated successfully.');
			else if (info.phase === 'failed' && !detail.textContent)
				detail.textContent = info.error || _('Update failed. Check the system log for details.');
		}

		function refresh() {
			return command('status').then(display).catch(function(error) {
				detail.textContent = String(error);
			});
		}

		uploadButton = E('button', {
			'class': 'btn cbi-button cbi-button-action',
			'type': 'button',
			'click': function() {
				pendingAction = true;
				uploadButton.disabled = true;
				detail.textContent = '';
				hash.textContent = '';
				return ui.uploadFile('/tmp/sb-gateway-upgrade.apk').then(function() {
					return command('inspect');
				}).then(function(info) {
					hash.textContent = info.sha256 || '';
					detail.textContent = _('Installing version %s…').format(info.version);
					return command('start', [info.sha256]);
				}).then(refresh).catch(function(error) {
					detail.textContent = String(error);
				}).finally(function() {
					pendingAction = false;
					uploadButton.disabled = initial.busy === 'yes';
				});
			}
		}, _('Choose APK and update'));

		display(initial);
		poll.add(refresh, 3);
		return sbgTheme.decorate(E('div', {}, [
			E('h2', {}, _('Package update')),
			E('p', {}, _('Only SB Gateway is updated. The proxy service briefly restarts; subscription updates remain restart-free.')),
			E('p', {}, _('Compact updates keep a recovery copy in RAM. A power loss during installation may require reinstalling the package.')),
			status,
			E('div', { 'class': 'cbi-section' }, uploadButton),
			E('p', {}, [ _('APK SHA-256: '), hash ]),
			detail
		]));
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
