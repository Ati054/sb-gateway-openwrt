'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view';
'require fs';
'require ui';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

function readLog(kind, lines) {
	return fs.exec('/usr/libexec/resproxy-log', [kind, lines]).then(function(result) {
		if (result.code !== 0)
			throw new Error(result.stderr || _('Unable to read the journal.'));
		return result.stdout || _('No matching events.');
	});
}

return view.extend({
	load: function() { return readLog('events', '200'); },

	render: function(initial) {
		var kind = E('select', { 'class': 'cbi-input-select', 'aria-label': _('Journal category') }, [
			E('option', { 'value': 'events' }, _('Proxy events')),
			E('option', { 'value': 'errors' }, _('Errors and warnings')),
			E('option', { 'value': 'xray' }, _('Xray messages')),
			E('option', { 'value': 'all' }, _('System journal'))
		]);
		var count = E('select', { 'class': 'cbi-input-select', 'aria-label': _('Maximum lines') }, [
			E('option', { 'value': '50' }, '50'),
			E('option', { 'value': '200', 'selected': 'selected' }, '200'),
			E('option', { 'value': '500' }, '500')
		]);
		var search = E('input', {
			'class': 'cbi-input-text', 'type': 'search', 'placeholder': _('Find in loaded lines'),
			'aria-label': _('Find in loaded lines')
		});
		var output = E('pre', {
			'class': 'cbi-section',
			'style': 'white-space:pre-wrap;overflow-wrap:anywhere;max-height:70vh;overflow:auto',
			'aria-label': _('Journal output')
		}, initial);
		var loaded = initial;
		function filter() {
			var query = search.value.toLocaleLowerCase();
			output.textContent = query ? loaded.split('\n').filter(function(line) {
				return line.toLocaleLowerCase().indexOf(query) >= 0;
			}).join('\n') : loaded;
		}
		function refresh() {
			return readLog(kind.value, count.value).then(function(text) {
				loaded = text;
				filter();
			}).catch(function(error) {
				ui.addNotification(null, E('p', {}, String(error)));
			});
		}
		kind.addEventListener('change', refresh);
		count.addEventListener('change', refresh);
		search.addEventListener('input', filter);
		var button = E('button', {
			'class': 'btn cbi-button cbi-button-action',
			'type': 'button',
			'click': refresh
		}, _('Refresh'));
		return sbgTheme.decorate(E('div', {}, [
			E('h2', {}, _('Journal')),
			E('div', { 'class': 'cbi-page-actions', 'style': 'display:flex;gap:.5rem;flex-wrap:wrap;align-items:center' }, [kind, count, search, button]),
			output
		]));
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
