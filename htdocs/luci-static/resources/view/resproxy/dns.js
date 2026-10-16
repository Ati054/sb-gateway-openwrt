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
		var map = new form.Map('resproxy', _('DNS клиентов LAN'));
		var section = map.section(form.NamedSection, 'main', 'main', _('Режим DNS'));
		section.addremove = false;

		if (data[1].external === 'https-dns-proxy' || uci.get('resproxy', 'main', 'dns_handoff_confirmed') === '1') {
			var handoff = section.option(form.Flag, 'dns_handoff_confirmed', _('Остановить https-dns-proxy и передать DNS'));
			handoff.default = '0';
			handoff.rmempty = false;
			handoff.description = _('Подтверждаю переключение. Настройки службы сохраняются; её состояние восстановится при возврате к DNS OpenWrt или ошибке.');
		}

		var proxyProtocol = section.option(form.ListValue, 'dns_proxy_protocol', _('Протокол через прокси'));
		proxyProtocol.value('doh', 'DNS-over-HTTPS (DoH)');
		proxyProtocol.value('dot', 'DNS-over-TLS (DoT)');
		proxyProtocol.value('tcp', _('Обычный DNS/TCP'));
		proxyProtocol.default = 'doh';
		proxyProtocol.rmempty = false;

		var proxyProvider = section.option(form.ListValue, 'dns_proxy_provider', _('Провайдер DNS через прокси'));
		proxyProvider.value('cloudflare', 'Cloudflare');
		proxyProvider.value('google', 'Google');
		proxyProvider.value('yandex', 'Яндекс');
		proxyProvider.value('quad9', 'Quad9');
		proxyProvider.value('adguard', 'AdGuard');
		proxyProvider.value('custom', _('Другой адрес'));
		proxyProvider.default = 'custom';
		proxyProvider.rmempty = false;
		var proxy = section.option(form.Value, 'dns_proxy_url', _('Адрес DNS через прокси'));
		proxy.default = 'https://1.1.1.1/dns-query';
		proxy.placeholder = 'https://… · tls://… · tcp://IP:53';
		proxy.rmempty = false;
		proxy.depends('dns_proxy_provider', 'custom');

		var directProtocol = section.option(form.ListValue, 'dns_direct_protocol', _('Протокол через WAN'));
		directProtocol.value('doh', 'DNS-over-HTTPS (DoH)');
		directProtocol.value('dot', 'DNS-over-TLS (DoT)');
		directProtocol.value('tcp', _('Обычный DNS/TCP'));
		directProtocol.value('udp', _('Обычный DNS/UDP'));
		directProtocol.default = 'doh';
		directProtocol.rmempty = false;

		var directProvider = section.option(form.ListValue, 'dns_direct_provider', _('Провайдер DNS через WAN'));
		directProvider.value('cloudflare', 'Cloudflare');
		directProvider.value('google', 'Google');
		directProvider.value('yandex', 'Яндекс');
		directProvider.value('quad9', 'Quad9');
		directProvider.value('adguard', 'AdGuard');
		directProvider.value('custom', _('Другой адрес'));
		directProvider.default = 'custom';
		directProvider.rmempty = false;
		var direct = section.option(form.Value, 'dns_direct_url', _('Адрес DNS через WAN'));
		direct.default = 'https://1.1.1.1/dns-query';
		direct.placeholder = 'https://… · tls://… · tcp://IP:53 · udp://IP:53';
		direct.rmempty = false;
		direct.depends('dns_direct_provider', 'custom');

		var port = section.option(form.Value, 'dns_port', _('Локальный порт DNS'));
		port.datatype = 'port';
		port.default = '1053';
		port.rmempty = false;

		return map.render().then(sbgTheme.decorate);
	}
});
