'use strict';
'require baseclass';
'require ui';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

var lifetime = 6 * 60 * 60 * 1000;
function storageKey(page) { return 'resproxy.draft.' + L.env.sessionid + '.' + page; }
function read(page) {
 try {
  var item = JSON.parse(sessionStorage.getItem(storageKey(page)) || 'null');
  return item && Date.now() - item.at < lifetime ? item.value : null;
 } catch (_) { return null; }
}
function write(page, value) {
 if (value == null) { sessionStorage.removeItem(storageKey(page)); return; }
 var text = JSON.stringify({ at: Date.now(), value: value });
 if (text.length > 1024 * 1024) throw new Error(_('The draft is too large. Save the form before continuing.'));
 sessionStorage.setItem(storageKey(page), text);
}

// Changes arriving during a write get a subsequent write. Failed saves retain
// their version and reject Flush, preventing Apply from publishing stale data.
function queue(save, onError) {
 var version = 0, saved = 0, timer = null, flight = null, disposed = false;
 function flush() {
  if (timer != null) { clearTimeout(timer); timer = null; }
  if (flight) return flight.then(flush);
  if (disposed || version === saved) return Promise.resolve();
  var writing = version;
  flight = Promise.resolve().then(save).then(function() { saved = writing; }, function(error) { onError(error); throw error; });
  return flight.then(function() { flight = null; return flush(); }, function(error) { flight = null; throw error; });
 }
 return {
  mark: function() {
   if (disposed) return;
   version++;
   if (timer != null) clearTimeout(timer);
   timer = setTimeout(function() { timer = null; flush().catch(function() {}); }, 700);
  },
  flush: flush,
  pending: function() { return version !== saved; },
  discard: function() { saved = version; if (timer != null) clearTimeout(timer); timer = null; },
  dispose: function() { disposed = true; if (timer != null) clearTimeout(timer); timer = null; }
 };
}
function attach(map, page) {
 var snapshot = read(page) || { fields: {}, dirty: false }, root = null, explicit = false, saving = false, saveFlight = null;
 var legacySnapshot = snapshot.dirty == null;
 function comparable(value) {
  return JSON.stringify((Array.isArray(value) ? value : [value]).filter(function(item) { return item != null && item !== ''; }).map(String));
 }
 var lastError = '', saveOriginal = map.save, parseOriginal = map.parse;
 var parsing = false, lastStaged = {};
 function report(error) {
  var message = error.message || String(error);
  if (message !== lastError) ui.addNotification(null, E('p', {}, [_('Draft not saved. Apply was not started.'), ' ', message]), 'error');
  lastError = message;
 }
 function fields(fn) {
  map.children.forEach(function(section) {
   section.cfgsections().forEach(function(sid) {
    section.children.forEach(function(field) { if (!field._sbgOwnDraft && typeof field.formvalue === 'function' && typeof field.renderWidget === 'function') fn(field, sid); });
   });
  });
 }
 function capture() {
  if (!root || map.readonly || saving) return;
  var next = {};
  fields(function(field, sid) {
   if (!map.findElement('id', field.cbid(sid))) return;
   var value = field.formvalue(sid);
   if (value == null) return;
   var key = sid + '.' + field.option, previous = snapshot.fields[key];
   next[key] = { value: value, base: previous ? previous.base : field.cfgvalue(sid), staged: previous && previous.staged };
  });
  snapshot.fields = next;
  write(page, snapshot);
 }
 function freeze(selector) {
  var elements = Array.from(root.querySelectorAll(selector)).map(function(element) { var state = element.disabled; element.disabled = true; return [element, state]; });
  return function() { elements.forEach(function(pair) { if (pair[0].isConnected) pair[0].disabled = pair[1]; }); };
 }
 var saves = queue(function() {
  var release = freeze('button');
  map._sbgDraftStage = !explicit;
  map.checkDepends();
  // Save to the existing rpcd UCI session (RAM). Do not commit, reload the
  // form or collapse its cards while the user is typing.
  var staged = {};
  return Promise.resolve().then(function() { return map.parse(); }).then(function() {
   fields(function(field, sid) { staged[sid + '.' + field.option] = field.formvalue(sid); });
   return map.data.save();
  }).then(function() {
   Object.keys(staged).forEach(function(key) { if (snapshot.fields[key]) snapshot.fields[key].staged = staged[key]; });
   lastStaged = staged; write(page, snapshot); lastError = '';
  }).finally(function() { map._sbgDraftStage = false; release(); });
 }, function(error) { if (explicit) report(error); });
 function flush(strict) {
  if (map.readonly) return Promise.resolve();
  capture(); explicit = strict !== false; saves.mark();
  return saves.flush().finally(function() { explicit = false; });
 }
 // Native fields cache their initial cfgvalue. During parsing compare with
 // the acknowledged RAM draft, so a revert is saved without rewriting every
 // unchanged field on every keystroke. Rendering retains native UCI values.
 map.parse = function() {
  var args = arguments; parsing = true;
  return Promise.resolve().then(function() { return parseOriginal.apply(map, args); }).finally(function() { parsing = false; });
 };
 // Inject the local value only into initial rendering.
 fields(function(field, sid) {
  if (field._sbgDraftWrapped) return;
  field._sbgDraftWrapped = true;
  var cfgvalue = field.cfgvalue;
  field.cfgvalue = function(section) {
   var key = section + '.' + this.option;
   if (parsing && Object.prototype.hasOwnProperty.call(lastStaged, key)) return lastStaged[key];
   return cfgvalue.apply(this, arguments);
  };
  var render = field.renderWidget;
  field.renderWidget = function(section, index, value) {
   var args = Array.prototype.slice.call(arguments), stored = snapshot.fields[section + '.' + this.option];
   if (!root && stored) {
    if (JSON.stringify(value) === JSON.stringify(stored.base) || JSON.stringify(value) === JSON.stringify(stored.staged)) {
     args[2] = stored.value;
     // Older drafts have no edit marker. Preserve real edits, while an
     // unchanged rendered default is still only a read-only visit.
     if (legacySnapshot && comparable(stored.value) !== comparable(stored.base == null ? this.default : stored.base)) snapshot.dirty = true;
    }
    else delete snapshot.fields[section + '.' + this.option];
   }
   return render.apply(this, args);
  };
 });
 map.save = function() {
  var args = arguments;
  if (saveFlight) return saveFlight;
  saveFlight = flush().then(function() {
   saving = true;
   var release = freeze('input,select,textarea,button');
   return Promise.resolve().then(function() { return saveOriginal.apply(map, args); }).then(function(node) {
    saves.discard(); lastStaged = {}; snapshot = { fields: {}, dirty: false }; write(page, null); return node;
   }).finally(function() { saving = false; release(); });
  }).finally(function() { saveFlight = null; });
  return saveFlight;
 };
 var applyOriginal = ui.changes.apply;
 ui.changes.apply = function() {
  var args = arguments;
  return Promise.resolve(saveFlight).then(flush).then(function() { return applyOriginal.apply(ui.changes, args); });
 };
 function navigate(event) {
  var link = event.target.closest && event.target.closest('a[href]');
  if (!link || event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || link.target || link.hasAttribute('download')) return;
  var url = new URL(link.href, window.location.href);
  if (url.origin !== window.location.origin || url.pathname === window.location.pathname || !saves.pending()) return;
  event.preventDefault();
  // Incomplete forms remain in sessionStorage. Navigation must never trap
  // the user on an editor; only Save/Apply requires a valid complete form.
  flush(false).catch(function() {}).then(function() { window.location.assign(url.href); });
 }
 function leave() { try { capture(); } catch (error) { report(error); } saves.dispose(); }
 function revert() { saves.discard(); saves.dispose(); write(page, null); }
 document.addEventListener('click', navigate);
 document.addEventListener('uci-reverted', revert);
 window.addEventListener('pagehide', leave, { once: true });
 return {
  bind: function(node) {
   root = node;
   function changed() { if (saving || map.readonly) return; try { snapshot.dirty = true; capture(); saves.mark(); } catch (error) { report(error); } }
   ['input', 'change', 'widget-change'].forEach(function(type) { node.addEventListener(type, changed); });
   // Restored invalid values remain local and visible until corrected.
   if (snapshot.dirty && Object.keys(snapshot.fields).length) saves.mark();
   return node;
  },
  flush: flush
 };
}
return baseclass.extend({ read: read, write: write, queue: queue, attach: attach });
