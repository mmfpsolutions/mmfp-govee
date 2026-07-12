/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee — Mappings page (CRUD + editor modal)
'use strict';

var mappingsState = {
    mappings: [],
    tokens: [],
    devices: [],
    eventCatalog: { gss: [], gssm: [] },
    editingId: null,      // null = creating
    selectedEvents: [],   // events checked in the editor
};

var effectPresets = {
    celebrate: { effect: 'flash', params: { colorA: 16756736, colorB: 65280, cycles: 5, delayMs: 700, brightness: 100, endState: 'holdB' } },
    alert:     { effect: 'flash', params: { colorA: 16711680, colorB: 0, cycles: 5, delayMs: 700, brightness: 100, endState: 'holdA' } },
    allclear:  { effect: 'solid', params: { colorA: 65280, brightness: 100 } },
    off:       { effect: 'off', params: {} }
};

document.addEventListener('DOMContentLoaded', function() {
    Promise.all([
        api.getMappings(),
        api.getTokens(),
        api.getDevices().catch(function() { return { data: { devices: [] } }; }),
        api.getEvents()
    ]).then(function(results) {
        mappingsState.mappings = (results[0].data && results[0].data.mappings) || [];
        mappingsState.tokens = (results[1].data && results[1].data.tokens) || [];
        mappingsState.devices = (results[2].data && results[2].data.devices) || [];
        mappingsState.eventCatalog = results[3].data || { gss: [], gssm: [] };
        renderMappings();
    }).catch(function(err) {
        document.getElementById('mappings-loading').innerHTML =
            '<p style="color: #f87171;">Could not load mappings: ' + escapeHtml(err.message) + '</p>';
    });
});

function reloadMappings() {
    return api.getMappings().then(function(resp) {
        mappingsState.mappings = (resp.data && resp.data.mappings) || [];
        renderMappings();
    });
}

function deviceLabel(ref) {
    for (var i = 0; i < mappingsState.devices.length; i++) {
        var d = mappingsState.devices[i];
        if (d.device === ref.device) return d.deviceName;
    }
    return ref.sku + ' / ' + ref.device;
}

function renderMappings() {
    document.getElementById('mappings-loading').classList.add('hidden');
    var list = document.getElementById('mappings-list');
    var empty = document.getElementById('mappings-empty');
    list.classList.remove('hidden');

    var countEl = document.getElementById('mappings-count');
    if (countEl) countEl.textContent = mappingsState.mappings.length;

    if (!mappingsState.mappings.length) {
        list.innerHTML = '';
        empty.classList.remove('hidden');
        return;
    }
    empty.classList.add('hidden');

    list.innerHTML = mappingsState.mappings.map(function(m) {
        var events = (m.events || []).map(function(e) {
            return '<span class="text-xs px-2 py-0.5 rounded" style="background: rgba(59, 130, 246, 0.15); color: #60a5fa;">' + escapeHtml(e) + '</span>';
        }).join(' ');
        var devices = (m.devices || []).map(function(d) {
            var label = escapeHtml(deviceLabel(d));
            if (d.afterScene && d.afterScene.name) {
                label += ' <span style="color: #64748b;">&rarr; scene: ' + escapeHtml(d.afterScene.name) + '</span>';
            }
            return label;
        }).join(', ');
        var effectDesc = escapeHtml(m.effect);
        if (m.effect === 'flash') {
            effectDesc += ' ' + colorSwatch(m.params.colorA) + '/' + colorSwatch(m.params.colorB) + ' &times;' + m.params.cycles;
        } else if (m.effect === 'solid') {
            effectDesc += ' ' + colorSwatch(m.params.colorA);
        }
        var statusPill = m.enabled
            ? '<span class="text-xs px-2 py-0.5 rounded" style="background: rgba(34, 197, 94, 0.15); color: #4ade80;">enabled</span>'
            : '<span class="text-xs px-2 py-0.5 rounded" style="background: rgba(100, 116, 139, 0.2); color: #94a3b8;">disabled</span>';

        return '<div class="device-card px-5 py-4">' +
            '<div class="flex items-center justify-between mb-2">' +
                '<div class="flex items-center space-x-3">' +
                    '<span class="device-card-name">' + escapeHtml(m.name) + '</span>' +
                    statusPill +
                '</div>' +
                '<div class="flex items-center space-x-2">' +
                    '<button onclick="testMapping(\'' + m.id + '\', this)" class="text-xs px-3 py-1.5 rounded transition-colors" ' +
                        'style="background: rgba(34, 197, 94, 0.15); color: #4ade80; border: 1px solid rgba(34, 197, 94, 0.3); cursor: pointer;">Test</button>' +
                    '<button onclick="openMappingModal(\'' + m.id + '\')" class="text-xs px-3 py-1.5 rounded transition-colors" ' +
                        'style="background: rgba(59, 130, 246, 0.15); color: #60a5fa; border: 1px solid rgba(59, 130, 246, 0.3); cursor: pointer;">Edit</button>' +
                    '<button onclick="confirmDeleteMapping(\'' + m.id + '\', \'' + escapeHtml(m.name) + '\')" class="text-xs px-3 py-1.5 rounded transition-colors" ' +
                        'style="background: rgba(239, 68, 68, 0.1); color: #f87171; border: 1px solid rgba(239, 68, 68, 0.3); cursor: pointer;">Delete</button>' +
                '</div>' +
            '</div>' +
            '<div class="data-row"><span class="data-label">Token</span><span class="data-value">' + escapeHtml(m.token) + '</span></div>' +
            '<div class="data-row"><span class="data-label">Events</span><span class="data-value">' + events + '</span></div>' +
            (m.entityFilter ? '<div class="data-row"><span class="data-label">Entity filter</span><span class="data-value">' + escapeHtml(m.entityFilter) + '</span></div>' : '') +
            '<div class="data-row"><span class="data-label">Devices</span><span class="data-value">' + devices + '</span></div>' +
            '<div class="data-row"><span class="data-label">Effect</span><span class="data-value">' + effectDesc + '</span></div>' +
            '<div class="data-row"><span class="data-label">Cooldown</span><span class="data-value">' + (m.cooldownSeconds || 0) + 's</span></div>' +
        '</div>';
    }).join('');
}

