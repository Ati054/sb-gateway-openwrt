'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view.resproxy.monitor as sharedMonitor';
'require view';
'require view.resproxy.drafts as Drafts';
'require form';
'require view.resproxy.location_picker as locationPicker';
'require uci';
'require fs';
'require ui';
'require rpc';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

function values(value) {
	if (!value)
		return [];
	return Array.isArray(value) ? value.slice() : String(value).trim().split(/\s+/);
}

// Read the saved UCI session directly, independently of another view's cached package.
var getSavedConfig = rpc.declare({ object:'uci', method:'get', params:['config'], expect:{values:{}} });
var rulesRequest = null;
function readSavedRules() {
 if (!rulesRequest) rulesRequest = getSavedConfig('resproxy').then(function(config) {
  return Object.keys(config).map(function(section) { return Object.assign({}, config[section], {'.name':section}); })
   .filter(function(rule) { return rule['.type']==='rule' && rule.id && rule.name; })
   .sort(function(a,b) { return (a['.index'] || 0)-(b['.index'] || 0); });
 }).finally(function() { rulesRequest = null; });
 return rulesRequest;
}
function ruleSignature(rules) {
 return JSON.stringify(rules.map(function(rule) { return [rule.id,rule.name,rule.action,rule.preset_key,rule.preset_variant,rule.preset_variant_name]; }));
}
function refreshRulePickers() {
 var pickers = Array.from(document.querySelectorAll('.sbg-rule-picker'));
 if (!pickers.length) return Promise.resolve();
 return readSavedRules().then(function(rules) {
  pickers.forEach(function(picker) { if (picker.isConnected) picker.dispatchEvent(new CustomEvent('sbg-rules-current', {detail:rules})); });
 }).catch(function() { /* Keep existing choices when a background refresh is unavailable. */ });
}
window.addEventListener('focus', refreshRulePickers);
window.addEventListener('pageshow', refreshRulePickers);
document.addEventListener('visibilitychange', function() { if (!document.hidden) refreshRulePickers(); });

function ruleLabel(rule) {
 return '%s · %s'.format(rule.name, rule.action === 'direct' ? _('Direct WAN') : _('Proxy through this route list'));
}

