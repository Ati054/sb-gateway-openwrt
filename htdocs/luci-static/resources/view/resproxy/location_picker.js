'use strict';
'require baseclass';
'require form';
'require uci';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

function list(value) { return Array.isArray(value) ? value.slice() : (value ? String(value).trim().split(/\s+/) : []); }
function name(value) { return String(value || '').toLowerCase().trim().replace(/[\uFE0E\uFE0F]/g, ''); }
function parts(value) { return String(value || '').split(',').map(name).filter(Boolean); }
function city(value) { return String(value || '').trim().replace(/\s+/g, ' ').replace(/^[^\p{L}\p{N}]+/u, '').toLowerCase(); }
function nodeLabel(node) {
 var title = String(node.label || ''), protocol = String(node.protocol || '').toLowerCase();
 var mentioned = protocol === 'hysteria2' ? /(?:^|[^a-z0-9])(?:hysteria[\s_-]*2|hy[\s_-]*2)(?:$|[^a-z0-9])/i.test(title) :
  name(title).split(/[^a-z0-9]+/).indexOf(protocol) >= 0;
 var parts = [title, node.subscription];
 if (protocol && !mentioned) parts.push(protocol === 'hysteria2' ? 'Hysteria 2' : protocol.toUpperCase());
 return parts.filter(Boolean).join(' · ');
}
function matches(token, node, regions) {
 var split = token.indexOf(':'), kind = token.slice(0, split), value = token.slice(split + 1);
 if (kind === 'all') return value === '*';
 if (kind === 'country') return node.country === value;
 if (kind === 'subscription') return node.subscription === decodeURIComponent(value);
 if (kind === 'location') return node.value.slice(9).indexOf(value) === 0;
 if (kind === 'city') {
  var colon = value.indexOf(':');
  return (value.slice(0, colon) === '*' || value.slice(0, colon) === node.country) && city(decodeURIComponent(value.slice(colon + 1))) === city(node.city);
 }
 if (kind === 'region') {
  if (value !== 'other') return !!((regions[value] || {})[node.country]);
  return !Object.keys(regions).some(function(key) { return !!regions[key][node.country]; });
 }
 return false;
}
function eligible(state, node) {
 if (state.kind !== 'filter') return true;
 if (state.source !== '*' && state.source !== node.subscription) return false;
 if (state.countries.length && state.countries.indexOf(node.country) < 0) return false;
 if (state.protocols.length && state.protocols.indexOf(node.protocol) < 0) return false;
 var label = name(node.label), words = label.split(/[^\p{L}\p{Nd}]+/u);
 return parts(state.include).every(function(part) { return label.indexOf(part) >= 0; }) &&
  !parts(state.exclude).some(function(part) { return words.indexOf(part) >= 0; });
}
function candidates(state, inventory) {
 var claimed = {}, result = [];
 var order = state.kind === 'filter' ? ['all:*'] : state.order;
 order.forEach(function(token) {
  inventory.nodes.forEach(function(node) {
   if (!claimed[node.id] && matches(token, node, inventory.regions) && eligible(state, node)) {
    claimed[node.id] = true; result.push(node);
   }
  });
 });
 return result;
}
function picker(option, section, hidden, state, inventory) {
 var regions = inventory.regions || {}, nodes = inventory.nodes || [], locations = inventory.locations || [];
 inventory = { regions: regions, nodes: nodes, locations: locations };
 var regionTitles = { europe:_('Europe'), asia:_('Asia'), america:_('Americas'), africa:_('Africa'), oceania:_('Oceania'), other:_('Other regions') };
 var regionOrder = ['europe', 'asia', 'america', 'africa', 'oceania', 'other'];
 var selectionMode = uci.get('resproxy', section, 'mode') || 'best';
 function countryRegion(code) { return regionOrder.find(function(key) { return key !== 'other' && (regions[key] || {})[code]; }) || 'other'; }
 function groupedCountries(codes) {
  return regionOrder.map(function(key) { return { key:key, countries:codes.filter(function(code) { return countryRegion(code) === key; }).sort(function(a, b) { return countryLabel(a).localeCompare(countryLabel(b), languages); }) }; }).filter(function(group) { return group.countries.length; });
 }
 var languages = (document.documentElement.lang || 'ru');
 var displayNames = typeof Intl.DisplayNames === 'function' ? new Intl.DisplayNames([languages], { type: 'region' }) : null;
 function countryLabel(code) { return code ? (displayNames ? displayNames.of(code) : code) + ' (' + code + ')' : _('Without country'); }
 var sourceIDs = [];
 nodes.forEach(function(node) { if (sourceIDs.indexOf(node.subscription) < 0) sourceIDs.push(node.subscription); });
 uci.sections('resproxy', 'source').forEach(function(source) { if (source.id && sourceIDs.indexOf(source.id) < 0) sourceIDs.push(source.id); });
 if (state.source !== '*' && sourceIDs.indexOf(state.source) < 0) sourceIDs.push(state.source);
 var countries = Array.from(new Set(nodes.map(function(node) { return node.country; }).filter(Boolean))).sort();
 state.countries.forEach(function(code) { if (countries.indexOf(code) < 0) countries.push(code); });
 var root = E('div', { 'class': 'sbg-location-picker', 'data-kind':state.kind });
 var browser = E('div', { 'class': 'sbg-location-browser' });
 var chosen = E('div', { 'class': 'sbg-location-chosen' });
 var preview = E('div', { 'class': 'sbg-location-preview', 'aria-live': 'polite' });
 var search = E('input', { type: 'search', placeholder: _('Country, city or node name'), 'aria-label': _('Find in list') });
 var query = '', expanded = {};
 root.addEventListener('sbg-selection-mode', function(event) { selectionMode = event.detail; renderChosen(); });
 function sync() {
  var element = option.getUIElement(section);
  if (element) { element.setValue(JSON.stringify(state)); element.triggerValidation(); }
  root.dispatchEvent(new CustomEvent('widget-change', { bubbles: true }));
  renderChosen(); renderPreview(); renderFilterSummaries();
 }
 function refresh(token) {
  Array.prototype.forEach.call(browser.querySelectorAll('details'), function(detail) { expanded[detail.dataset.group] = detail.open; });
  renderBrowser(); sync();
  if (token) Array.prototype.some.call(browser.querySelectorAll('input[type=checkbox]'), function(input) {
   if (input.value === token) { input.focus(); return true; } return false;
  });
 }
 function label(token) {
  if (token.indexOf('country:') === 0) return countryLabel(token.slice(8));
  if (token.indexOf('region:') === 0) return regionTitles[token.slice(7)] || token;
  if (token.indexOf('subscription:') === 0) return _('Subscription: %s').format(decodeURIComponent(token.slice(13)));
  var place = locations.find(function(item) { return item.value === token; });
  if (place) return countryLabel(place.country) + ' / ' + place.city;
  var node = nodes.find(function(item) { return matches(token, item, regions); });
  return node ? nodeLabel(node) : _('Unavailable selection: %s').format(token);
 }
 function button(text, title, action, disabled) {
  return E('button', { type: 'button', 'class': 'cbi-button', 'aria-label': title, disabled: disabled ? '' : null, click: action }, text);
 }
 function renderChosen() {
  chosen.replaceChildren();
  chosen.hidden = state.kind !== 'manual';
  if (chosen.hidden) return;
  chosen.appendChild(E('strong', {}, selectionMode === 'priority' ? _('Selected — top to bottom') : _('Selected')));
  if (!state.order.length) chosen.appendChild(E('p', {}, _('Nothing selected. Choose countries, cities or nodes below.')));
  var rows = E('ol');
  state.order.forEach(function(token, index) {
   var title = label(token);
   var actions = [];
   if (selectionMode === 'priority') actions.push(
    button('↑', _('Move up: %s').format(title), function() { state.order.splice(index, 1); state.order.splice(index - 1, 0, token); refresh(); }, index === 0),
    button('↓', _('Move down: %s').format(title), function() { state.order.splice(index, 1); state.order.splice(index + 1, 0, token); refresh(); }, index === state.order.length - 1)
   );
   actions.push(button('×', _('Remove: %s').format(title), function() { state.order.splice(index, 1); refresh(); }));
   rows.appendChild(E('li', {}, [E('span', {}, title), E('span', { 'class':'sbg-location-actions' }, actions)]));
  });
  chosen.appendChild(rows);
 }
 var previewQuery = '', previewOpen = false;
 function renderPreview() {
  var found = candidates(state, inventory);
  preview.replaceChildren(E('strong', {}, _('Matching nodes: %d').format(found.length)));
  if (!found.length) {
   preview.appendChild(E('p', {}, _('No matching nodes. This list will not use another location automatically.'))); return;
  }
  function table() {
   var lookup = E('input', { type:'search', value:previewQuery, placeholder:_('Find a node'), 'aria-label':_('Find a node') });
   var body = E('tbody'), scroll = E('div', { 'class':'sbg-matching-scroll', tabindex:'0', 'aria-label':_('Matching nodes') },
    E('table', { 'class':'sbg-matching-table' }, [
     E('thead', {}, E('tr', {}, [_('Node'), _('Subscription'), _('Protocol')].map(function(title) { return E('th', { scope:'col' }, title); }))), body
    ]));
   function rows() {
    var query = name(lookup.value);
    body.replaceChildren();
    found.forEach(function(node) {
     if (query && name([node.label, node.subscription, node.protocol].join(' ')).indexOf(query) < 0) return;
     body.appendChild(E('tr', { 'data-node-id':node.id }, [E('td', {}, node.label), E('td', {}, node.subscription),
      E('td', {}, node.protocol === 'hysteria2' ? 'Hysteria 2' : String(node.protocol || '').toUpperCase())]));
    });
    if (!body.children.length) body.appendChild(E('tr', {}, E('td', { colspan:'3' }, _('No matching nodes.'))));
    scroll.scrollTop = 0;
   }
   lookup.addEventListener('input', function() { previewQuery = lookup.value; rows(); });
   rows(); return E('div', { 'class':'sbg-matching-list' }, [lookup, scroll]);
  }
  if (state.kind === 'filter') preview.appendChild(table());
  else {
   var detail = E('details', { open:previewOpen ? '' : null }, [E('summary', {}, _('View matching nodes'))]);
   function populate() { if (detail.open && detail.children.length === 1) detail.appendChild(table()); }
   detail.addEventListener('toggle', function() { previewOpen = detail.open; populate(); });
   populate(); preview.appendChild(detail);
  }
 }
 function checkbox(token, title, members) {
  var selected = state.order.indexOf(token) >= 0;
  var parent = !selected && state.order.some(function(other) { return members.length && members.every(function(node) { return matches(other, node, regions); }); });
  var input = E('input', { type: 'checkbox', value: token, checked: selected || parent ? '' : null, disabled: parent ? '' : null, 'aria-label': title });
  input.indeterminate = !selected && !parent && members.some(function(node) { return state.order.some(function(other) { return matches(other, node, regions); }); });
  input.addEventListener('click', function(event) { event.stopPropagation(); });
  input.addEventListener('change', function() {
   if (input.checked) {
    state.order = state.order.filter(function(other) {
     var otherMembers = nodes.filter(function(node) { return matches(other, node, regions); });
     return !otherMembers.length || !otherMembers.every(function(node) { return members.some(function(member) { return member.id === node.id; }); });
    });
    state.order.push(token);
   } else state.order = state.order.filter(function(value) { return value !== token; });
   refresh(token);
  });
  return E('label', {}, [input, E('span', {}, title)]);
 }
 function detail(key, title, children) {
  return E('details', { 'data-group': key, open: expanded[key] || query ? '' : null }, [E('summary', {}, title)].concat(children));
 }
 function visible(node) { return !query || name([node.label, node.country, countryLabel(node.country), regionTitles[countryRegion(node.country)], node.city, node.subscription].join(' ')).indexOf(query) >= 0; }
 function nodeOption(node) { return checkbox(node.value, nodeLabel(node), [node]); }
 function renderBrowser() {
  browser.replaceChildren(); browser.hidden = state.kind !== 'manual'; search.parentNode.hidden = browser.hidden; view.parentNode.hidden = browser.hidden;
  if (browser.hidden) return;
  if (!nodes.length) { browser.appendChild(E('p', {}, _('No subscription nodes loaded. Add or update a subscription first.'))); return; }
  if (state.view === 'subscription') {
   sourceIDs.forEach(function(source) {
    var members = nodes.filter(function(node) { return node.subscription === source; });
    var shown = members.filter(visible);
    if (shown.length) browser.appendChild(detail('source:' + source,
     checkbox('subscription:' + encodeURIComponent(source), source + ' (' + members.length + ')', members), shown.map(nodeOption)));
   });
  } else {
   var codes = Array.from(new Set(nodes.map(function(node) { return node.country; })));
   groupedCountries(codes).forEach(function(group) {
    var regionMembers = nodes.filter(function(node) { return countryRegion(node.country) === group.key; });
    var countryRows = [];
    group.countries.forEach(function(country) {
     var members = regionMembers.filter(function(node) { return node.country === country; });
     var shown = members.filter(visible);
     if (!shown.length) return;
     var content = [];
     Array.from(new Set(shown.map(function(node) { return node.city; }))).sort().forEach(function(town) {
      var townNodes = members.filter(function(node) { return node.city === town; });
      var location = locations.find(function(item) { return item.country === country && item.city === town; });
      var children = townNodes.filter(visible).map(nodeOption);
      if (town && location) content.push(detail('city:' + location.value, checkbox(location.value, town + ' (' + townNodes.length + ')', townNodes), children));
      else content = content.concat(children);
     });
     countryRows.push(detail('country:' + country, country ? checkbox('country:' + country, countryLabel(country) + ' (' + members.length + ')', members) : countryLabel(''), content));
    });
    if (countryRows.length) browser.appendChild(detail('region:' + group.key,
     checkbox('region:' + group.key, regionTitles[group.key] + ' (' + regionMembers.length + ')', regionMembers), countryRows));
   });
  }
 }
 function control(title, node, hint) { return E('label', { 'class': 'sbg-location-control' }, [E('span', {}, title), node].concat(hint ? [E('small', {}, hint)] : [])); }
 function select(title, choices, current, change) {
  var input = E('select', { 'aria-label': title }, choices.map(function(item) { return E('option', { value: item[0], selected: item[0] === current ? '' : null }, item[1]); }));
  input.addEventListener('change', function() { change(input.value); refresh(); }); return input;
 }
 var kind = select(_('Node selection'), [['manual', _('Choose locations')], ['filter', _('By filter')]], state.kind, function(value) { state.kind = value; root.dataset.kind = value; filterBox.hidden = value !== 'filter'; });
 var view = select(_('Display order'), [['countries', _('By country')], ['subscription', _('As in subscription')]], state.view, function(value) { state.view = value; });
 var toolbar = E('div', { 'class': 'sbg-location-toolbar' }, [control(_('Node selection'), kind), control(_('Display order'), view), control(_('Find in list'), search)]);
 root.appendChild(toolbar);
 root.appendChild(chosen);
 root.appendChild(browser);
 var filterBox = E('div', { 'class': 'sbg-location-filter', hidden: state.kind !== 'filter' ? '' : null });
 var filterPrimary = E('div', { 'class':'sbg-location-filter-primary' });
 filterPrimary.appendChild(E('fieldset', { 'class':'sbg-location-filter-section' }, [E('legend', {}, _('Subscription')),
  select(_('Filter subscription'), [['*', _('All subscriptions')]].concat(sourceIDs.map(function(id) { return [id, id]; })), state.source, function(value) { state.source = value; })]));
 filterBox.appendChild(filterPrimary);
 function checkList(choices, key) {
  return E('div', { 'class':'sbg-location-checks' }, choices.map(function(choice) {
   var input = E('input', { type:'checkbox', value:choice[0], 'aria-label':choice[1], checked:state[key].indexOf(choice[0]) >= 0 ? '' : null });
   input.addEventListener('change', function() { state[key] = state[key].filter(function(value) { return value !== choice[0]; }); if (input.checked) state[key].push(choice[0]); sync(); });
   return E('label', {}, [input, E('span', {}, choice[1])]);
  }));
 }
 var filterSummaries = [], regionFilters = [];
 function renderFilterSummaries() {
  regionFilters.forEach(function(group) {
   var selected = group.countries.filter(function(code) { return state.countries.indexOf(code) >= 0; }).length;
   group.input.checked = selected === group.countries.length;
   group.input.indeterminate = selected > 0 && selected < group.countries.length;
   group.content.querySelectorAll('input[type=checkbox]').forEach(function(input) { input.checked = state.countries.indexOf(input.value) >= 0; });
  });
  filterSummaries.forEach(function(item) {
   var values = state[item.key].map(item.label);
   item.summary.textContent = !values.length ? item.all : values.length <= 2 ? values.join(', ') : _('Selected: %d').format(values.length);
   item.summary.title = item.summary.textContent;
   item.reset.hidden = !values.length;
  });
 }
 function filterSection(title, all, key, label, content) {
  var summary = E('summary');
  var reset = button(all, all, function() {
   state[key] = [];
   Array.prototype.forEach.call(content.querySelectorAll('input[type=checkbox]'), function(input) { input.checked = false; });
   sync();
  });
  filterSummaries.push({ key:key, label:label, all:all, summary:summary, reset:reset });
  var detail = E('details', { 'class':'sbg-location-filter-options' }, [summary,
   E('div', { 'class':'sbg-filter-dropdown' }, [reset, content])]);
  detail.addEventListener('toggle', function() {
   if (detail.open) filterPrimary.querySelectorAll('details').forEach(function(other) { if (other !== detail) other.open = false; });
  });
  detail.addEventListener('keydown', function(event) { if (event.key === 'Escape') { detail.open = false; summary.focus(); } });
  root.addEventListener('click', function(event) { if (!detail.contains(event.target)) detail.open = false; });
  root.addEventListener('focusin', function(event) { if (!detail.contains(event.target)) detail.open = false; });
  return E('fieldset', { 'class':'sbg-location-filter-section' }, [E('legend', {}, title), detail]);
 }
 var countrySearch = E('input', { type:'search', placeholder:_('Find a country'), 'aria-label':_('Find a country') });
 var countryList = E('div', { 'class':'sbg-country-list' });
 groupedCountries(countries).forEach(function(group) {
  var input = E('input', { type:'checkbox', 'aria-label':regionTitles[group.key] });
  var content = checkList(group.countries.map(function(code) { return [code, countryLabel(code)]; }), 'countries');
  regionFilters.push({ countries:group.countries, input:input, content:content });
  input.addEventListener('change', function() {
   state.countries = state.countries.filter(function(code) { return group.countries.indexOf(code) < 0; });
   if (input.checked) state.countries = state.countries.concat(group.countries);
   sync();
  });
  countryList.appendChild(E('section', {}, [E('label', { 'class':'sbg-country-region' }, [input, E('strong', {}, regionTitles[group.key])]), content]));
 });
 countrySearch.addEventListener('input', function() {
  var query = name(countrySearch.value);
  countryList.querySelectorAll('section').forEach(function(group) {
   var shown = 0, regionMatch = name(group.querySelector('.sbg-country-region').textContent).indexOf(query) >= 0;
   group.querySelectorAll('.sbg-location-checks label').forEach(function(label) {
    label.hidden = query && !regionMatch && name(label.textContent).indexOf(query) < 0;
    if (!label.hidden) shown++;
   });
   group.hidden = !shown;
  });
 });
 var countryFilters = E('div', { 'class':'sbg-country-picker' }, [countrySearch, countryList]);
 filterPrimary.appendChild(filterSection(_('Countries'), _('All countries'), 'countries', countryLabel, countryFilters));
 var protocolChoices = Array.from(new Set(nodes.map(function(node) { return node.protocol; }).concat(state.protocols))).filter(Boolean).sort().map(function(protocol) { return [protocol, protocol === 'hysteria2' ? 'Hysteria 2' : protocol.toUpperCase()]; });
 filterPrimary.appendChild(filterSection(_('Protocols'), _('All protocols'), 'protocols', function(value) {
  var choice = protocolChoices.find(function(item) { return item[0] === value; }); return choice ? choice[1] : value;
 }, checkList(protocolChoices, 'protocols')));
 [['include', _('Name or emoji contains'), _('All comma-separated fragments must occur in the node name.')], ['exclude', _('Exclude whole words'), _('Comma-separated words. For example, test does not exclude fastest.')]].forEach(function(item) {
  var input = E('input', { type: 'text', maxlength: 128, value: state[item[0]], 'aria-label': item[1] });
  input.addEventListener('input', function() { state[item[0]] = input.value; sync(); });
  filterBox.appendChild(control(item[1], input, item[2]));
 });
 filterBox.appendChild(E('p', {}, _('Filter membership is recalculated when subscriptions update. Matching nodes keep their subscription order.')));
 root.appendChild(filterBox);
 root.appendChild(preview);
 search.addEventListener('input', function() { query = name(search.value); renderBrowser(); });
 renderChosen(); renderBrowser(); renderPreview(); renderFilterSummaries();
 return root;
}
var Option = form.Value.extend({
 setMode: function(section, mode) {
  var input = this.map.findElement('id', this.cbid(section));
  var row = input && input.closest('.cbi-value');
  var root = row && row.querySelector('.sbg-location-picker');
  if (root) root.dispatchEvent(new CustomEvent('sbg-selection-mode', { detail:mode }));
 },
 cfgvalue: function(section) {
  var order = list(uci.get('resproxy', section, 'selection_order'));
  return JSON.stringify({ order: order.filter(function(value) { return value !== 'all:*'; }),
   kind: order.indexOf('all:*') >= 0 ? 'filter' : 'manual', view: uci.get('resproxy', section, 'selection_view') || 'countries',
   source: uci.get('resproxy', section, 'selection_source') || '*', countries: list(uci.get('resproxy', section, 'selection_countries')),
   protocols: list(uci.get('resproxy', section, 'selection_protocols')), include: uci.get('resproxy', section, 'selection_include') || '', exclude: uci.get('resproxy', section, 'selection_exclude') || '' });
 },
 renderWidget: function(section, index, value) {
  var state = JSON.parse(value);
  this.messages = this.messages || {};
  this.messages[section] = E('p', { role: 'alert', hidden: '' });
  var hidden = form.Value.prototype.renderWidget.call(this, section, index, value);
  var content = picker(this, section, hidden, state, this.inventory);
  if (this.map.readonly) Array.prototype.forEach.call(content.querySelectorAll('input,select,button'), function(input) { input.disabled = true; });
  return E('div', {}, [E('div', { style: 'display:none' }, hidden), content, this.messages[section]]);
 },
 validate: function(section, value) {
  var state = JSON.parse(value);
  var error = state.kind === 'manual' && !state.order.length ? _('Select at least one country, city or node, or configure a filter.') :
   state.kind === 'filter' && parts(state.exclude).some(function(word) { return /[^\p{L}\p{Nd}]/u.test(word); }) ? _('Exclusions must be whole words separated by commas.') : '';
  if (this.messages && this.messages[section]) { this.messages[section].textContent = error; this.messages[section].hidden = !error; }
  return error || true;
 },
 write: function(section, value) {
  var state = JSON.parse(value), filtered = state.kind === 'filter';
  uci.set('resproxy', section, 'selection_order', filtered ? ['all:*'] : state.order);
  uci.set('resproxy', section, 'selection_view', state.view);
  var fields = { selection_source: filtered ? state.source : '', selection_countries: filtered ? state.countries : [],
   selection_protocols: filtered ? state.protocols : [], selection_include: filtered ? state.include.trim() : '', selection_exclude: filtered ? state.exclude.trim() : '' };
  Object.keys(fields).forEach(function(key) { if (fields[key].length) uci.set('resproxy', section, key, fields[key]); else uci.unset('resproxy', section, key); });
 }
});
return baseclass.extend({ Option: Option });