function testMapping(id, btn) {
    btn.disabled = true;
    btn.textContent = 'Running...';
    api.testMapping(id).then(function(resp) {
        btn.textContent = (resp.data && resp.data.result) || 'Sent';
    }).catch(function(err) {
        btn.textContent = 'Failed';
        console.error('Mapping test failed:', err.message);
    }).finally(function() {
        setTimeout(function() {
            btn.disabled = false;
            btn.textContent = 'Test';
        }, 3000);
    });
}

// ── Editor modal ─────────────────────────────────────────────

function openMappingModal(id) {
    mappingsState.editingId = id;
    var m = null;
    if (id) {
        for (var i = 0; i < mappingsState.mappings.length; i++) {
            if (mappingsState.mappings[i].id === id) { m = mappingsState.mappings[i]; break; }
        }
    }

    document.getElementById('mapping-modal-title').textContent = m ? 'Edit Mapping' : 'New Mapping';
    document.getElementById('mapping-modal-error').classList.add('hidden');
    document.getElementById('mapping-name').value = m ? m.name : '';
    document.getElementById('mapping-cooldown').value = m ? (m.cooldownSeconds || 10) : 10;
    document.getElementById('mapping-enabled').checked = m ? !!m.enabled : true;
    document.getElementById('mapping-entity').value = (m && m.entityFilter) || '';
    document.getElementById('mapping-preset').value = '';

    // Token select: * plus configured token names
    var tokenSel = document.getElementById('mapping-token');
    var opts = '<option value="*">* (any token)</option>';
    mappingsState.tokens.forEach(function(t) {
        opts += '<option value="' + escapeHtml(t.name) + '">' + escapeHtml(t.name) + '</option>';
    });
    tokenSel.innerHTML = opts;
    tokenSel.value = (m && m.token) || '*';
    if (tokenSel.value !== ((m && m.token) || '*')) tokenSel.value = '*'; // referenced token was deleted

    mappingsState.selectedEvents = m ? (m.events || []).slice() : [];
    renderEventChecklist();

    renderDeviceChecklist(m ? (m.devices || []) : []);

    document.getElementById('mapping-effect').value = m ? m.effect : 'flash';
    renderEffectParams(m ? m.params : null);

    var modal = document.getElementById('mapping-modal');
    modal.classList.remove('hidden');
    modal.style.display = 'flex';
}

function closeMappingModal() {
    var modal = document.getElementById('mapping-modal');
    if (modal) {
        modal.classList.add('hidden');
        modal.style.display = 'none';
    }
}

