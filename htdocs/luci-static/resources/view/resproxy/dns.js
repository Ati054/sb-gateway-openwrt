'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view';
'require form';
'require uci';
'require view.resproxy.runtime_status as monitor';

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('resproxy'),
			monitor.externalDNS().then(function(name) {return {external:name};}).catch(function() {return {external:''};})
		]);
	},
	render: function(data) {
		var map = new form.Map('resproxy', _('LAN client DNS'));
		var section = map.section(form.NamedSection, 'main', 'main', _('DNS mode'));
		section.addremove = false;

		if (data[1].external === 'https-dns-proxy' || uci.get('resproxy', 'main', 'dns_handoff_confirmed') === '1') {
			var handoff = section.option(form.Flag, 'dns_handoff_confirmed', _('Stop https-dns-proxy and take over DNS'));
			handoff.default = '0';
			handoff.rmempty = false;
			handoff.description = _('I confirm the switch. Service settings are kept; its previous state is restored when returning to OpenWrt DNS or if takeover fails.');
		}

		var proxyProtocol = section.option(form.ListValue, 'dns_proxy_protocol', _('Protocol through proxy'));
		proxyProtocol.value('doh', 'DNS-over-HTTPS (DoH)');
		proxyProtocol.value('dot', 'DNS-over-TLS (DoT)');
		proxyProtocol.value('tcp', _('Plain DNS/TCP'));
		proxyProtocol.default = 'doh';
		proxyProtocol.rmempty = false;

		var proxyProvider = section.option(form.ListValue, 'dns_proxy_provider', _('DNS provider through proxy'));
		proxyProvider.value('cloudflare', 'Cloudflare');
		proxyProvider.value('google', 'Google');
		proxyProvider.value('yandex', _('Yandex'));
		proxyProvider.value('quad9', 'Quad9');
		proxyProvider.value('adguard', 'AdGuard');
		proxyProvider.value('custom', _('Custom address'));
		proxyProvider.default = 'custom';
		proxyProvider.rmempty = false;
		var proxy = section.option(form.Value, 'dns_proxy_url', _('DNS address through proxy'));
		proxy.default = 'https://1.1.1.1/dns-query';
		proxy.placeholder = 'https://… · tls://… · tcp://IP:53';
		proxy.rmempty = false;
		proxy.depends('dns_proxy_provider', 'custom');

		var directProtocol = section.option(form.ListValue, 'dns_direct_protocol', _('Protocol through WAN'));
		directProtocol.value('doh', 'DNS-over-HTTPS (DoH)');
		directProtocol.value('dot', 'DNS-over-TLS (DoT)');
		directProtocol.value('tcp', _('Plain DNS/TCP'));
		directProtocol.value('udp', _('Plain DNS/UDP'));
		directProtocol.default = 'doh';
		directProtocol.rmempty = false;

		var directProvider = section.option(form.ListValue, 'dns_direct_provider', _('DNS provider through WAN'));
		directProvider.value('cloudflare', 'Cloudflare');
		directProvider.value('google', 'Google');
		directProvider.value('yandex', _('Yandex'));
		directProvider.value('quad9', 'Quad9');
		directProvider.value('adguard', 'AdGuard');
		directProvider.value('custom', _('Custom address'));
		directProvider.default = 'custom';
		directProvider.rmempty = false;
		var direct = section.option(form.Value, 'dns_direct_url', _('DNS address through WAN'));
		direct.default = 'https://1.1.1.1/dns-query';
		direct.placeholder = 'https://… · tls://… · tcp://IP:53 · udp://IP:53';
		direct.rmempty = false;
		direct.depends('dns_direct_provider', 'custom');

		var port = section.option(form.Value, 'dns_port', _('Local DNS port'));
		port.datatype = 'port';
		port.default = '1053';
		port.rmempty = false;

		return map.render().then(sbgTheme.decorate);
	}
});
