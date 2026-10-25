'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view';
'require view.resproxy.plugins as plugins';
'require form';
'require view.resproxy.interval as IntervalValue';
'require fs';
'require uci';
'require ui';
'require rpc';
'require poll';
'require view.resproxy.runtime_status as monitor';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

var commitSourceSettings = rpc.declare({
 object: 'uci', method: 'commit', params: ['config'], reject: true
});

function readSourceStatus() { return monitor.sources(); }

function sourceName(value) {
	return String(value || '').trim();
}

function groupActions(section, decorateRow) {
	var rows = section.querySelectorAll(':scope > .cbi-section-node');
	var actions = section.querySelectorAll(':scope > .cbi-section-remove');
	rows.forEach(function(row, index) {
		decorateRow(row);
		var action = actions[index];
		if (!action)
			return;
		var name = uci.get('resproxy', row.getAttribute('data-section-id'), 'id') || _('New source');
		action.classList.remove('right');
		action.style.cssText = 'float:none;margin:0';
		section.insertBefore(E('div', {
			'class': 'cbi-section-header',
			'style': 'display:flex;align-items:center;flex-wrap:wrap;gap:.75rem;margin:1rem 0 .5rem'
		}, [ E('h4', { 'style': 'margin:0' }, name), action ]), row);
	});
	return section;
}