function renderEventChecklist() {
    var box = document.getElementById('mapping-events');
    var catalog = mappingsState.eventCatalog;
    var html = '';

    ['gss', 'gssm'].forEach(function(source) {
        var events = catalog[source] || [];
        if (!events.length) return;
        html += '<div class="text-xs font-semibold uppercase mt-2 mb-1" style="color: #64748b;">' + source.toUpperCase() + '</div>';
        html += '<div class="grid grid-cols-2 gap-x-4">';
        events.forEach(function(e) {
            var checked = mappingsState.selectedEvents.indexOf(e) !== -1 ? ' checked' : '';
            html += '<label class="flex items-center space-x-2 text-sm py-0.5" style="color: #e2e8f0; cursor: pointer;">' +
                '<input type="checkbox" class="settings-checkbox mapping-event-cb" value="' + escapeHtml(e) + '"' + checked + '>' +
                '<span>' + escapeHtml(e) + '</span></label>';
        });
        html += '</div>';
    });

    // Custom events already selected but not in the catalog
    var known = (catalog.gss || []).concat(catalog.gssm || []);
    var custom = mappingsState.selectedEvents.filter(function(e) { return known.indexOf(e) === -1; });
    if (custom.length) {
        html += '<div class="text-xs font-semibold uppercase mt-2 mb-1" style="color: #64748b;">Custom</div>';
        custom.forEach(function(e) {
            html += '<label class="flex items-center space-x-2 text-sm py-0.5" style="color: #e2e8f0; cursor: pointer;">' +
                '<input type="checkbox" class="settings-checkbox mapping-event-cb" value="' + escapeHtml(e) + '" checked>' +
                '<span>' + escapeHtml(e) + '</span></label>';
        });
    }

    box.innerHTML = html || '<p class="text-sm" style="color: #64748b;">No event catalog available.</p>';
}

function addCustomEvent() {
    var input = document.getElementById('mapping-event-custom');
    var val = input.value.trim();
    if (!val) return;
    syncSelectedEvents();
    if (mappingsState.selectedEvents.indexOf(val) === -1) {
        mappingsState.selectedEvents.push(val);
    }
    input.value = '';
    renderEventChecklist();
}

function syncSelectedEvents() {
    var checked = [];
    document.querySelectorAll('.mapping-event-cb:checked').forEach(function(cb) {
        checked.push(cb.value);
    });
    mappingsState.selectedEvents = checked;
}

function renderDeviceChecklist(selected) {
    var box = document.getElementById('mapping-devices');
    if (!mappingsState.devices.length) {
        box.innerHTML = '<p class="text-sm" style="color: #64748b;">No devices cached — visit the Devices page first.</p>';
        return;
    }
    // afterScene selections carried by the mapping being edited, keyed by device ID
    mappingsState.afterScenes = {};
    (selected || []).forEach(function(d) {
        if (d.afterScene) mappingsState.afterScenes[d.device] = d.afterScene;
    });

    var selectedIds = (selected || []).map(function(d) { return d.device; });
    box.innerHTML = mappingsState.devices.map(function(d) {
        var isChecked = selectedIds.indexOf(d.device) !== -1;
        var checked = isChecked ? ' checked' : '';
        var disabled = d.controllable ? '' : ' disabled';
        var dim = d.controllable ? '' : ' opacity-50';
        return '<div class="py-0.5' + dim + '">' +
            '<label class="flex items-center space-x-2 text-sm" style="color: #e2e8f0; cursor: pointer;">' +
                '<input type="checkbox" class="settings-checkbox mapping-device-cb" value="' + escapeHtml(d.device) + '" data-sku="' + escapeHtml(d.sku) + '"' + checked + disabled +
                    ' onchange="onDeviceToggled(this)">' +
                '<span>' + escapeHtml(d.deviceName) + ' <span style="color: #64748b;">(' + escapeHtml(d.sku) + ')</span></span>' +
            '</label>' +
            '<div class="ml-6 mt-1' + (isChecked ? '' : ' hidden') + '" id="afterscene-row-' + cssSafe(d.device) + '">' +
                '<label class="text-xs mr-2" style="color: #64748b;">After effect:</label>' +
                '<select class="settings-select text-xs mapping-afterscene" data-device="' + escapeHtml(d.device) + '" data-sku="' + escapeHtml(d.sku) + '">' +
                    '<option value="">(leave as effect end state)</option>' +
                '</select>' +
            '</div>' +
        '</div>';
    }).join('');

    // Load scene lists for devices that start checked (editing an existing mapping)
    document.querySelectorAll('.mapping-device-cb:checked').forEach(function(cb) {
        loadSceneOptions(cb.value, cb.getAttribute('data-sku'));
    });
}

