'use strict';
'require baseclass';
'require dom';

// LuCI interprets a bare scalar child as HTML. Package views accept text
// and explicit DOM nodes only, including strings produced by callbacks.
function children(value) {
 if (typeof value === 'function') return function(node) { return children(value(node)); };
 if (value == null || dom.elem(value) || Array.isArray(value)) return value;
 return [String(value)];
}
function create(tag, attributes, content) {
 if (!(attributes instanceof Object) || Array.isArray(attributes)) {
  content = attributes; attributes = {};
 }
 if (Array.isArray(tag)) {
  var fragment = document.createDocumentFragment();
  tag.forEach(function(item) { if (item != null) fragment.appendChild(dom.elem(item) ? item : document.createTextNode(String(item))); });
  return dom.create(fragment, attributes, children(content));
 }
 // Tags are code, never subscription data or an HTML fragment.
 if (typeof tag === 'string' && !/^[a-z][a-z0-9-]*$/i.test(tag)) throw new TypeError('Invalid element name');
 return dom.create(tag, attributes, children(content));
}
return baseclass.extend({ create: create });
