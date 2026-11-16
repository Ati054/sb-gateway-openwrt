'use strict';
'require baseclass';
'require uci';
'require view.resproxy.unicode_text as unicodeText';

function labels(type) {
 var result=Object.create(null);
 uci.sections('resproxy',type).forEach(function(item) { if(item.id) result[item.id]=item.name || item.id; });
 if(type==='subscription' && uci.get('resproxy','main','subscription_url')) {
  var id=uci.get('resproxy','main','provider_id') || 'primary';
  if(!result[id]) result[id]=uci.get('resproxy','main','provider_name') || id;
 }
 return result;
}
function title(type,id) { return labels(type)[id] || id || ''; }
function canonicalURL(value) { try {return new URL(String(value || '').trim()).href;} catch(error) {return ''; } }
function ensureID(section, type, address) {
 var current=uci.get('resproxy',section,'id');
 if (current) return current;
 // Anonymous UCI section names survive edits. Persist the generated value;
 // display-name changes must never change node identities or route references.
 var prefix=type === 'subscription' ? 'sub-' : 'route-';
 var suffix=String(section).toLowerCase().replace(/[^a-z0-9]/g,'').slice(-20) || 'new';
 var used=uci.sections('resproxy',type).map(function(item) { return String(item.id || '').toLowerCase(); });
 if(type==='subscription') {
  var legacyURL=canonicalURL(uci.get('resproxy','main','subscription_url'));
  var legacyID=uci.get('resproxy','main','provider_id') || 'primary';
  if(legacyURL && legacyURL===canonicalURL(address || uci.get('resproxy',section,'url')) && used.indexOf(legacyID.toLowerCase())<0) {
   uci.set('resproxy',section,'id',legacyID);
   return legacyID;
  }
 }
 var candidate=prefix+suffix, index=1;
 while (used.indexOf(candidate)>=0) candidate=prefix+suffix+'-'+index++;
 uci.set('resproxy',section,'id',candidate);
 return candidate;
}
function bind(option, section, type) {
 var key=option.option, idKey=type==='legacy' ? 'provider_id' : 'id';
 option.rmempty=false;
 option.cfgvalue=function(sid) { return uci.get('resproxy',sid,key) || uci.get('resproxy',sid,idKey) || (type==='legacy' ? 'primary' : ''); };
 option.validate=function(sid,value) {
  var name=unicodeText.normalize(value);
  if (!name || Array.from(name).length>120 || /[\u0000-\u001f\u007f-\u009f]/.test(name)) return _('Enter a name of up to 120 characters.');
  return !section.cfgsections().some(function(other) {
   return other!==sid && unicodeText.fold(option.formvalue(other))===unicodeText.fold(name);
  }) || _('This name is already used.');
 };
 option.write=function(sid,value) {
  if(type!=='legacy') {
   var urls=type==='subscription' ? option.map.lookupOption('url',sid) : [];
   ensureID(sid,type,urls.length ? urls[0].formvalue(sid) : '');
  }
  uci.set('resproxy',sid,key,unicodeText.normalize(value));
 };
 var render=option.renderWidget;
 option.renderWidget=function() {
  var widget=render.apply(this,arguments), input=widget.matches('input') ? widget : widget.querySelector('input');
  if(input) input.setAttribute('dir','auto');
  return widget;
 };
 return option;
}
return baseclass.extend({title:title, labels:labels, ensureID:ensureID, bind:bind});