// Device IDs contain colons — not valid inside an element id.
function cssSafe(device) {
    return device.replace(/[^A-Za-z0-9]/g, '_');
}

function onDeviceToggled(cb) {
    var row = document.getElementById('afterscene-row-' + cssSafe(cb.value));
    if (!row) return;
    if (cb.checked) {
        row.classList.remove('hidden');
        loadSceneOptions(cb.value, cb.getAttribute('data-sku'));
    } else {
        row.classList.add('hidden');
    }
}

// Fills a device's after-effect dropdown from its scene catalog (cached
// server-side). Selection is restored from the mapping being edited.
function loadSceneOptions(device, sku) {
    var sel = document.querySelector('.mapping-afterscene[data-device="' + device + '"]');
    if (!sel || sel.options.length > 1) return; // already loaded
    api.getDeviceScenes(device, sku).then(function(resp) {
        var scenes = (resp.data && resp.data.scenes) || [];
        var current = mappingsState.afterScenes[device];
        scenes.forEach(function(s) {
            var opt = document.createElement('option');
            opt.value = s.instance + '|' + s.name;
            opt.textContent = s.name + (s.instance === 'diyScene' ? ' (DIY)' : '');
            opt.setAttribute('data-scene', JSON.stringify(s));
            if (current && current.name === s.name && current.instance === s.instance) {
                opt.selected = true;
            }
            sel.appendChild(opt);
        });
    }).catch(function(err) {
        console.error('Scene list failed for ' + device + ':', err.message);
    });
}

function applyPreset() {
    var key = document.getElementById('mapping-preset').value;
    if (!key || !effectPresets[key]) return;
    var preset = effectPresets[key];
    document.getElementById('mapping-effect').value = preset.effect;
    renderEffectParams(preset.params);
}

// Renders the param inputs for the selected effect. `params` = existing
// values, or null for defaults.
function renderEffectParams(params) {
    var effect = document.getElementById('mapping-effect').value;
    var p = params || {};
    var box = document.getElementById('mapping-params');
    var html = '';

    function colorInput(id, label, def) {
        var val = rgbIntToHex(p[id] !== undefined ? p[id] : def);
        return '<div><label class="block text-sm mb-1.5" style="color: #94a3b8;">' + label + '</label>' +
            '<input type="color" id="param-' + id + '" value="' + val + '" class="w-16 h-9 rounded cursor-pointer" style="background-color: #0f172a; border: 1px solid #475569;"></div>';
    }
    function numInput(id, label, def, min, max, width) {
        var val = p[id] !== undefined && p[id] !== 0 ? p[id] : def;
        return '<div><label class="block text-sm mb-1.5" style="color: #94a3b8;">' + label + '</label>' +
            '<input type="number" id="param-' + id + '" value="' + val + '" min="' + min + '" max="' + max + '" class="settings-input ' + width + '"></div>';
    }

    if (effect === 'flash') {
        html += colorInput('colorA', 'Color A', 16756736);
        html += colorInput('colorB', 'Color B', 65280);
        html += numInput('cycles', 'Cycles', 5, 1, 20, 'w-20');
        html += numInput('delayMs', 'Delay (ms)', 700, 100, 5000, 'w-24');
        html += numInput('brightness', 'Brightness %', 100, 1, 100, 'w-20');
        var endState = p.endState || 'holdB';
        html += '<div><label class="block text-sm mb-1.5" style="color: #94a3b8;">End state</label>' +
            '<select id="param-endState" class="settings-select">' +
            '<option value="holdA"' + (endState === 'holdA' ? ' selected' : '') + '>hold Color A</option>' +
            '<option value="holdB"' + (endState === 'holdB' ? ' selected' : '') + '>hold Color B</option>' +
            '<option value="off"' + (endState === 'off' ? ' selected' : '') + '>off</option>' +
            '</select></div>';
        html += '<div><label class="block text-sm mb-1.5" style="color: #94a3b8;">Restore prior state</label>' +
            '<input type="checkbox" id="param-restore" class="settings-checkbox"' + (p.restore ? ' checked' : '') + '></div>';
    } else if (effect === 'solid') {
        html += colorInput('colorA', 'Color', 65280);
        html += numInput('brightness', 'Brightness %', 100, 1, 100, 'w-20');
    } else if (effect === 'on') {
        html += numInput('brightness', 'Brightness %', 100, 1, 100, 'w-20');
    }
    // 'off' has no params

    box.innerHTML = html;
}

