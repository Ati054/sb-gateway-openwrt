'use strict';
'require baseclass';

function normalize(value) { return String(value || '').toLowerCase().trim().replace(/[\uFE0E\uFE0F]/g, ''); }
function parse(value) {
 var raw = String(value || ''), terms = [];
 if (new TextEncoder().encode(raw).length > 512) return { terms:[], error:'size' };
 if (/[\u0000-\u001F\u007F-\u009F]/.test(raw)) return { terms:[], error:'control' };
 while (raw.length) {
  raw = raw.trim();
  if (!raw) break;
  if (raw[0] === ',' || raw[0] === '|') { raw = raw.slice(1); continue; }
  if (raw[0] !== '"') {
   var end = raw.search(/[,|]/);
   if (end < 0) end = raw.length;
   var fragment = normalize(raw.slice(0, end));
   if (fragment) terms.push({ text:fragment, exact:false });
   raw = raw.slice(end); continue;
  }
  var text = '', closed = false;
  for (var i = 1; i < raw.length; i++) {
   if (raw[i] !== '"') { text += raw[i]; continue; }
   if (raw[i+1] === '"') { text += '"'; i++; continue; }
   raw = raw.slice(i+1).trim(); closed = true; break;
  }
  if (!closed) return { terms:[], error:'unclosed' };
  text = normalize(text);
  if (!text) return { terms:[], error:'empty' };
  if (raw && raw[0] !== ',' && raw[0] !== '|') return { terms:[], error:'separator' };
  terms.push({ text:text, exact:true });
 }
 return { terms:terms, error:'' };
}
function matches(label, parsed, empty, extra) {
 if (parsed.error) return false;
 if (!parsed.terms.length) return !!empty;
 label = normalize(label);
 var search = extra ? label + ' ' + normalize(extra) : label;
 return parsed.terms.some(function(term) { return term.exact ? label === term.text : search.indexOf(term.text) >= 0; });
}
function errorMessage(error) {
 return error === 'unclosed' ? _('Close the double quotes around the full name.') :
  error === 'empty' ? _('A quoted full name cannot be empty.') :
  error === 'separator' ? _('Separate names with a comma or | after a closing quote.') :
  error === 'control' ? _('Name filters cannot contain control characters.') :
  error === 'size' ? _('The name filter is too long. Shorten the list.') : '';
}
return baseclass.extend({ parse:parse, matches:matches, errorMessage:errorMessage });