return view.extend({
	load: function() { return Promise.all([uci.load('resproxy'), readSourceStatus().catch(function() { return null; })]); },

	render: function(data) {
		var map = new form.Map('resproxy', _('Subscriptions'));
		var loading = false;
		var activeID = '';
		var status = data[1] || {};
		var statusFailed = !data[1];
		var openStates = {};
		var pending = null;
		var lastPoll = 0;
		var sources = map.section(form.TypedSection, 'subscription', _('Sources'));
		sources.anonymous = true;
		sources.addremove = true;
		sources.max_count = 16;
		// LuCI re-renders this section after Add/Remove. Keep each action next
		// to its own row on every render, not only on the initial page load.
		var renderContents = sources.renderContents;
		sources.renderContents = function() {
			return groupActions(renderContents.apply(this, arguments), decorateSource);
		};
		var id = sources.option(form.Value, 'id', _('Source name'));
		id.rmempty = false;
		id.description = _('Keep this name after the first import.');
		id.validate = function(section, value) {
			var name = sourceName(value);
			if (!/^[A-Za-z][A-Za-z0-9 _-]{0,31}$/.test(name))
				return _('Use 1–32 Latin letters, digits, spaces, underscores or hyphens; start with a letter.');
			return !sources.cfgsections().some(function(other) {
				return other !== section && sourceName(id.formvalue(other)).toLowerCase() === name.toLowerCase();
			}) || _('This source name is already used.');
		};
		id.write = function(section, value) {
			return uci.set('resproxy', section, 'id', sourceName(value));
		};
		var url = sources.option(form.Value, 'url', _('Subscription URL'));
		url.datatype = 'string';
		url.placeholder = 'https://';
		url.rmempty = false;
		url.validate = function(section, value) {
			try {
				var address = new URL(value);
				if (address.protocol === 'https:' && address.hostname && !address.username && !address.password)
					return true;
			} catch (error) {}
			return _('Enter an HTTPS subscription address.');
		};
		var mode = sources.option(form.ListValue, 'update_mode', _('Update method'));
		mode.value('automatic', _('Automatic and on request'));
		mode.value('daily', _('On schedule'));
		mode.value('manual', _('Only on request'));
		mode.default = 'automatic';
		mode.rmempty = false;
		var interval = sources.option(IntervalValue.Value, 'update_interval_seconds', _('Automatic update interval'));
		interval.datatype = 'ufloat';
		interval.minimumSeconds = 300;
		interval.maximumSeconds = 86400;
		interval.intervalError = _('Choose an interval from 5 minutes to 24 hours, with whole seconds.');
		interval.default = '1800';
		interval.rmempty = false;
		interval.retain = true;
		interval.depends('update_mode', 'automatic');
		var at = sources.option(form.Value, 'update_time', _('Update time'));
		at.default = '03:00';
		at.rmempty = false;
		at.retain = true;
		at.description = _('Router time');
		at.depends('update_mode', 'daily');
		at.validate = function(section, value) {
			return /^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(value || '') || _('Specify time as HH:MM.');
		};
		at.renderWidget = function() {
			var widget = form.Value.prototype.renderWidget.apply(this, arguments);
			var input = widget.querySelector('input') || widget;
			input.type = 'time';
			input.step = '60';
			return widget;
		};
		var via = sources.option(form.ListValue, 'fetch_via', _('Fetch path'));
		via.value('proxy-first', _('Current proxy, then direct'));
		via.value('direct-first', _('Direct, then current proxy'));
		via.value('proxy-only', _('Current proxy only'));
		via.value('direct-only', _('Direct only'));
		via.default = 'proxy-first';
		via.rmempty = false;
		var limit = sources.option(form.Value, 'max_nodes', _('Maximum imported nodes'));
		limit.datatype = 'range(1,5000)';
		limit.default = '1000';
		limit.rmempty = false;
		var refresh = sources.option(form.Button, '_refresh', _('Subscription update'));
		refresh.inputtitle = function(section) {
			var name = sourceName(id.formvalue(section) || uci.get('resproxy', section, 'id'));
			return loading && name === activeID ? _('Loading…') : _('Refresh');
		};
		refresh.inputstyle = 'action';
		var renderButton = refresh.renderWidget;
		refresh.renderWidget = function() {
			var widget = renderButton.apply(this, arguments);
			widget.querySelectorAll('button').forEach(function(button) { button.disabled = loading || map.readonly; });
			return widget;
		};

		var save = sources.option(form.Button, '_save_load', '');
		save.inputtitle = function(section) {
			var name = sourceName(id.formvalue(section) || uci.get('resproxy', section, 'id'));
			return loading && name === activeID ? _('Loading…') : _('Save and load');
		};
		save.inputstyle = 'action';
		save.renderWidget = refresh.renderWidget;

		function sourceState(row) {
			return status[sourceName(uci.get('resproxy', row.dataset.sectionId, 'id'))] || {};
		}
		function decorateSource(row) {
			var section = row.dataset.sectionId;
			var state = sourceState(row);
			if (openStates[section] == null) openStates[section] = !state.nodes || state.state === 'error';
			var fields = Array.from(row.children);
			var action = row.querySelector('.cbi-value[data-name="_refresh"]');
			var summary = E('div', { 'class':'sbg-source-status', role:'status', 'aria-live':'polite' });
			var settings = E('details', { 'class':'sbg-source-settings' }, [ E('summary', {}, _('Settings')) ]);
			settings.open = openStates[section];
			settings.addEventListener('toggle', function() { openStates[section] = settings.open; updateButtons(); });
			fields.forEach(function(field) { if (field !== action) settings.appendChild(field); });
			row.appendChild(E('div', { 'class':'sbg-source-toolbar' }, [ summary, action ]));
			row.appendChild(settings);
			paintSource(row);
		}
		function paintSource(row) {
			var state = sourceState(row), parts = [];
			parts.push(statusFailed ? _('Status unavailable') : state.nodes ? _('%d nodes').format(state.nodes) : _('No nodes loaded'));
			if (state.state === 'loading') parts.push(_('Loading…'));
			if (state.updated_at) {
				var date = new Date(state.updated_at);
				if (!isNaN(date.getTime())) parts.push(_('Updated: %s').format(date.toLocaleString()));
			}
			if (state.state === 'success' && (state.added || state.removed))
				parts.push(_('Added: %d · Removed: %d').format(state.added || 0, state.removed || 0));
			var errors = { network:_('Subscription download failed'), server:_('Subscription server error'),
				unreachable:_('No reachable nodes; previous nodes retained'), interrupted:_('Loading interrupted'), failed:_('Subscription update failed') };
			if (state.state === 'error') parts.push(errors[state.error] || errors.failed);
			var summary = row.querySelector('.sbg-source-status');
			if (summary && summary.textContent !== parts.join(' · ')) summary.textContent = parts.join(' · ');
			summary.classList.toggle('sbg-source-error', state.state === 'error');
			row.querySelector('[data-name="_refresh"]').hidden = !state.nodes;
		}
		function finishLoading(success) {
			if (map.root && activeID) map.root.querySelectorAll('.cbi-section-node[data-section-id]').forEach(function(row) {
				if (sourceName(uci.get('resproxy', row.dataset.sectionId, 'id')) !== activeID) return;
				var settings = row.querySelector('.sbg-source-settings');
				if (settings) {
					var focused = settings.contains(document.activeElement);
					settings.open = !success;
					openStates[row.dataset.sectionId] = !success;
					if (success && focused) row.querySelector('[data-name="_refresh"] button').focus();
				}
			});
			loading = false; activeID = ''; pending = null; updateButtons();
		}
		function updateStatus() {
			if (!pending && Date.now() - lastPoll < 15000) return Promise.resolve();
			lastPoll = Date.now();
			return readSourceStatus().then(function(value) {
				status = value; statusFailed = false;
				if (map.root) map.root.querySelectorAll('.cbi-section-node[data-section-id]').forEach(paintSource);
				if (pending) {
					var state = status[activeID];
					if (state && state.attempt && state.attempt !== pending.attempt && state.state !== 'loading') {
						finishLoading(state.state === 'success');
					} else if (Date.now() - pending.started > 300000) {
						ui.addNotification(null, E('p', {}, _('Loading is taking longer than expected.')));
						finishLoading(false);
					}
				}
			}).catch(function() {
				statusFailed = true;
				if (map.root) map.root.querySelectorAll('.cbi-section-node[data-section-id]').forEach(paintSource);
				if (pending && Date.now() - pending.started > 300000) finishLoading(false);
			});
		}
		function updateButtons() {
			if (!map.root) return;
			map.root.querySelectorAll('.cbi-value[data-name="_refresh"] button, .cbi-value[data-name="_save_load"] button').forEach(function(button) {
				var row = button.closest('.cbi-section-node');
				button.disabled = loading || map.readonly;
				button.classList.toggle('spinning', loading && sourceName(id.formvalue(row.dataset.sectionId) || uci.get('resproxy', row.dataset.sectionId, 'id')) === activeID);
				var action = button.closest('.cbi-value').dataset.name === '_save_load' ? save : refresh;
				button.textContent = action.inputtitle(row.dataset.sectionId);
			});
		}
		refresh.onclick = function(event, section) {
			if (loading || map.readonly) return;
			activeID = sourceName(id.formvalue(section));
			loading = true;
			updateButtons();
			var failure = _('Check the subscription fields.');
			return map.save(null, true).then(function() {
				failure = _('Could not save subscription settings.');
				return commitSourceSettings('resproxy');
			}).then(function() {
				if (uci.get('resproxy', 'main', 'enabled') !== '1') {
					return {code:0};
				}
				failure = _('Could not apply subscription settings.');
				return fs.exec('/etc/init.d/resproxy', ['reload']);
			}).then(function(result) {
				if (result.code !== 0) throw new Error(failure);
				failure = _('Could not start subscription loading.');
				return readSourceStatus().then(function(value) {
					status = value;
					pending = { attempt:(value[activeID] || {}).attempt, started:Date.now() };
					return fs.exec('/usr/libexec/resproxy-refresh', [activeID]);
				});
			}).then(function(result) {
				if (result.code !== 0) throw new Error(failure);
				return ui.changes.init().then(function() {
					lastPoll = 0;
					return updateStatus();
				});
			}).catch(function() {
				finishLoading(false);
				ui.addNotification(null, E('p', {}, failure));
			}).finally(function() {
				if (!pending) finishLoading(false);
			});
		};
		save.onclick = refresh.onclick;
		if (uci.get('resproxy', 'main', 'subscription_url')) {
			var legacy = map.section(form.NamedSection, 'main', 'main', _('Existing single source'));
			legacy.addremove = false;
			legacy.description = _('This older source is used only while no sources are configured above. When migrating, reuse its Provider ID to preserve node identity, then clear the older URL.');
			var legacyURL = legacy.option(form.Value, 'subscription_url', _('Subscription URL'));
			legacyURL.datatype = 'url';
			var legacyID = legacy.option(form.Value, 'provider_id', _('Provider ID'));
			legacyID.rmempty = false;
		}
		return map.render().then(function(node) { updateButtons(); poll.add(updateStatus, 3); return sbgTheme.decorate(E('div', {}, [node, plugins.render()])); });
	}
});
