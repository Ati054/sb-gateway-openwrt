'use strict';
'require baseclass';
'require session';
'require ui';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

function decorate(node) {
 var language = (document.documentElement.lang || 'en').replace(/_/g, '-');
 var menuVersion = 'r104-i18n:' + language;
 // LuCI keeps its menu in browser-session storage across package upgrades.
 if (session.getLocalData('sbg-menu-version') !== menuVersion) {
  ui.menu.flushCache();
  if (session.setLocalData('sbg-menu-version', menuVersion))
   window.location.reload();
 }

	if (!document.getElementById('sbg-ui-style'))
		document.head.appendChild(E('link', {
			'id': 'sbg-ui-style',
			'rel': 'stylesheet',
			'href': L.resource('view/resproxy/style.css') + '?v=r104-i18n'
		}));
	node.classList.add('sbg-page');
	node.setAttribute('dir', /^(ar|fa|he|ur)(-|$)/i.test(language) ? 'rtl' : 'ltr');
	return node;
}

return baseclass.extend({ decorate: decorate });