var RulePicker = form.Value.extend({
 parse: function(section) {
  // Native Save also includes a dropdown choice that has not used Add yet.
  var input=this.map.findElement('id',this.cbid(section));
  var field=input && input.closest('.cbi-value');
  var picker=field && field.querySelector('.sbg-rule-picker');
  if (picker && !this.map._sbgDraftStage) picker.dispatchEvent(new CustomEvent('sbg-rule-commit'));
  return this.super('parse',arguments);
 },
 cfgvalue: function(section) { return JSON.stringify(values(uci.get('resproxy', section, 'rule_refs'))); },
 renderWidget: function(section, index, value) {
  var option=this, selected=JSON.parse(value || '[]'), rules=this.rules || [];
  var hidden=form.Value.prototype.renderWidget.call(this, section, index, value);
  var rows=E('ol', { 'class':'sbg-rule-order' });
  var select=E('select', { 'aria-label':_('Choose a rule') });
  var add=E('button', { type:'button', 'class':'cbi-button cbi-button-add' }, _('Add'));
  var root=E('div', { 'class':'sbg-rule-picker' });
  var variants={};
  function updateVariants() {
   variants={};
   rules.forEach(function(rule){if(rule.preset_key){var key=rule.preset_variant || 'legacy';if(!variants[key])variants[key]={name:rule.preset_variant_name || _('Ready-made rules'),rules:[]};variants[key].rules.push(rule);}});
  }
  updateVariants();
  root.addEventListener('sbg-rules-current', function(event) {
   if (ruleSignature(rules)===ruleSignature(event.detail)) return;
   option.rules=rules=event.detail;updateVariants();render();
   addRow.hidden=!rules.length;createLink.hidden=!!rules.length;
  });
  root.addEventListener('focusin', refreshRulePickers);
  function title(id) { var rule=rules.find(function(rule) { return rule.id === id; }); return rule ? ruleLabel(rule) : _('Unavailable rule: %s').format(id); }
  function action(text, label, fn, disabled) { return E('button', { type:'button', 'class':'cbi-button', 'aria-label':label, disabled:disabled || option.map.readonly ? '' : null, click:fn }, text); }
  function sync() {
   var ui=option.getUIElement(section); if(ui) { ui.setValue(JSON.stringify(selected)); ui.triggerValidation(); }
   root.dispatchEvent(new CustomEvent('widget-change', { bubbles:true }));render();
  }
  function selectionUnits() {
   var units=[];
   selected.forEach(function(id) {
    var rule=rules.find(function(item) { return item.id===id; });
    var key=rule && rule.preset_key ? rule.preset_variant || 'legacy' : null;
    var last=units[units.length-1];
    // Only join adjacent preset references, preserving saved first-match priority.
    if(key && last && last.key===key)last.ids.push(id);
    else units.push({key:key,ids:[id]});
   });
   return units;
  }
  function moveUnit(units,index,direction) {
   var next=index+direction,unit=units[index];
   units[index]=units[next];units[next]=unit;
   selected=units.reduce(function(ids,item) { return ids.concat(item.ids); },[]);sync();
  }
  function render() {
   rows.replaceChildren();
   var units=selectionUnits();
   units.forEach(function(unit,index) {
    var label,content;
    if(unit.key) {
     var variant=variants[unit.key],actions=Array.from(new Set(unit.ids.map(function(id) { return rules.find(function(rule) { return rule.id===id; }).action; })));
     label=variant.name+' · '+_('%d rules').format(unit.ids.length);
     if(actions.length===1)label+=' · '+(actions[0]==='direct' ? _('Direct WAN') : _('Proxy through this route list'));
     content=E('details',{'class':'sbg-rule-preset'},[E('summary',{},label),E('ul',{},unit.ids.map(function(id) {
      var rule=rules.find(function(item) { return item.id===id; });
      return E('li',{},ruleLabel(rule));
     }))]);
    } else { label=title(unit.ids[0]);content=E('span',{},label); }
    var controls=[];
    if(units.length>1) {
     controls.push(action('↑', _('Move up: %s').format(label),function() { moveUnit(units,index,-1); },index===0));
     controls.push(action('↓', _('Move down: %s').format(label),function() { moveUnit(units,index,1); },index===units.length-1));
    }
    controls.push(action('×', _('Remove: %s').format(label),function() { selected=selected.filter(function(id) { return unit.ids.indexOf(id)<0; });sync(); }));
    rows.appendChild(E('li',{'class':'sbg-rule-unit'},[content,E('span',{'class':'sbg-rule-actions'},controls)]));
   });
   var current=select.value;
   select.replaceChildren(E('option', { value:'' }, _('Choose a rule')));
   var sets=E('optgroup',{label:_('Rule variants')}),individual=E('optgroup',{label:_('Individual rules')});
   Object.keys(variants).forEach(function(key){var variant=variants[key];if(variant.rules.some(function(rule){return selected.indexOf(rule.id)<0;}))sets.appendChild(E('option',{value:'variant:'+key},variant.name+' · '+_('%d rules').format(variant.rules.length)));});
   if(sets.children.length)select.appendChild(sets);
   rules.forEach(function(rule) { if(!rule.preset_key && selected.indexOf(rule.id)<0)individual.appendChild(E('option', { value:rule.id }, ruleLabel(rule))); });
   if(individual.children.length)select.appendChild(individual);
   select.value=current; if(!select.value)select.value='';
   select.disabled=!!option.map.readonly || select.options.length===1;
   add.disabled=!!option.map.readonly || !select.value;
   rows.hidden=!selected.length;
  }
  select.addEventListener('change', function() { add.disabled=!!option.map.readonly || !select.value; });
  function addSelected() {
   if(!select.value || option.map.readonly)return;
   var variant=variants[select.value.slice(8)];
   var additions=select.value.indexOf('variant:')===0 ? (variant ? variant.rules.map(function(rule){return rule.id;}) : []) : [select.value];
   additions.forEach(function(id){if(selected.indexOf(id)<0)selected.push(id);});
   select.value='';sync();
  }
  root.addEventListener('sbg-rule-commit',addSelected);
  add.addEventListener('click', function() { addSelected();select.focus(); });
  root.appendChild(rows);
  var addRow=E('div', { 'class':'sbg-rule-add', hidden:rules.length ? null : '' }, [select,add]);
  var createLink=E('a', { href:L.url('admin/services/resproxy/rulesets'), hidden:rules.length ? '' : null }, _('Create rules'));
  root.appendChild(addRow);root.appendChild(createLink);
  render();return E('div', {}, [E('div', { style:'display:none' }, hidden),root]);
 },
 write: function(section,value) { var selected=JSON.parse(value || '[]');if(selected.length)uci.set('resproxy',section,'rule_refs',selected);else uci.unset('resproxy',section,'rule_refs'); }
});


