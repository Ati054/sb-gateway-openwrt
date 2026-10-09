'use strict';
'require form';
'require baseclass';

// Display convenient units while retaining seconds in the existing UCI option.
var IntervalValue = form.Value.extend({
 minimumSeconds: 3600,
 maximumSeconds: 604800,
 intervalError: _('Choose an interval from 1 hour to 7 days, with whole seconds.'),
 renderWidget: function(section, index, cfgvalue) {
  var seconds = Number(cfgvalue != null ? cfgvalue : this.default);
  var factor = seconds % 3600 === 0 ? 3600 : seconds % 60 === 0 ? 60 : 1;
  this.units = this.units || {};
  this.units[section] = factor;
  var field = this;
  var unit = E('select', { 'aria-label': _('Interval units'), disabled: this.map.readonly ? '' : null,
   change: function(event) {
    field.units[section] = Number(event.target.value);
    var input = field.getUIElement(section);
    if (input) input.triggerValidation();
    event.currentTarget.dispatchEvent(new CustomEvent('widget-change', { bubbles: true }));
   }
  }, [[1, _('Seconds')], [60, _('Minutes')], [3600, _('Hours')]].map(function(item) {
   return E('option', { value: item[0], selected: item[0] === factor ? '' : null }, item[1]);
  }));
  return E('div', { 'class': 'sbg-interval-control' }, [
   form.Value.prototype.renderWidget.call(this, section, index, String(seconds / factor)), unit
  ]);
 },
 formvalue: function(section) {
  var value = form.Value.prototype.formvalue.call(this, section);
  return value == null || value === '' ? value : String(Math.round(Number(value) * this.units[section]));
 },
 validate: function(section, value) {
  var seconds = Number(value) * this.units[section];
  return Number.isFinite(seconds) && seconds >= this.minimumSeconds && seconds <= this.maximumSeconds && Math.abs(seconds - Math.round(seconds)) < 0.000001 ||
   this.intervalError;
 }
});


return baseclass.extend({ Value: IntervalValue });
