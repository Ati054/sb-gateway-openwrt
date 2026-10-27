'use strict';
'require view.resproxy.appearance as sbgTheme';
'require view';
'require view.resproxy.drafts as Drafts';
'require form';
'require view.resproxy.interval as IntervalValue';
'require uci';
'require fs';
'require ui';
'require validation';
'require poll';
'require view.resproxy.safe_dom as safeDOM';

var E = safeDOM.create;

function values(value) {
	if (!value)
		return [];
	return Array.isArray(value) ? value.slice() : String(value).trim().split(/\s+/);
}

// Fixed choices stay visible as checkboxes, including on LuCI versions whose
// MultiValue widget always renders a dropdown.
var CheckboxList = form.MultiValue.extend({
 renderWidget: function(section, index, cfgvalue) {
  var selected = values(cfgvalue), field = this;
  return E('div', { id: this.cbid(section), style: 'display:flex;flex-wrap:wrap;gap:.5rem 1rem' }, this.keylist.map(function(key, i) {
   return E('label', { style: 'display:flex;align-items:center;gap:.35rem;cursor:pointer' }, [
    E('input', { type: 'checkbox', value: key, checked: selected.indexOf(key) >= 0 ? '' : null,
     disabled: field.map.readonly ? '' : null,
     change: function(event) { event.currentTarget.parentElement.parentElement.dispatchEvent(new CustomEvent('widget-change', { bubbles: true })); }
    }), E('span', {}, field.vallist[i])
   ]);
  }));
 },
 formvalue: function(section) {
  var node = this.map.findElement('id', this.cbid(section));
  return node ? Array.prototype.map.call(node.querySelectorAll('input:checked'), function(input) { return input.value; }) : null;
 }
});

var countries = { ww: _('Services'), ru: _('Russia (RU)'), cn: _('China (CN)'), ir: _('Iran (IR)') };

// UI sections combine the reviewed source categories without changing their IDs.
var categorySections = {
 ru: [
  { id: 'government', name: _('Government services'), packs: ['ru-government'] },
  { id: 'finance', name: _('Finance'), packs: ['ru-banks'] },
  { id: 'shopping', name: _('Shopping and travel'), packs: ['ru-marketplaces', 'ru-travel'] },
  { id: 'platforms', name: _('Communication and ecosystems'), packs: ['yandex', 'mailru-group', 'ru-telecom'] },
  { id: 'media', name: _('Media and entertainment'), packs: ['ru-media'] },
  { id: 'learning', name: _('Education, work and IT'), packs: ['ru-education-work-it'] },
  { id: 'all', name: _('All Russian services'), packs: ['ru-all'] }
 ],
 cn: [
  { id: 'finance', name: _('Finance'), packs: ['cn-banks', 'cn-securities'] },
  { id: 'learning', name: _('Education and science'), packs: ['cn-education', 'cn-mooc', 'cn-scholar', 'cn-wiki'] },
  { id: 'media', name: _('Media and communication'), packs: ['cn-media', 'cn-social', 'cn-entertainment', 'cn-blog'] },
  { id: 'games', name: _('Games'), packs: ['cn-games', 'cn-game-accelerator'] },
  { id: 'work', name: _('Work and IT'), packs: ['cn-ai', 'cn-dev', 'cn-documents', 'cn-collaborate', 'cn-outsource', 'cn-enterprise-query-platform'] },
  { id: 'network', name: _('Internet and networks'), packs: ['cn-cdn', 'cn-httpdns', 'cn-ntp', 'cn-netdisk', 'cn-network-security', 'cn-number-verification'] },
  { id: 'daily', name: _('Everyday services'), packs: ['cn-food', 'cn-logistics', 'cn-hospital'] },
  { id: 'devices', name: _('Electronics and cars'), packs: ['cn-electronic', 'cn-automobile'] },
  { id: 'all', name: _('All Chinese services'), packs: ['cn-all'] }
 ],
 ir: [
  { id: 'finance', name: _('Finance'), packs: ['ir-banks', 'ir-bourse', 'ir-payment', 'ir-insurance'] },
  { id: 'learning', name: _('Education and science'), packs: ['ir-education', 'ir-scholar'] },
  { id: 'media', name: _('Media and communication'), packs: ['ir-media', 'ir-news', 'ir-social-media', 'ir-forums'] },
  { id: 'government', name: _('Government services'), packs: ['ir-government'] },
  { id: 'shopping', name: _('Shopping and travel'), packs: ['ir-shopping', 'ir-travel'] },
  { id: 'tech', name: _('Technology'), packs: ['ir-tech'] },
  { id: 'ads', name: _('Advertising'), packs: ['ir-ads'] },
  { id: 'all', name: _('All Iranian services'), packs: ['ir-all'] }
 ]
};
function countrySections(country, catalog) {
 if (country === 'ww') return (catalog.choices || []).filter(function(item) { return item.country === 'ww'; }).map(function(item) { return {key:'group:ww-' + item.id,label:item.name,packs:[item.id]}; });
 return (categorySections[country] || []).map(function(group) {
  return { key: 'group:' + country + '-' + group.id, label: group.name,
   packs: group.packs.filter(function(id) { return (catalog.choices || []).some(function(item) { return item.id === id && item.country === country; }); }) };
 }).filter(function(group) { return group.packs.length > 0; });
}

function presetChoices(country, catalog) {
	var choices = [
		...(country === 'ww' ? [] : [{ key: 'geoip:' + country, label: _('GeoIP — IP networks'), mode: 'geoip' }]),

	];
	(catalog.choices || []).forEach(function(pack) {
		if (pack.country === country)
			choices.push({ key: 'pack:' + pack.id, label: pack.name, pack: pack.id, description: pack.description,
				wideCountry: (pack.id === 'ru-all' || pack.id === 'cn-all' || pack.id === 'ir-all') ? country : null });
	});
	return choices.concat(countrySections(country, catalog));
}

