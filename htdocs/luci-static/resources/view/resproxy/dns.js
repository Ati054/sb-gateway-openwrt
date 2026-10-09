'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view';
'require form';
'require uci';
'require fs';

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('resproxy'),
			fs.exec('/usr/libexec/resproxy-status').then(function(result) {
				var status = JSON.parse(result.stdout || '{}');
				return { external: status.external_dns_service || '' };
			}).catch(function() { return { external: '' }; })
		]);
	},
	render: function(data) {
		var map = new form.Map('resproxy', _('DNS клиентов LAN'));
		var section = map.section(form.NamedSection, 'main', 'main', _('Режим DNS'));
		section.addremove = false;

		var mode = section.option(form.ListValue, 'dns_mode', _('Запросы DNS устройств'));
		mode.value('proxy-fallback-wan', _('Через прокси; при отказе всех узлов — через WAN'));
		mode.value('proxy-only', _('Без аварийного WAN для прокси-маршрутов'));
		mode.default = 'proxy-fallback-wan';
		mode.rmempty = false;
		if (uci.get('resproxy', 'main', 'dns_mode') === 'system' || !uci.get('resproxy', 'main', 'dns_mode')) {
			var legacy = section.option(form.DummyValue, '_legacy_dns_mode', _('Прежняя настройка DNS'));
			legacy.cfgvalue = function() {
				return _('Сейчас используется DNS OpenWrt. Выберите режим выше и примените настройки, чтобы включить DNS этого пакета.');
			};
		}
		if (data[1].external === 'https-dns-proxy' || uci.get('resproxy', 'main', 'dns_handoff_confirmed') === '1') {
			var handoff = section.option(form.Flag, 'dns_handoff_confirmed', _('Остановить https-dns-proxy и передать DNS'));
			handoff.default = '0';
			handoff.rmempty = false;
			handoff.description = _('Подтверждаю переключение. Настройки службы сохраняются; её состояние восстановится при возврате к DNS OpenWrt или ошибке.');
			handoff.depends('dns_mode', 'proxy-fallback-wan');
			handoff.depends('dns_mode', 'proxy-only');
		}

		var proxyProtocol = section.option(form.ListValue, 'dns_proxy_protocol', _('Протокол через прокси'));
		proxyProtocol.value('doh', 'DNS-over-HTTPS (DoH)');
		proxyProtocol.value('dot', 'DNS-over-TLS (DoT)');
		proxyProtocol.value('tcp', _('Обычный DNS/TCP'));
		proxyProtocol.default = 'doh';
		proxyProtocol.rmempty = false;
		proxyProtocol.depends('dns_mode', 'proxy-fallback-wan');
		proxyProtocol.depends('dns_mode', 'proxy-only');

		var proxyProvider = section.option(form.ListValue, 'dns_proxy_provider', _('Провайдер DNS через прокси'));
		proxyProvider.value('cloudflare', 'Cloudflare');
		proxyProvider.value('google', 'Google');
		proxyProvider.value('yandex', 'Яндекс');
		proxyProvider.value('quad9', 'Quad9');
		proxyProvider.value('adguard', 'AdGuard');
		proxyProvider.value('custom', _('Другой адрес'));
		proxyProvider.default = 'custom';
		proxyProvider.rmempty = false;
		proxyProvider.depends('dns_mode', 'proxy-fallback-wan');
		proxyProvider.depends('dns_mode', 'proxy-only');
		var proxy = section.option(form.Value, 'dns_proxy_url', _('Адрес DNS через прокси'));
		proxy.default = 'https://1.1.1.1/dns-query';
		proxy.placeholder = 'https://… · tls://… · tcp://IP:53';
		proxy.rmempty = false;
		proxy.depends({ dns_mode: 'proxy-fallback-wan', dns_proxy_provider: 'custom' });
		proxy.depends({ dns_mode: 'proxy-only', dns_proxy_provider: 'custom' });

		var directProtocol = section.option(form.ListValue, 'dns_direct_protocol', _('Протокол через WAN'));
		directProtocol.value('doh', 'DNS-over-HTTPS (DoH)');
		directProtocol.value('dot', 'DNS-over-TLS (DoT)');
		directProtocol.value('tcp', _('Обычный DNS/TCP'));
		directProtocol.value('udp', _('Обычный DNS/UDP'));
		directProtocol.default = 'doh';
		directProtocol.rmempty = false;
		directProtocol.depends('dns_mode', 'proxy-fallback-wan');
		directProtocol.depends('dns_mode', 'proxy-only');

		var directProvider = section.option(form.ListValue, 'dns_direct_provider', _('Провайдер DNS через WAN'));
		directProvider.value('cloudflare', 'Cloudflare');
		directProvider.value('google', 'Google');
		directProvider.value('yandex', 'Яндекс');
		directProvider.value('quad9', 'Quad9');
		directProvider.value('adguard', 'AdGuard');
		directProvider.value('custom', _('Другой адрес'));
		directProvider.default = 'custom';
		directProvider.rmempty = false;
		directProvider.depends('dns_mode', 'proxy-fallback-wan');
		directProvider.depends('dns_mode', 'proxy-only');
		var direct = section.option(form.Value, 'dns_direct_url', _('Адрес DNS через WAN'));
		direct.default = 'https://1.1.1.1/dns-query';
		direct.placeholder = 'https://… · tls://… · tcp://IP:53 · udp://IP:53';
		direct.rmempty = false;
		direct.depends({ dns_mode: 'proxy-fallback-wan', dns_direct_provider: 'custom' });
		direct.depends({ dns_mode: 'proxy-only', dns_direct_provider: 'custom' });

		var port = section.option(form.Value, 'dns_port', _('Локальный порт DNS'));
		port.datatype = 'port';
		port.default = '1053';
		port.rmempty = false;
		port.depends('dns_mode', 'proxy-fallback-wan');
		port.depends('dns_mode', 'proxy-only');

		return map.render().then(sbgTheme.decorate);
	}
});