function field(section, group, type, name, title) {
 var option = section.option(type, name, title);
 var render = option.render;
 option.render = function() {
  return Promise.resolve(render.apply(this, arguments)).then(function(node) {
   node.setAttribute('data-sbg-group', group);
   return node;
  });
 };
 return option;
}

function groupActions(section, map, openStates) {
	var rows = section.querySelectorAll(':scope > .cbi-section-node');
	var actions = section.querySelectorAll(':scope > .cbi-section-remove');
	rows.forEach(function(row, index) {
		[
			['selection', _('Servers and mode')], ['devices', _('Devices')],
			['direct', _('Routing rules')]
		].forEach(function(group) {
			var fields = Array.prototype.slice.call(row.querySelectorAll(':scope > [data-sbg-group="' + group[0] + '"]'));
			var content = E('div', { 'class': 'sbg-route-fields' }, fields);
			var children = [ E('h5', {}, group[1]), content ];
			row.appendChild(E('section', { 'class': 'sbg-route-group sbg-route-' + group[0], 'aria-label': group[1] }, children));
		});
		var sectionID = row.getAttribute('data-section-id');
		var name = uci.get('resproxy', sectionID, 'id');
		var metadata = name ? [
			uci.get('resproxy', sectionID, 'mode') === 'priority' ? _('Priority / reserve') : _('Best available'),
			values(uci.get('resproxy', sectionID, 'source_cidrs')).length ? _('Selected devices') : _('All devices'),
			_('%d rules').format(values(uci.get('resproxy', sectionID, 'rule_refs')).length)
		] : [];
		row.appendChild(E('div', { 'class':'sbg-route-save' }, E('button', {
			type:'button', 'class':'cbi-button cbi-button-save', disabled:map.readonly ? '' : null,
			click:ui.createHandlerFn(map, function() {
				return this.save().then(function(node) {
					var saved = Array.from(node.querySelectorAll('.sbg-route-card-settings')).find(function(item) {
						return item.getAttribute('data-route-section') === sectionID;
					});
					if (saved) { saved.querySelector('summary').focus(); saved.scrollIntoView({block:'nearest'}); }
					return node;
				});
			})
		}, _('Save'))));
		var details = E('details', {
			'class':'sbg-route-card-settings', 'data-route-section':sectionID,
			open:(openStates[sectionID] !== undefined ? openStates[sectionID] : !name) ? '' : null
		}, [ E('summary', {}, [E('strong', {}, name || _('New route list')),
			E('small', {}, metadata.map(function(text) { return E('span', {}, text); }))]), row ]);
		details.addEventListener('toggle', function() { openStates[sectionID] = details.open; });
		var card = E('div', { 'class':'sbg-route-card' }, details);
		var action = actions[index];
		if (action) {
			action.classList.remove('right');
			action.style.cssText = 'float:none;margin:0';
			section.insertBefore(card, action);
			card.appendChild(action);
		} else section.insertBefore(card, section.querySelector('.cbi-section-create'));
	});
	return section;
}

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('resproxy'),
			fs.exec_direct('/usr/libexec/resproxy-locations', [], 'json'),
			fs.exec('/usr/libexec/resproxy-rulesets').then(function(result) {
				return JSON.parse(result.stdout || '{}');
			}).catch(function() { return {}; }),
			readSavedRules()
		]).then(function(data) {
			return data;
		});
	},

	render: function(data) {
		var map = new form.Map('resproxy', _('Route lists for devices'));
		var openStates = {};
		var saveMap = map.save;
		map.save = function() {
			return saveMap.apply(this, arguments).then(function(node) {
				node.querySelectorAll('.sbg-route-card-settings').forEach(function(details) {
					var sectionID = details.getAttribute('data-route-section');
					if (uci.get('resproxy', sectionID, 'id')) {
						openStates[sectionID] = false;
						details.open = false;
					}
				});
				return node;
			}).catch(function(error) {
				if (map.root) map.root.querySelectorAll('.cbi-input-invalid').forEach(function(input) {
					var details = input.closest('.sbg-route-card-settings');
					if (details) { details.open = true; openStates[details.getAttribute('data-route-section')] = true; }
				});
				throw error;
			});
		};
		var policies = map.section(form.TypedSection, 'policy', _('Route lists'));
		policies.anonymous = true;
		policies.addremove = true;
		policies.sortable = true;
		policies.max_count = 8;
		var renderContents = policies.renderContents;
		policies.renderContents = function() {
			return groupActions(renderContents.apply(this, arguments), map, openStates);
		};

		policies.handleAdd = function() {
			// Parse existing forms before creating a blank section, then open only the new list.
			var added;
			return map.save(function() {
				added = uci.add('resproxy', 'policy');
				openStates[added] = true;
			}).then(function(node) {
				var details = Array.from(node.querySelectorAll('.sbg-route-card-settings')).find(function(item) {
					return item.getAttribute('data-route-section') === added;
				});
				if (details) { details.scrollIntoView({block:'start'}); var input = details.querySelector('input'); if (input) input.focus(); }
				return node;
			});
		};

		var id = field(policies, 'selection', form.Value, 'id', _('Route list ID'));
		id.rmempty = false;
		id.validate = function(section, value) {
			return /^[a-z][a-z0-9-]{0,31}$/.test(value) ? true : _('Use 1–32 lowercase Latin letters, digits, or hyphens; start with a letter.');
		};
		var mode = field(policies, 'selection', form.ListValue, 'mode', _('Selection mode'));
		mode.value('best', _('Best available'));
		mode.value('priority', _('Priority / reserve'));
		mode.default = 'best';
		mode.rmempty = false;
		var selection = field(policies, 'selection', locationPicker.Option, 'selection_order', '');
  var renderSelection = selection.render;
  selection.render = function() {
   return renderSelection.apply(this, arguments).then(function(node) {
    var title = node.querySelector('.cbi-value-title');
    if (title) title.remove();
    return node;
   });
  };
  selection.inventory = data[1];
  selection.rmempty = false;
  mode.onchange = function(event, section, value) { selection.setMode(section,value); };

		var deviceScope = field(policies, 'devices', form.ListValue, '_device_scope', _('Devices in this list'));
		deviceScope.value('all', _('All devices'));
		deviceScope.value('selected', _('Selected devices'));
		deviceScope.default = 'selected';
		deviceScope.cfgvalue = function(section) {
			return values(uci.get('resproxy', section, 'source_cidrs')).length || !uci.get('resproxy', section, 'id') ? 'selected' : 'all';
		};
		deviceScope.write = function() {};
		deviceScope.remove = function() {};
		deviceScope.validate = function(section, value) {
			if (value === 'all') {
				var duplicate = uci.sections('resproxy', 'policy').some(function(policy) {
					return policy['.name'] !== section && deviceScope.formvalue(policy['.name']) === 'all';
				});
				if (duplicate)
					return _('Only one list can cover all devices. Select devices in the other lists.');
			}
			return true;
		};
		deviceScope.description = _('Selected-device lists take precedence. If selected lists overlap, the first list wins.');
		var sources = field(policies, 'devices', form.DynamicList, 'source_cidrs', _('Client IP addresses or subnets'));
		sources.depends('_device_scope', 'selected');
		sources.datatype = 'cidr';
		sources.placeholder = '192.168.1.20/32';
		sources.rmempty = false;
        var refs = field(policies, 'direct', RulePicker, 'rule_refs', _('Rules — top to bottom'));
        refs.rmempty = true;
        refs.description = _('Select rules. The first match decides the route.');
        refs.rules = data[3];

		sharedMonitor.addTo(map);
		var drafts = Drafts.attach(map, 'routes');
		return map.render().then(function(node) {
			drafts.bind(node);
			node.addEventListener('toggle', function(event) {
				if (event.target.matches('.sbg-route-card-settings') && event.target.open) refreshRulePickers();
			}, true);
			return sbgTheme.decorate(node);
		});
	}
});