function presetName(key, catalog) {
	if (key === 'traffic:bittorrent') return _('BitTorrent');
	if (key.indexOf('group:') === 0) {
		var country = key.slice(6, 8), group = countrySections(country, catalog).filter(function(item) { return item.key === key; })[0];
		return group ? '%s · %s'.format(countries[country], group.label) : key;
	}
	if (key.indexOf('pack:') === 0) {
		var pack = (catalog.choices || []).filter(function(item) { return item.id === key.slice(5); })[0];
		return pack ? '%s · %s'.format(countries[pack.country] || pack.country, pack.name) : key;
	}
	var country = key.split(':')[1];
	var choice = presetChoices(country === 'ru' || country === 'cn' || country === 'ir' ? country : 'ru', catalog)
		.filter(function(item) { return item.key === key; })[0];
	return choice ? '%s · %s'.format(countries[country] || countries.ru, choice.label) : key;
}

function presetID(key, action) {
	var suffix = action === 'direct' ? '-wan' : '-proxy';
	var base = ('preset-' + key.replace(/[^a-z0-9]+/g, '-')).slice(0, 32 - suffix.length);
	var id = base + suffix, index = 2;
	var used = {};
	uci.sections('resproxy', 'rule').forEach(function(rule) { used[rule.id] = true; });
	while (used[id]) {
		var numberedSuffix = suffix + '-' + index++;
		id = base.slice(0, 32 - numberedSuffix.length) + numberedSuffix;
	}
	return id;
}

function groupHeaders(section, map, openStates) {
	var rows = section.querySelectorAll(':scope > .cbi-section-node');
	var actions = section.querySelectorAll(':scope > .cbi-section-remove');
	rows.forEach(function(row, index) {
		var sectionID = row.getAttribute('data-section-id');
		var rule = uci.get('resproxy', sectionID) || {};
		var linked = uci.sections('resproxy', 'policy').filter(function(policy) { return values(policy.rule_refs).indexOf(rule.id) >= 0; });
		var metadata = rule.name ? [rule.action === 'proxy' ? _('Proxy through this route list') : _('Direct WAN'),
			linked.length ? linked.map(function(policy) { return policy.id; }).join(', ') : _('Not used')] : [];
		row.appendChild(E('div', { 'class':'sbg-custom-rule-save' }, E('button', {
			type:'button', 'class':'cbi-button cbi-button-save', disabled:map.readonly ? '' : null,
			click:ui.createHandlerFn(map, function() {
				return this.save().then(function(node) {
					var saved = Array.from(node.querySelectorAll('.sbg-custom-rule-settings')).find(function(item) {
						return item.getAttribute('data-rule-section') === uci.resolveSID('resproxy', sectionID);
					});
					if (saved) { saved.querySelector('summary').focus(); saved.scrollIntoView({block:'nearest'}); }
					return node;
				});
			})
		}, _('Save'))));
		var details = E('details', {
			'class':'sbg-custom-rule-settings', 'data-rule-section':sectionID,
			open:(openStates[sectionID] !== undefined ? openStates[sectionID] : !rule.name) ? '' : null
		}, [ E('summary', {}, [E('strong', {}, rule.name || _('New rule group')),
			E('small', {}, metadata.map(function(text) { return E('span', {}, text); }))]), row ]);
		details.addEventListener('toggle', function() { openStates[sectionID] = details.open; });
		var card = E('div', { 'class':'sbg-custom-rule-card' }, details);
		var action = actions[index];
		if (action) {
			action.classList.remove('right'); action.style.cssText = 'float:none;margin:0';
			section.insertBefore(card, action); card.appendChild(action);
		} else section.insertBefore(card, section.querySelector('.cbi-section-create'));
	});
	return section;
}


