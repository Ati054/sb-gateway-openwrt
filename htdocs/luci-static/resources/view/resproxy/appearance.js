'use strict';
'require baseclass';
'require session';
'require ui';

function decorate(node) {
 // LuCI keeps its menu in browser-session storage across package upgrades.
 if (session.getLocalData('sbg-menu-version') !== 'r42') {
  ui.menu.flushCache();
  if (session.setLocalData('sbg-menu-version', 'r42'))
   window.location.reload();
 }

	if (!document.getElementById('sbg-ui-style'))
		document.head.appendChild(E('link', {
			'id': 'sbg-ui-style',
			'rel': 'stylesheet',
			'href': L.resource('view/resproxy/style.css') + '?v=r64-health-1635'
		}));
	node.classList.add('sbg-page');
	return node;
}

return baseclass.extend({ decorate: decorate });
