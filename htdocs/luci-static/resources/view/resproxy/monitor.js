'use strict';
'require baseclass';
'require form';
'require uci';


function interval(section, key, title, fallback, minimum, maximum) {
	var option = section.option(form.Value, key, title);
	option.datatype = 'range(%d,%d)'.format(minimum, maximum);
	option.default = String(fallback);
	option.rmempty = false;
	return option;
}

return baseclass.extend({
	addTo: function(map) {
		var section = map.section(form.NamedSection, 'main', 'main', _('Route monitoring — shared settings'));
		section.addremove = false;
		section.description = _('These settings apply to all route lists.');
        interval(section,'active_liveness_seconds',_('Active availability (seconds)'),3,2,30);
        interval(section,'active_check_seconds',_('HTTPS latency (seconds)'),60,1,3600);
        var batch = interval(section,'probe_batch_size',_('Concurrent node checks'),10,1,64);
        batch.load = function(section) { var value = form.Value.prototype.load.call(this,section); return value == null || value === '0' ? '10' : value; };
        batch.description = _('Shared across route lists. Emergency recovery has priority.');
        var improvement = interval(section,'switch_improvement_ms',_('Response improvement for URLTest (ms)'),50,0,30000);
        improvement.load = function(section) {
            var value = form.Value.prototype.load.call(this,section);
            if (value != null) return value;
            // Preserve the existing first URLTest setting until the shared control is saved.
            var legacy = uci.sections('resproxy','policy').find(function(policy) { return !policy.mode || policy.mode === 'best'; });
            return legacy && legacy.switch_improvement_ms != null ? legacy.switch_improvement_ms : '50';
        };
        improvement.description = _('Only for URLTest (Best available). Switch after two fresh confirmations of this HTTPS latency improvement.');
        var history = interval(section,'history_limit_kib',_('Node history RAM budget (KiB)'),2048,256,16384);
        history.description = _('Shared by all route lists. Older history is removed when the budget is reached. Current routing evidence is kept. History survives service restart and is cleared on router reboot.');
		var render = section.renderContents;
		section.renderContents = function() {
			var node = render.apply(this, arguments);
			node.classList.add('sbg-shared-settings', 'sbg-route-monitor');
			return node;
		};
		return section;
	}
});