return view.extend({
	load: function() {
		return Promise.all([
			uci.load('resproxy'),
            fs.exec('/usr/libexec/resproxy-rulesets').then(function(result) {
                if (result.code !== 0) throw new Error(result.stderr || _('Catalog read failed.'));
                return JSON.parse(result.stdout);
            }).catch(function() { return {}; })
		]).then(function(data) {
			return data;
		});
	},

	render: function(data) {
		var catalog = data[1] || {};
		var map = new form.Map('resproxy', _('Rule databases'));
		var customOpenStates = {};
		var saveMap = map.save;
		map.save = function() {
			return saveMap.apply(this, arguments).then(function(node) {
				node.querySelectorAll('.sbg-custom-rule-settings').forEach(function(details) {
					var sectionID = details.getAttribute('data-rule-section');
					if (uci.get('resproxy', sectionID, 'name')) {
						customOpenStates[sectionID] = false; details.open = false;
					}
				});
				return node;
			}).catch(function(error) {
				if (map.root) map.root.querySelectorAll('.cbi-input-invalid').forEach(function(input) {
					var details = input.closest('.sbg-custom-rule-settings');
					if (details) { details.open = true; customOpenStates[details.getAttribute('data-rule-section')] = true; }
				});
				throw error;
			});
		};
		// Only rule editors are tabs; database updates remain visible for both.
		var renderChildren = map.renderChildren;
		map.renderChildren = function() {
			return renderChildren.apply(this, arguments).then(function(nodes) {
				nodes[0].setAttribute('data-tab', 'main');
				nodes[0].setAttribute('data-tab-title', _('Ready-made rules'));
				nodes[1].setAttribute('data-tab', 'rule');
				nodes[1].setAttribute('data-tab-title', _('Custom rules'));
				return [E('div', { 'class': 'cbi-map-tabbed' }, nodes.slice(0, 2))].concat(nodes.slice(2));
			});
		};
		var settings = map.section(form.NamedSection, 'main', 'main', _('Ready-made rules'));
		settings.addremove = false;
        var creatorOpen = !uci.sections('resproxy', 'rule').some(function(rule) { return !!rule.preset_key; });
        var creating = false, editingVariant = null, settingsNode = null, drafts = (Drafts.read('preset-editor') || {}).drafts || Object.create(null);
        var draftName = '', draftCountry = 'ru', draftAction = 'direct', draftOutage = 'block', retainedPresets = [];
        function persistDrafts() {
            Drafts.write('preset-editor', { drafts:drafts, active:editingVariant && editingVariant.key || '_new', open:creatorOpen });
        }
        function resetDraft() {
            editingVariant = null; draftName = ''; draftCountry = 'ru'; draftAction = 'direct'; draftOutage = 'block'; retainedPresets = [];
            Object.keys(presetSelections).forEach(function(code) { presetSelections[code].geoip = false; presetSelections[code].packs = {}; });
            torrentSelected = false; creatorOpen = false;
            presetOutage.keylist = ['block', 'wan'];
            presetOutage.vallist = [_('Block'), _('Through WAN')];
            presetAction.keylist = ['direct', 'proxy'];
            presetAction.vallist = [_('Direct WAN'), _('Proxy through this route list')];
            addPreset.inputtitle = _('Create selected rules');
            persistDrafts();
        }
        function rememberDraft() {
            if (!settingsNode) return;
            drafts[editingVariant ? editingVariant.key : '_new'] = {
                name:variantName.formvalue('main') || '', country:presetCountry.formvalue('main') || draftCountry,
                action:presetAction.formvalue('main') || draftAction,
                outage:presetOutage.formvalue('main') || draftOutage,
                selections:JSON.parse(JSON.stringify(presetSelections)), torrent:torrentSelected,
                retained:retainedPresets.map(function(item) { return {rule:item.rule, selected:item.selected}; })
            };
            persistDrafts();
        }
        function restoreDraft(key) {
            var draft = drafts[key];
            if (!draft) return false;
            draftName = draft.name; draftCountry = draft.country; draftAction = draft.action; draftOutage = draft.outage || 'block';
            Object.keys(presetSelections).forEach(function(code) {
                presetSelections[code] = JSON.parse(JSON.stringify(draft.selections[code]));
            });
            torrentSelected = draft.torrent; retainedPresets = draft.retained.map(function(item) { return {rule:item.rule, selected:item.selected}; });
            if (draftAction === 'keep') presetAction.value('keep', _('Keep existing actions'));
            if (draftOutage === 'keep') presetOutage.value('keep', _('Keep existing behavior'));
            return true;
        }
        function refreshEditor(focus, summaryKey) {
            var previous = settingsNode;
            return settings.render().then(function(node) {
                if (previous && previous.parentNode) {
                    Array.prototype.forEach.call(previous.attributes, function(attribute) { if (attribute.name !== 'class') node.setAttribute(attribute.name, attribute.value); });
                    previous.replaceWith(node);
                }
                map.checkDepends();
                if (summaryKey != null) {
                    var card = Array.prototype.find.call(node.querySelectorAll('.sbg-ready-card'), function(item) { return item.dataset.variant === summaryKey; });
                    if (card) card.querySelector('summary').focus({preventScroll:true});
                }
                if (focus) { var input = node.querySelector('input[id$="._preset_variant_name"]'); if (input) { input.focus(); input.scrollIntoView({block:'center'}); } }
            });
        }
        function allPresetChoices() {
            return Object.keys(countries).reduce(function(choices, country) {
                return choices.concat(presetChoices(country, catalog).map(function(choice) { return Object.assign({ country:country }, choice); }));
            }, [{ key:'traffic:bittorrent', protocols:['bittorrent'] }]);
        }
        function loadVariant(key, variant) {
            editingVariant = { key:key, rules:variant.rules };
            draftName = variant.name;
            var actions = Array.from(new Set(variant.rules.map(function(rule) { return rule.action; })));
            draftAction = actions.length === 1 ? actions[0] : 'keep';
            var outages = Array.from(new Set(variant.rules.map(function(rule) { return rule.proxy_outage_action || 'block'; })));
            draftOutage = outages.length > 1 ? 'keep' : outages[0] || 'block';
            if (draftOutage === 'keep') presetOutage.value('keep', _('Keep existing behavior'));
            if (draftAction === 'keep') presetAction.value('keep', _('Keep existing actions'));
            var choices = allPresetChoices(), firstCountry = null;
            variant.rules.forEach(function(rule) {
                var choice = choices.find(function(item) { return item.key === rule.preset_key; });
                if (!choice) { retainedPresets.push({ rule:rule, selected:true }); return; }
                if (choice.protocols) { torrentSelected = true; return; }
                var selection = presetSelections[choice.country];
                firstCountry = firstCountry || choice.country;
                if (choice.mode) selection.geoip = true;
                else (choice.packs || [choice.pack]).forEach(function(id) { selection.packs['pack:' + id] = true; });
            });
            draftCountry = firstCountry || 'ru'; restoreDraft(key); creatorOpen = true; addPreset.inputtitle = _('Save changes');
        }
        function editVariant(key, variant) {
            if (creating || map.readonly) return;
            rememberDraft(); resetDraft(); loadVariant(key, variant); persistDrafts();
            return refreshEditor(false, key);
        }
        function updateRouteReferences(oldRules, newIDs, addedIDs) {
            var oldIDs = oldRules.map(function(rule) { return rule.id; });
            uci.sections('resproxy', 'policy').forEach(function(route) {
                var refs = values(route.rule_refs), last = -1;
                refs.forEach(function(id, index) { if (oldIDs.indexOf(id) >= 0) last = index; });
                if (last < 0) return;
                var next = [];
                refs.forEach(function(id, index) {
                    if (oldIDs.indexOf(id) < 0 || newIDs.indexOf(id) >= 0) next.push(id);
                    if (index === last) addedIDs.forEach(function(added) { if (refs.indexOf(added) < 0) next.push(added); });
                });
                if (next.length) uci.set('resproxy', route['.name'], 'rule_refs', next); else uci.unset('resproxy', route['.name'], 'rule_refs');
            });
        }
        var renderSettings = settings.renderContents;
        settings.renderContents = function() {
            var node = renderSettings.apply(this, arguments), row = node.querySelector('.cbi-section-node');
            var fields = Array.prototype.filter.call(row.children, function(field) { return !field.classList.contains('sbg-ready-variants'); });
            var content = E('div', { 'class':'sbg-preset-fields' }, fields);
            var activeCard = editingVariant && Array.prototype.find.call(row.querySelectorAll('.sbg-ready-card'), function(card) { return card.dataset.variant === editingVariant.key; });
            if (activeCard) {
                var activeDetails = activeCard.querySelector('.sbg-ready-variant');
                activeDetails.open = creatorOpen;
                activeDetails.querySelector('.sbg-ready-variant-rules').remove();
                activeDetails.appendChild(content);
            }
            var editor = E('details', { 'class':'sbg-preset-editor', open:!editingVariant && creatorOpen ? '' : null }, [E('summary', {}, _('Add a variant'))]);
            if (!activeCard) editor.appendChild(content);
            editor.addEventListener('toggle', function() {
                if (!editor.isConnected || creating) return;
                if (editor.open && editingVariant) {
                    rememberDraft(); resetDraft(); restoreDraft('_new'); creatorOpen = true;
                    refreshEditor(true).catch(function(error) { ui.addNotification(null, E('p', {}, error.message)); });
                } else if (!editingVariant) creatorOpen = editor.open;
            });
            row.appendChild(editor);
            settingsNode = node;
            return node;
        };
        var variantName = settings.option(form.Value, '_preset_variant_name', _('Variant name'));
        variantName.placeholder = _('Enter a name');
        variantName.cfgvalue = function() { return draftName; };
        variantName.write = variantName.remove = function() {};
        variantName.validate = function(section, value) { value = String(value || ''); return value.length <= 40 && !/[\x00-\x1f\x7f]/.test(value) || _('Enter a name of up to 40 characters.'); };
		var presetCountry = settings.option(form.ListValue, '_preset_country', _('Preset section'));
		Object.keys(countries).forEach(function(code) { presetCountry.value(code, countries[code]); });
		presetCountry.value('other', _('Other country — custom rules'));
		presetCountry.cfgvalue = function() { return draftCountry; };
		presetCountry.description = _('Choose a country or services.');
		presetCountry.write = presetCountry.remove = function() {};
        var presetSelections = {};
        Object.keys(countries).forEach(function(code) { presetSelections[code] = { geoip:false, packs:{} }; });

		function presetOption(code, choice, selection, onChange) {
			var isGeo = !!choice.mode;
			var input = {
				'type': 'checkbox',
				'name': isGeo ? 'resproxy-preset-geo-' + code : 'resproxy-preset-pack-' + code,
				'value': choice.key,
                'checked': (isGeo ? !!selection[choice.mode] : !!selection.packs[choice.key]) ? '' : null,
                'disabled': map.readonly ? '' : null,
				'change': function(event) {
					if (isGeo)
						selection[choice.mode] = event.target.checked;
					else
						selection.packs[choice.key] = event.target.checked;
					if (onChange) onChange();
				}
			};
			return E('label', {
				'style': 'display:flex;align-items:flex-start;gap:.5rem;padding:.4rem 0;cursor:pointer'
			}, [E('input', input), E('span', {}, choice.label)]);
		}
		Object.keys(countries).forEach(function(code) {
			var field = settings.option(form.DummyValue, '_preset_' + code, _('Available presets'));
			field.depends('_preset_country', code);
			field.renderWidget = function() {
				var selection = presetSelections[code];
				var choices = presetChoices(code, catalog);
				var geo = choices.filter(function(item) {
					return item.mode === 'geoip';
				});
				var sections = geo.length ? [E('fieldset', { 'style': 'border:0;padding:0;margin:0 0 1rem' }, [
					E('legend', { 'style': 'font-weight:600;margin-bottom:.35rem' }, _('Country data')),
					E('div', { 'style': 'display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,17rem),1fr));gap:0 .75rem' },
						geo.map(function(choice) { return presetOption(code, choice, selection); }))
				])] : [];
				var packs = choices.filter(function(item) { return !!item.pack; });
                if (packs.length) {
                    var groups = countrySections(code, catalog);
                    var content;
                    if (groups.length) {
                        content = groups.map(function(group) {
                            if (code === 'ww' && group.packs.length === 1) {
                                var single = packs.find(function(item) { return item.pack === group.packs[0]; });
                                return E('div', { 'class':'sbg-rule-category sbg-service-card', style:'padding:.25rem .6rem;font-weight:600', title:single.description || null }, presetOption(code,single,selection));
                            }

                            var count = E('span', { 'class': 'sbg-category-count' });
                            var bulk = E('input', { type: 'checkbox', disabled:map.readonly ? '' : null, 'aria-label': _('Select all: %s').format(group.label) });
                            function sync() {
                                var selected = group.packs.filter(function(id) { return !!selection.packs['pack:' + id]; }).length;
                                bulk.checked = selected === group.packs.length;
                                bulk.indeterminate = selected > 0 && selected < group.packs.length;
                                count.textContent = selected ? _('%d of %d selected').format(selected, group.packs.length) : _('%d lists').format(group.packs.length);
                            }
                            var body = E('div', { 'class': 'sbg-category-lists' }, group.packs.map(function(id) {
                                var choice = packs.filter(function(item) { return item.pack === id; })[0];
                                return presetOption(code, choice, selection, sync);
                            }));
                            bulk.addEventListener('click', function(event) { event.stopPropagation(); });
                            bulk.addEventListener('change', function() {
                                group.packs.forEach(function(id) { selection.packs['pack:' + id] = bulk.checked; });
                                Array.prototype.forEach.call(body.querySelectorAll('input'), function(input) { input.checked = bulk.checked; });
                                sync();
                            });
                            sync();
                            return E('details', { 'class': 'sbg-rule-category' }, [
                                E('summary', {}, [bulk, E('strong', {}, group.label), count]), body
                            ]);
                        });
                    } else {
                        content = packs.map(function(choice) { return presetOption(code, choice, selection); });
                    }
                    sections.push(E('fieldset', { style: 'border:0;padding:0;margin:0' }, [
                        E('legend', { style: 'font-weight:600;margin-bottom:.35rem' }, _('Thematic lists')),
                        E('div', { 'class': 'sbg-category-grid' }, content)
                    ]));
                }
				return E('div', {}, sections);
			};
		});
		var otherCountry = settings.option(form.DummyValue, '_preset_other', _('Custom rules'));
		otherCountry.depends('_preset_country', 'other');
		otherCountry.renderWidget = function() {
			return E('div', {}, [
				E('p', {}, _('For another country, create a named domain and IP rule under “Custom rules”, then select it in a route list.')),
				E('button', {
					'class': 'cbi-button cbi-button-action', 'type': 'button',
					'click': function() {
						var target = Array.prototype.filter.call(document.querySelectorAll('.sbg-page .cbi-tabmenu a'),
							function(item) { return item.textContent.trim() === _('Custom rules'); })[0];
						if (target)
							target.click();
					}
				}, _('Open custom rule groups'))
			]);
		};
		var torrentSelected = false;
		var traffic = settings.option(form.DummyValue, '_preset_traffic', _('Traffic rules'));
		traffic.renderWidget = function() {
			return E('label', { style: 'display:flex;align-items:center;gap:.5rem;cursor:pointer' }, [
				E('input', { type: 'checkbox', checked: torrentSelected ? '' : null, change: function(event) { torrentSelected = event.target.checked; } }),
				E('span', {}, _('BitTorrent'))
			]);
		};
		traffic.description = _('Matches traffic detected by Xray as BitTorrent.');
		var presetAction = settings.option(form.ListValue, '_preset_action', _('Action'));
		presetAction.value('direct', _('Direct WAN'));
		presetAction.value('proxy', _('Proxy through this route list'));
		presetAction.cfgvalue = function() { return draftAction; };
		presetAction.write = presetAction.remove = function() {};
        var presetOutage = settings.option(form.ListValue, '_preset_outage_action', _('When all proxies fail'));
        presetOutage.value('block', _('Block'));
        presetOutage.value('wan', _('Through WAN'));
        presetOutage.cfgvalue = function() { return draftOutage; };
        presetOutage.write = presetOutage.remove = function() {};
        var retained = settings.option(form.DummyValue, '_preset_retained', _('Other presets'));
        var renderRetained = retained.render;
        retained.render = function() { return Promise.resolve(renderRetained.apply(this, arguments)).then(function(node) { if (!retainedPresets.length) node.style.display = 'none'; return node; }); };
        retained.renderWidget = function() {
            return E('div', {}, retainedPresets.map(function(item) {
                return E('label', {'class':'sbg-retained-preset'}, [E('input', {type:'checkbox', checked:item.selected ? '' : null, disabled:map.readonly ? '' : null,
                    change:function(event) { item.selected = event.target.checked; }}), E('span', {}, item.rule.name || item.rule.preset_key)]);
            }));
        };
        var addPreset = settings.option(form.Button, '_preset_add', _('Selected presets'));
        addPreset.inputtitle = _('Create selected rules');
        addPreset.inputstyle = 'action';
        var renderAddPreset = addPreset.renderWidget;
        addPreset.renderWidget = function() {
            var button = renderAddPreset.apply(this, arguments);
            return E('div', {'class':'sbg-preset-form-actions'}, [button, E('button', {type:'button', 'class':'cbi-button', disabled:map.readonly ? '' : null,
                click:function() { if (creating) return; delete drafts[editingVariant ? editingVariant.key : '_new']; resetDraft(); return refreshEditor(); }}, _('Cancel'))]);
        };
        addPreset.onclick = function() {
            if (creating || map.readonly) return;
            var keys = [];
            Object.keys(presetSelections).forEach(function(country) {
                var selection = presetSelections[country];
                if (selection.geoip) keys.push('geoip:' + country);
                keys = keys.concat(Object.keys(selection.packs).filter(function(key) { return selection.packs[key]; }));
                countrySections(country, catalog).forEach(function(group) {
                    var oldGroup = editingVariant && editingVariant.rules.some(function(rule) { return rule.preset_key === group.key; });
                    if (group.packs.length > 1 && (!editingVariant || oldGroup) && group.packs.every(function(id) { return keys.indexOf('pack:' + id) >= 0; })) {
                        keys = keys.filter(function(key) { return key.indexOf('pack:') !== 0 || group.packs.indexOf(key.slice(5)) < 0; });
                        keys.push(group.key);
                    }
                });
            });
            if (torrentSelected) keys.push('traffic:bittorrent');
            var outage = presetOutage.formvalue('main') || draftOutage;
            var action = presetAction.formvalue('main'), choices = allPresetChoices();
            var additions = keys.map(function(key) { return choices.find(function(item) { return item.key === key; }); }).filter(Boolean);
            var kept = retainedPresets.filter(function(item) { return item.selected; }).map(function(item) { return item.rule; });
            if ((!additions.length && !kept.length) || (action !== 'direct' && action !== 'proxy' && !(editingVariant && action === 'keep'))) {
                ui.addNotification(null, E('p', {}, _('Select at least one preset.'))); return;
            }
            var oldRules = editingVariant ? editingVariant.rules : [];
            var resultingCount = kept.length + additions.reduce(function(count, choice) { return count + Math.max(1, oldRules.filter(function(rule) { return rule.preset_key === choice.key; }).length); }, 0);
            if (uci.sections('resproxy', 'rule').length - oldRules.length + resultingCount > 128) {
                ui.addNotification(null, E('p', {}, _('The limit of 128 rules has been reached.'))); return;
            }
            var requestedName = String(variantName.formvalue('main') || '').trim(), usedNames = Object.create(null);
            uci.sections('resproxy', 'rule').forEach(function(rule) { if (!oldRules.some(function(existing) { return existing['.name'] === rule['.name']; })) usedNames[String(rule.preset_variant_name || '').toLocaleLowerCase()] = true; });
            var title = requestedName, n = 1;
            if (!title) { do { title = _('Variant %d').format(n++); } while (usedNames[title.toLocaleLowerCase()]); }
            else { var base = title; n = 2; while (usedNames[title.toLocaleLowerCase()]) { var suffix = ' (' + n++ + ')'; title = base.slice(0, 40 - suffix.length).replace(/[\uD800-\uDBFF]$/, '') + suffix; } }
            var variantID = editingVariant && editingVariant.key !== 'legacy' ? editingVariant.key : 'variant-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2,8);
            creating = true;
            return map.save(function() {
                var newIDs = [], addedIDs = [];
                function setName(sid, label) {
                    uci.set('resproxy', sid, 'name', (title + ' · ' + label).slice(0,120));
                    uci.set('resproxy', sid, 'preset_variant', variantID);
                    uci.set('resproxy', sid, 'preset_variant_name', title);
                }
                additions.forEach(function(selected) {
                    var matches = oldRules.filter(function(rule) { return rule.preset_key === selected.key; });
                    (matches.length ? matches : [null]).forEach(function(existing) {
                        var nextAction = action === 'keep' ? existing && existing.action || 'direct' : action;
                        var sid = existing ? existing['.name'] : uci.add('resproxy', 'rule');
                        var id = existing ? existing.id : presetID(selected.key, nextAction);
                        if (!existing) addedIDs.push(id);
                        newIDs.push(id);
                        uci.set('resproxy', sid, 'id', id);
                        uci.set('resproxy', sid, 'action', nextAction);
                        uci.set('resproxy', sid, 'proxy_outage_action', outage === 'keep' ? existing && existing.proxy_outage_action || 'block' : outage);
                        uci.unset('resproxy', sid, 'outage_action');
                        uci.set('resproxy', sid, 'preset_key', selected.key);
                        setName(sid, presetName(selected.key, catalog));
                        if (existing) return;
                        if (selected.wideCountry) { uci.set('resproxy', sid, 'country', selected.wideCountry); uci.set('resproxy', sid, 'geo_mode', 'geosite'); }
                        if (selected.protocols) uci.set('resproxy', sid, 'protocols', selected.protocols);
                        else if (selected.pack || selected.packs) uci.set('resproxy', sid, 'service_packs', selected.packs || [selected.pack]);
                        else { uci.set('resproxy', sid, 'country', selected.country); uci.set('resproxy', sid, 'geo_mode', selected.mode); }
                    });
                });
                kept.forEach(function(rule) {
                    newIDs.push(rule.id);
                    var prefix = (rule.preset_variant_name || '') + ' · ', label = rule.name || rule.preset_key;
                    if (rule.preset_variant_name && label.indexOf(prefix) === 0) label = label.slice(prefix.length);
                    setName(rule['.name'], label);
                    if (action !== 'keep') uci.set('resproxy', rule['.name'], 'action', action);
                    if (outage !== 'keep') uci.set('resproxy', rule['.name'], 'proxy_outage_action', outage);
                    uci.unset('resproxy', rule['.name'], 'outage_action');
                });
                updateRouteReferences(oldRules, newIDs, addedIDs);
                oldRules.forEach(function(rule) { if (newIDs.indexOf(rule.id) < 0) uci.remove('resproxy', rule['.name']); });
            }).then(function(node) {
                delete drafts[editingVariant ? editingVariant.key : '_new']; resetDraft();
                return refreshEditor().then(function() { return node; });
            }).catch(function(error) {
                // Keep staged IDs on retry, even if rpcd acknowledged only part
                // of a failed save. Never create a duplicate variant on retry.
                rememberDraft();
                var priorKey = editingVariant ? editingVariant.key : '_new', priorDraft = drafts[priorKey];
                var stagedRules = uci.sections('resproxy', 'rule').filter(function(rule) { return rule.preset_variant === variantID; });
                if (stagedRules.length && priorDraft) { editingVariant = {key:variantID,rules:stagedRules}; drafts[variantID] = priorDraft; restoreDraft(variantID); }
                creatorOpen = true; persistDrafts();
                throw error;
            }).finally(function() { creating = false; });
        };

        var assigned = settings.option(form.DummyValue, '_preset_assigned', '');
        var renderAssigned = assigned.render;
        assigned.render = function() { return Promise.resolve(renderAssigned.apply(this, arguments)).then(function(node) {
            node.classList.add('sbg-ready-variants');
            var label = node.querySelector('.cbi-value-title'); if (label) label.remove();
            return node;
        }); };
        assigned.renderWidget = function() {
            var variants = {};
            uci.sections('resproxy', 'rule').forEach(function(rule) {
                if (!rule.preset_key || !rule.id) return;
                var key = rule.preset_variant || 'legacy';
                if (!variants[key]) variants[key] = { name:rule.preset_variant_name || _('Ready-made rules'), rules:[] };
                variants[key].rules.push(rule);
            });
            var cards = Object.keys(variants).map(function(key) {
                var variant = variants[key], actions = Array.from(new Set(variant.rules.map(function(rule) { return rule.action; })));
                var ids = variant.rules.map(function(rule) { return rule.id; });
                var linked = uci.sections('resproxy', 'policy').filter(function(policy) { return values(policy.rule_refs).some(function(id) { return ids.indexOf(id) >= 0; }); });
                var metadata = [_('%d rules').format(variant.rules.length)];
                if (actions.length === 1) metadata.push(actions[0] === 'direct' ? _('Direct WAN') : _('Proxy through this route list'));
                metadata.push(linked.length ? linked.map(function(policy) { return policy.id; }).join(', ') : _('Not used'));
                var details = E('details', {'class':'sbg-ready-variant'}, [E('summary', {}, [E('strong', {}, variant.name), E('small', {}, metadata.join(' · '))]),
                        E('ul', {'class':'sbg-ready-variant-rules'}, variant.rules.map(function(rule) {
                            var prefix = (rule.preset_variant_name || '') + ' · ', label = rule.name || presetName(rule.preset_key, catalog);
                            if (rule.preset_variant_name && label.indexOf(prefix) === 0) label = label.slice(prefix.length);
                            if (actions.length > 1) label += ' · ' + (rule.action === 'direct' ? _('Direct WAN') : _('Proxy through this route list'));
                            return E('li', {}, label);
                        }))]);
                details.addEventListener('toggle', function() {
                    if (!details.isConnected || creating) return;
                    if (editingVariant && editingVariant.key === key) { creatorOpen = details.open; return; }
                    if (details.open && !map.readonly)
                        editVariant(key, variant).catch(function(error) { ui.addNotification(null, E('p', {}, error.message)); });
                });
                return E('article', {'class':'sbg-ready-card', 'data-variant':key}, [details,
                    E('div', {'class':'sbg-ready-card-actions'}, [
                        E('button', {type:'button', 'class':'cbi-button cbi-button-negative', disabled:map.readonly ? '' : null,
                            click:function() {
                                if (creating || map.readonly) return;
                                rememberDraft(); delete drafts[key]; resetDraft(); creating = true;
                                return refreshEditor().then(function() { return map.save(function() {
                                    updateRouteReferences(variant.rules, [], []);
                                    variant.rules.forEach(function(rule) { uci.remove('resproxy', rule['.name']); });
                                }); }).finally(function() { creating = false; });
                            }}, _('Delete'))
                    ])
                ]);
            });
            return E('div', {}, cards);
        };
		var groups = map.section(form.TypedSection, 'rule', _('Custom rules'));
		groups.anonymous = true;
		groups.addremove = true;
		groups.max_count = 128;
		groups.hidetitle = true;
		groups.filter = function(section) { return !uci.get('resproxy', section, 'preset_key'); };
		var renderGroups = groups.renderContents;
		groups.renderContents = function() {
			return groupHeaders(renderGroups.apply(this, arguments), map, customOpenStates);
		};
		groups.handleRemove = function(section) {
			var id = uci.get('resproxy', section, 'id');
			uci.sections('resproxy', 'policy').forEach(function(policy) {
				var current = values(uci.get('resproxy', policy['.name'], 'rule_refs'));
				if (current.indexOf(id) < 0)
					return;
				var refs = current
					.filter(function(ref) { return ref !== id; });
				if (refs.length)
					uci.set('resproxy', policy['.name'], 'rule_refs', refs);
				else
					uci.unset('resproxy', policy['.name'], 'rule_refs');
			});
			uci.remove('resproxy', section);
			return map.save(null, true);
		};
		groups.description = _('Create a rule, then select it in a route list.');
		groups.handleAdd = function() {
			if (uci.sections('resproxy', 'rule').length >= 128) {
				ui.addNotification(null, E('p', {}, _('The limit of 128 rules has been reached.')));
				return;
			}
			var section;
			// Parse existing forms before adding the intentionally empty draft.
			return map.save(function() {
				section = uci.add('resproxy', 'rule');
				uci.set('resproxy', section, 'id', ('rule-' + section).slice(0, 32));
				uci.set('resproxy', section, 'name', '');
				customOpenStates[section] = true;
			}).then(function() {
				var input = map.findElement('id', name.cbid(uci.resolveSID('resproxy', section)));
				if (input) (input.querySelector('input') || input).focus();
			});
		};
		var name = groups.option(form.Value, 'name', _('Rule name'));
		name.rmempty = false;
		name.placeholder = _('For example: Work services through WAN');
		name.write = function(section, value) { uci.set('resproxy', section, 'name', value.trim()); };
		name.cfgvalue = function(section) { var current = uci.get('resproxy', section, 'name'); return current || ''; };
		name.validate = function(section, value) {
			if (!value.trim() || value.length > 120 || /[\x00-\x1f\x7f]/.test(value)) return _('Enter a name of up to 120 characters.');
			if (uci.sections('resproxy', 'rule').some(function(rule) { return rule['.name'] !== section && String(rule.name || rule.id).toLocaleLowerCase() === value.trim().toLocaleLowerCase(); })) return _('This rule name already exists.');
			return true;
		};
		var action = groups.option(form.ListValue, 'action', _('Action'));
		action.value('direct', _('Direct WAN'));
		action.value('proxy', _('Proxy through this route list'));
		action.default = 'direct';
		action.rmempty = false;
        var outage = groups.option(form.ListValue, 'proxy_outage_action', _('When all proxies fail'));
        outage.depends('action', 'proxy');
        outage.value('block', _('Block'));
        outage.value('wan', _('Through WAN'));
        outage.default = 'block';
        outage.rmempty = false;
        outage.retain = true;
        outage.write = function(section, value) { uci.set('resproxy', section, 'proxy_outage_action', value); uci.unset('resproxy', section, 'outage_action'); };
		var protocol = groups.option(CheckboxList, 'protocols', _('Protocol'));
		['http', 'tls', 'quic', 'bittorrent'].forEach(function(value) { protocol.value(value, value.toUpperCase()); });
		protocol.rmempty = true;
		var inbound = groups.option(CheckboxList, 'inbounds', _('Inbound connection'));
		inbound.value('transparent', _('Network devices — transparent proxy'));
		inbound.value('socks', _('Router applications — SOCKS5'));
		inbound.description = _('Usually leave both unchecked. This filter separates network-device traffic from applications explicitly using SOCKS5 on the router at 127.0.0.1:%s.').format(uci.get('resproxy', 'main', 'socks_port') || '19081');
		inbound.rmempty = true;
		var renderInbound = inbound.render;
		inbound.render = function(index, section, inTable) {
			return Promise.resolve(renderInbound.apply(this, arguments)).then(function(node) {
				return E('details', { 'class': 'sbg-advanced-rule', open: values(uci.get('resproxy', section, 'inbounds')).length ? '' : null }, [
					E('summary', {}, _('Connection type — advanced')), node
				]);
			});
		};
		var network = groups.option(form.ListValue, 'network', _('Network'));
		network.value('', _('TCP and UDP'));
		network.value('tcp', 'TCP'); network.value('udp', 'UDP'); network.rmempty = true;
		var ports = groups.option(form.Value, 'ports', _('Destination ports'));
		ports.placeholder = '80,443,1000-2000'; ports.rmempty = true;
		ports.validate = function(section, value) {
			if (!value) return true;
			return value.length <= 512 && value.split(',').every(function(part) {
				if (!/^\d+(-\d+)?$/.test(part)) return false;
				var range = part.split('-').map(Number);
				return range[0] >= 1 && range[0] <= 65535 && (range.length === 1 || range[1] >= range[0] && range[1] <= 65535);
			}) || _('Use ports 1–65535 separated by commas, or ranges such as 1000-2000.');
		};
		var domains = groups.option(form.TextValue, 'domains', _('Domains — one per line'));
		domains.rows = 6;
		domains.placeholder = 'example.com\nfull:login.example.com';
		domains.rmempty = true;
		var cidrs = groups.option(form.TextValue, 'cidrs', _('IP networks — one per line'));
		cidrs.rows = 6;
		cidrs.placeholder = '203.0.113.0/24';
		cidrs.rmempty = true;
		cidrs.description = _('Optional. A rule may match only ports or a detected protocol. Leave domains and IP empty in that case.');
		var parseGroups = groups.parse;
		groups.parse = function() {
			var invalid = uci.sections('resproxy', 'rule').find(function(saved) {
				var section = saved['.name'];
				return groups.filter(section) && ![domains, cidrs, protocol, inbound, network, ports].some(function(field) {
					var current = field.formvalue(section); return Array.isArray(current) ? current.length > 0 : !!String(current || '').trim();
				});
			});
			if (invalid) {
				customOpenStates[invalid['.name']] = true;
				if (map.root) Array.from(map.root.querySelectorAll('.sbg-custom-rule-settings')).forEach(function(details) {
					if (details.getAttribute('data-rule-section') === invalid['.name']) details.open = true;
				});
				return Promise.reject(new Error(_('Add a domain, IP network or traffic condition.')));
			}
			return parseGroups.apply(this, arguments);
		};
        var common = map.section(form.NamedSection, 'main', 'main', _('Global exceptions and IPv6'));
        common.addremove = false;
        common.description = _('Applies to network-device traffic in all route lists.');
        var renderCommon = common.renderContents;
        common.renderContents = function() {
            var node = renderCommon.apply(this, arguments);
            node.classList.add('sbg-capture-settings');
            node.querySelector('.cbi-section-node').id = 'cbi-resproxy-capture-settings';
            return node;
        };
        var ipv6 = common.option(form.ListValue, 'ipv6_mode', _('IPv6 handling'));
        ipv6.value('proxy', _('Through proxy'));
        ipv6.value('direct', _('Direct — without proxy'));
        ipv6.value('block', _('Block internet access'));
        ipv6.default = 'proxy';
        ipv6.rmempty = false;
        ipv6.description = _('Blocking TCP/UDP preserves local IPv6. Ping follows its separate setting.');
        var ping = common.option(form.Flag, 'block_ping', _('Block WAN ping'));
        ping.default = '0';
        ping.rmempty = false;
        var bypass = common.option(form.TextValue, 'bypass_cidrs', _('Direct IP addresses and networks'));
        bypass.rows = 13;
        bypass.rmempty = true;

        var detected = catalog.automatic_ips || [];
        function detectedLines() { return detected.map(function(item) { return item.ip + ' # ' + item.source; }); }
        function userEntries(value) {
            var generated = detectedLines();
            return String(value || '').split(/\r?\n/).filter(function(line) {
                return generated.indexOf(line.trim()) < 0;
            }).map(function(line) { return line.split('#')[0].trim(); }).filter(Boolean);
        }
        bypass.cfgvalue = function(section) {
            return detectedLines().concat(values(uci.get('resproxy', section, 'bypass_cidrs'))).join('\n');
        };
        var renderBypass = bypass.renderWidget;
        bypass.renderWidget = function(section) {
            return Promise.resolve(renderBypass.apply(this, arguments)).then(function(node) {
                var textarea = node.matches('textarea') ? node : node.querySelector('textarea');
                poll.add(function() {
                    if (!textarea || document.activeElement === textarea || !document.contains(textarea)) return;
                    return fs.exec('/usr/libexec/resproxy-rulesets').then(function(result) {
                        if (result.code !== 0) return;
                        var next = JSON.parse(result.stdout).automatic_ips;
                        if (!Array.isArray(next) || JSON.stringify(next) === JSON.stringify(detected)) return;
                        var manual = userEntries(textarea.value);
                        detected = next;
                        textarea.value = detectedLines().concat(manual).join('\n');
                    }).catch(function() {});
                }, 15);
                return node;
            });
        };
        bypass.write = function(section, value) {
            var entries = userEntries(value);
            if (entries.length) uci.set('resproxy', section, 'bypass_cidrs', entries);
            else uci.unset('resproxy', section, 'bypass_cidrs');
        };
        bypass.validate = function(section, value) {
            var entries = userEntries(value);
            if (entries.length > 256) return _('Maximum: 256 IP addresses or networks.');
            var validator = {
                factory: validation,
                apply: function(type, value, args) { if (value != null) this.value = value; return validation.types[type].apply(this, args); },
                assert: function(condition) { return !!condition; }
            };
            return entries.every(function(entry) {
                return !/[%;]/.test(entry) && !/^::ffff:/i.test(entry) &&
                    (entry.includes('/') ? /\/\d+$/.test(entry) && validator.apply('cidr', entry) : validator.apply('ipaddr', entry, ['nomask']));
            }) ||
                _('Enter IPv4, IPv6 or CIDR networks, one per line.');
        };
        bypass.description = _('One IP or CIDR per line. # marks detected VPN addresses.');
		var updates = map.section(form.NamedSection, 'main', 'main', _('Rule database updates'));
		updates.addremove = false;
		var renderUpdates = updates.renderContents;
		updates.renderContents = function() {
			var node = renderUpdates.apply(this, arguments);
			node.classList.add('sbg-rule-updates');
			node.querySelector('.cbi-section-node').id = 'cbi-resproxy-rule-updates';
			return node;
		};
		var interval = updates.option(IntervalValue.Value, 'ruleset_update_interval_seconds', _('Automatic update interval'));
		interval.datatype = 'ufloat';
		interval.description = _('Applies to all loaded rule databases. Minimum: 1 hour; maximum: 7 days.');
		interval.default = '86400';
		interval.rmempty = false;
		var refresh = updates.option(form.Button, '_rulesets_refresh', _('Update now'));
		refresh.inputtitle = _('Update rules');
		refresh.inputstyle = 'action';
		refresh.onclick = function() {
			return fs.exec('/usr/libexec/resproxy-rulesets-refresh').then(function(result) {
				if (result.code !== 0)
					throw new Error(result.stderr || _('Catalog update request failed.'));
				ui.addNotification(null, E('p', {}, _('Catalog update requested. Current routes remain active.')));
			}).catch(function(error) {
				ui.addNotification(null, E('p', {}, String(error)));
			});
		};
        var cachedEditor = Drafts.read('preset-editor');
        if (cachedEditor && cachedEditor.open) {
            var activeKey = cachedEditor.active;
            if (activeKey !== '_new') {
                var activeRules = uci.sections('resproxy', 'rule').filter(function(rule) { return rule.preset_key && (rule.preset_variant || 'legacy') === activeKey; });
                if (activeRules.length) loadVariant(activeKey, {name:activeRules[0].preset_variant_name || _('Ready-made rules'),rules:activeRules});
                else activeKey = '_new';
            }
            if (activeKey === '_new' && restoreDraft(activeKey)) creatorOpen = true;
        }
        // This editor owns a draft per variant. A second, page-wide snapshot
        // must not restore virtual fields from another variant over that state.
        settings.children.forEach(function(field) { field._sbgOwnDraft = true; });
        var draftSession = Drafts.attach(map, 'rulesets');
        return map.render().then(function(node) {
            draftSession.bind(node);
            ['input', 'change', 'widget-change'].forEach(function(type) { node.addEventListener(type, rememberDraft); });
            window.addEventListener('pagehide', rememberDraft, {once:true});
            document.addEventListener('uci-reverted', function() { Drafts.write('preset-editor', null); });
            return sbgTheme.decorate(node);
        });
	}
});