function collectParams() {
    var effect = document.getElementById('mapping-effect').value;
    var params = {};
    function num(id) {
        var el = document.getElementById('param-' + id);
        return el ? parseInt(el.value, 10) || 0 : 0;
    }
    function color(id) {
        var el = document.getElementById('param-' + id);
        return el ? hexToRgbInt(el.value) : 0;
    }
    if (effect === 'flash') {
        params.colorA = color('colorA');
        params.colorB = color('colorB');
        params.cycles = num('cycles');
        params.delayMs = num('delayMs');
        params.brightness = num('brightness');
        params.endState = document.getElementById('param-endState').value;
        params.restore = document.getElementById('param-restore').checked;
    } else if (effect === 'solid') {
        params.colorA = color('colorA');
        params.brightness = num('brightness');
    } else if (effect === 'on') {
        params.brightness = num('brightness');
    }
    return params;
}

function saveMapping() {
    syncSelectedEvents();

    var devices = [];
    document.querySelectorAll('.mapping-device-cb:checked').forEach(function(cb) {
        var ref = { device: cb.value, sku: cb.getAttribute('data-sku') };
        var sel = document.querySelector('.mapping-afterscene[data-device="' + cb.value + '"]');
        if (sel && sel.value) {
            var opt = sel.options[sel.selectedIndex];
            var scene = JSON.parse(opt.getAttribute('data-scene'));
            ref.afterScene = { instance: scene.instance, name: scene.name, value: scene.value };
        }
        devices.push(ref);
    });

    var body = {
        name: document.getElementById('mapping-name').value.trim(),
        token: document.getElementById('mapping-token').value,
        events: mappingsState.selectedEvents,
        entityFilter: document.getElementById('mapping-entity').value.trim(),
        devices: devices,
        effect: document.getElementById('mapping-effect').value,
        params: collectParams(),
        cooldownSeconds: parseInt(document.getElementById('mapping-cooldown').value, 10) || 0,
        enabled: document.getElementById('mapping-enabled').checked
    };

    var btn = document.getElementById('mapping-save-btn');
    btn.disabled = true;
    btn.style.opacity = '0.6';

    var call = mappingsState.editingId
        ? api.updateMapping(mappingsState.editingId, body)
        : api.createMapping(body);

    call.then(function() {
        closeMappingModal();
        return reloadMappings();
    }).catch(function(err) {
        var el = document.getElementById('mapping-modal-error');
        el.textContent = err.message;
        el.classList.remove('hidden');
    }).finally(function() {
        btn.disabled = false;
        btn.style.opacity = '';
    });
}

// ── Delete confirm modal ─────────────────────────────────────

function openMappingConfirmModal() {
    var modal = document.getElementById('mapping-confirm-modal');
    if (!modal) return;
    modal.classList.remove('hidden');
    modal.style.display = 'flex';
}

function closeMappingConfirmModal() {
    var modal = document.getElementById('mapping-confirm-modal');
    if (modal) {
        modal.classList.add('hidden');
        modal.style.display = 'none';
    }
}

function confirmDeleteMapping(id, name) {
    var title = document.getElementById('mapping-confirm-modal-title');
    var content = document.getElementById('mapping-confirm-modal-content');
    if (title) title.textContent = 'Delete ' + name;
    if (content) {
        content.innerHTML =
            '<p style="color: #94a3b8;" class="mb-4">Delete mapping <span class="text-white font-semibold">' + escapeHtml(name) + '</span>?</p>' +
            '<p style="color: #64748b;" class="text-sm mb-6">Webhook events routed by this mapping will stop firing effects immediately.</p>' +
            '<div class="flex justify-end space-x-3">' +
                '<button onclick="closeMappingConfirmModal()" ' +
                    'class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                    'style="background: rgba(51, 65, 85, 0.5); color: #e2e8f0; border: 1px solid rgba(71, 85, 105, 0.5); cursor: pointer;">Cancel</button>' +
                '<button onclick="deleteMapping(\'' + id + '\')" ' +
                    'class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                    'style="background: rgba(239, 68, 68, 0.15); color: #f87171; border: 1px solid rgba(239, 68, 68, 0.3); cursor: pointer;">Delete</button>' +
            '</div>';
    }
    openMappingConfirmModal();
}

function deleteMapping(id) {
    api.deleteMapping(id).then(function() {
        closeMappingConfirmModal();
        return reloadMappings();
    }).catch(function(err) {
        console.error('Delete failed:', err.message);
        closeMappingConfirmModal();
    });
}
