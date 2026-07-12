/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee — Device controller page. The controls are rendered from the
// device's own capability declarations (widget registry) — no per-model code.
'use strict';

var dd = {
    device: window.GOVEE_DEVICE_ID,
    info: null,          // deviceView from /api/v1/devices
    state: {},           // "type|instance" → raw reported value
    online: false,
    assignedScene: null
};

document.addEventListener('DOMContentLoaded', function() {
    api.getDevices().then(function(resp) {
        var devices = (resp.data && resp.data.devices) || [];
        for (var i = 0; i < devices.length; i++) {
            if (devices[i].device === dd.device) { dd.info = devices[i]; break; }
        }
        if (!dd.info) {
            ddShowError('Device not found in the cache — refresh the Devices page.');
            return;
        }
        dd.assignedScene = dd.info.assignedScene || null;
        ddRenderHeader();
        ddRenderControls();
        document.getElementById('dd-loading').classList.add('hidden');
        document.getElementById('dd-content').classList.remove('hidden');
        ddRefreshState(null); // one read on open; manual after (30/min/device budget)
    }).catch(function(err) {
        ddShowError('Could not load device: ' + err.message);
    });
});

function ddShowError(msg) {
    document.getElementById('dd-loading').classList.add('hidden');
    var el = document.getElementById('dd-error');
    el.textContent = msg;
    el.classList.remove('hidden');
}

function ddRenderHeader() {
    document.getElementById('dd-name').textContent = dd.info.deviceName;
    document.getElementById('dd-sku').textContent = dd.info.sku;
    document.getElementById('dd-id').textContent = dd.info.device;

    var sel = document.getElementById('dd-assigned-scene');
    sel.innerHTML = dd.assignedScene
        ? '<option value="" selected>' + escapeHtml(dd.assignedScene.name) + '</option>'
        : '<option value="" selected>(none — restore last state)</option>';
}

// ── Live state ───────────────────────────────────────────────

function stateKey(type, instance) { return type + '|' + instance; }

function ddRefreshState(btn) {
    if (btn) { btn.disabled = true; btn.textContent = 'Reading...'; }
    api.getDeviceState(dd.device, dd.info.sku).then(function(resp) {
        var data = resp.data || {};
        dd.online = !!data.online;
        dd.state = {};
        (data.states || []).forEach(function(s) {
            dd.state[stateKey(s.type, s.instance)] = s.value;
        });
        ddApplyStateToWidgets();

        var online = document.getElementById('dd-online');
        online.textContent = dd.online ? 'online' : 'offline';
        online.style.background = dd.online ? 'rgba(34, 197, 94, 0.15)' : 'rgba(239, 68, 68, 0.15)';
        online.style.color = dd.online ? '#4ade80' : '#f87171';
        document.getElementById('dd-stale').classList.toggle('hidden', dd.online);
    }).catch(function(err) {
        console.error('State read failed:', err.message);
    }).finally(function() {
        if (btn) { btn.disabled = false; btn.textContent = 'Refresh State'; }
    });
}

// Pushes reported values into the rendered widgets. Empty string = the
// instance doesn't support query (per Govee docs) — leave the widget as-is.
function ddApplyStateToWidgets() {
    document.querySelectorAll('[data-state-key]').forEach(function(el) {
        var v = dd.state[el.getAttribute('data-state-key')];
        if (v === undefined || v === '' || v === null) return;
        if (el.classList.contains('dd-toggle-group')) {
            var on = (v === 1 || v === true) ? true : ((v === 0 || v === false) ? false : null);
            ddPaintToggleGroup(el, on);
        } else if (el.type === 'range') {
            el.value = v;
            var out = document.getElementById(el.id + '-value');
            if (out) out.textContent = v;
        } else if (el.type === 'color') {
            el.value = rgbIntToHex(v);
        } else if (el.tagName === 'SELECT') {
            el.value = String(v);
        }
    });
}

// ── Widget registry ──────────────────────────────────────────

var CAP = {
    ONOFF: 'devices.capabilities.on_off',
    TOGGLE: 'devices.capabilities.toggle',
    RANGE: 'devices.capabilities.range',
    COLOR: 'devices.capabilities.color_setting',
    SCENE: 'devices.capabilities.dynamic_scene',
    MODE: 'devices.capabilities.mode',
    MUSIC: 'devices.capabilities.music_setting',
    SEGMENT: 'devices.capabilities.segment_color_setting'
};

// "dreamViewToggle" → "Dream View"
function humanizeInstance(instance) {
    var s = instance.replace(/Toggle$/, '').replace(/Mode$/, ' Mode');
    s = s.replace(/([a-z])([A-Z])/g, '$1 $2');
    return s.charAt(0).toUpperCase() + s.slice(1);
}

function ddRenderControls() {
    var caps = dd.info.capabilityDetails || [];
    var sections = { primary: [], scenes: [], toggles: [], modes: [], music: [], unsupported: [] };

    caps.forEach(function(c) {
        var p = c.parameters || {};
        var key = stateKey(c.type, c.instance);
        if (c.type === CAP.ONOFF && c.instance === 'powerSwitch') {
            sections.primary.push(ddToggleWidget(c, 'Power', key));
        } else if (c.type === CAP.RANGE && c.instance === 'brightness') {
            sections.primary.push(ddSliderWidget(c, 'Brightness', p, key, ''));
        } else if (c.type === CAP.COLOR && c.instance === 'colorRgb') {
            sections.primary.push(ddColorWidget(c, 'Color', key));
        } else if (c.type === CAP.COLOR && c.instance === 'colorTemperatureK') {
            sections.primary.push(ddSliderWidget(c, 'Color Temperature', p, key, 'K'));
        } else if (c.type === CAP.SCENE && (c.instance === 'lightScene' || c.instance === 'diyScene')) {
            // one combined catalog dropdown covers both — rendered once below
        } else if (c.type === CAP.SCENE && c.instance === 'snapshot') {
            var w = ddSnapshotWidget(c, p);
            if (w) sections.scenes.push(w);
        } else if (c.type === CAP.TOGGLE) {
            sections.toggles.push(ddToggleWidget(c, humanizeInstance(c.instance), key));
        } else if (c.type === CAP.MODE) {
            var mw = ddModeWidget(c, p, key);
            if (mw) sections.modes.push(mw);
        } else if (c.type === CAP.MUSIC) {
            sections.music.push(ddMusicWidget(c, p));
        } else if (c.type === CAP.SEGMENT) {
            sections.unsupported.push(ddUnsupportedRow(c, 'segment controls planned'));
        } else {
            sections.unsupported.push(ddUnsupportedRow(c, 'not yet supported'));
        }
    });

    var hasScenes = caps.some(function(c) {
        return c.type === CAP.SCENE && (c.instance === 'lightScene' || c.instance === 'diyScene');
    });

    var html = '';
    if (sections.primary.length) html += ddSection('Power &amp; Light', '&#128161;', sections.primary.join(''));
    if (hasScenes || sections.scenes.length) {
        var sceneBody = hasScenes ? ddSceneApplyWidget() : '';
        html += ddSection('Scenes', '&#127916;', sceneBody + sections.scenes.join(''));
    }
    if (sections.toggles.length) html += ddSection('Toggles', '&#128280;', sections.toggles.join(''));
    if (sections.modes.length) html += ddSection('Modes', '&#9881;', sections.modes.join(''));
    if (sections.music.length) html += ddSection('Music Mode', '&#127925;', sections.music.join(''));
    if (sections.unsupported.length) html += ddSection('Other Capabilities', '&#128230;', sections.unsupported.join(''));

    document.getElementById('dd-controls').innerHTML = html;
    if (hasScenes) ddLoadSceneCatalog();
}

function ddSection(title, icon, body) {
    return '<div class="device-card px-5 py-4 mb-4">' +
        '<div class="flex items-center space-x-3 mb-3">' +
            '<span style="color: #38bdf8;">' + icon + '</span>' +
            '<span class="device-card-name">' + title + '</span>' +
        '</div>' + body + '</div>';
}

function ddControlRow(label, control) {
    return '<div class="flex items-center justify-between py-1.5" style="border-bottom: 1px solid rgba(51, 65, 85, 0.3);">' +
        '<span class="text-sm" style="color: #94a3b8;">' + label + '</span>' +
        '<span>' + control + '</span></div>';
}

// data-cap-* attributes carry what the control endpoint needs.
function capAttrs(c, key) {
    return 'data-state-key="' + key + '" data-cap-type="' + c.type + '" data-cap-instance="' + c.instance + '"';
}

// Toggles render as a segmented [On|Off] pair, NOT a blind flip-button:
// most Govee toggle instances return an EMPTY state value (the instance does
// not support query, per the docs), so their current position is unknowable.
// When the device DOES report state (e.g. powerSwitch), the active side is
// highlighted; when it doesn't, neither side is — the operator picks the
// state they want, explicitly.
function ddToggleWidget(c, label, key) {
    return ddControlRow(label,
        '<span class="dd-toggle-group inline-flex rounded overflow-hidden" ' + capAttrs(c, key) +
        ' style="border: 1px solid rgba(71, 85, 105, 0.5);">' +
        '<button type="button" class="dd-seg-on text-xs px-3 py-1 transition-colors" onclick="ddSegClicked(this, 1)"' +
            ' style="background: transparent; color: #94a3b8; border: none; cursor: pointer;">On</button>' +
        '<button type="button" class="dd-seg-off text-xs px-3 py-1 transition-colors" onclick="ddSegClicked(this, 0)"' +
            ' style="background: transparent; color: #94a3b8; border: none; border-left: 1px solid rgba(71, 85, 105, 0.5); cursor: pointer;">Off</button>' +
        '</span>');
}

// on: true / false / null (state not reported — neither side highlighted)
function ddPaintToggleGroup(group, on) {
    var onBtn = group.querySelector('.dd-seg-on');
    var offBtn = group.querySelector('.dd-seg-off');
    if (!onBtn || !offBtn) return;
    onBtn.style.background = on === true ? 'rgba(34, 197, 94, 0.25)' : 'transparent';
    onBtn.style.color = on === true ? '#4ade80' : '#94a3b8';
    offBtn.style.background = on === false ? 'rgba(100, 116, 139, 0.35)' : 'transparent';
    offBtn.style.color = on === false ? '#e2e8f0' : '#94a3b8';
}

function ddSegClicked(btn, value) {
    var group = btn.closest('.dd-toggle-group');
    if (!group) return;
    ddSendControl(group, value, function() { ddPaintToggleGroup(group, value === 1); });
}

function ddSliderWidget(c, label, p, key, unit) {
    var range = (p && p.range) || { min: 1, max: 100 };
    var id = 'dd-slider-' + c.instance;
    return ddControlRow(label + (unit ? ' (' + unit + ')' : ''),
        '<span class="text-xs mr-2" style="color: #e2e8f0;" id="' + id + '-value">--</span>' +
        '<input type="range" id="' + id + '" min="' + range.min + '" max="' + range.max + '" ' + capAttrs(c, key) +
        ' oninput="document.getElementById(\'' + id + '-value\').textContent = this.value"' +
        ' onchange="ddSendControl(this, parseInt(this.value, 10))" style="width: 10rem; vertical-align: middle;">');
}

function ddColorWidget(c, label, key) {
    return ddControlRow(label,
        '<input type="color" ' + capAttrs(c, key) +
        ' onchange="ddSendControl(this, hexToRgbInt(this.value))"' +
        ' class="w-16 h-8 rounded cursor-pointer" style="background-color: #0f172a; border: 1px solid #475569;">');
}

function ddModeWidget(c, p, key) {
    var options = (p && p.options) || [];
    if (!options.length) return null;
    var opts = options.map(function(o) {
        return '<option value="' + escapeHtml(JSON.stringify(o.value)) + '">' + escapeHtml(o.name) + '</option>';
    }).join('');
    return ddControlRow(humanizeInstance(c.instance),
        '<select class="settings-select text-xs" ' + capAttrs(c, key) +
        ' onchange="ddSendControl(this, JSON.parse(this.value))">' +
        '<option value="" selected disabled>choose...</option>' + opts + '</select>');
}

function ddSnapshotWidget(c, p) {
    var options = (p && p.options) || [];
    if (!options.length) return null;
    var opts = options.map(function(o) {
        return '<option value="' + escapeHtml(JSON.stringify(o.value)) + '">' + escapeHtml(o.name) + '</option>';
    }).join('');
    return ddControlRow('Snapshot',
        '<select class="settings-select text-xs" data-cap-type="' + c.type + '" data-cap-instance="snapshot"' +
        ' onchange="ddSendControl(this, JSON.parse(this.value))">' +
        '<option value="" selected disabled>choose...</option>' + opts + '</select>');
}

// ── Scenes (catalog-driven, shared lightScene/diyScene dropdown) ──

function ddSceneApplyWidget() {
    return ddControlRow('Scene',
        '<select id="dd-scene-select" class="settings-select text-xs"><option value="">loading...</option></select> ' +
        '<button onclick="ddApplyScene(this)" class="text-xs px-3 py-1.5 rounded ml-2 transition-colors" ' +
        'style="background: rgba(59, 130, 246, 0.15); color: #60a5fa; border: 1px solid rgba(59, 130, 246, 0.3); cursor: pointer;">Apply</button>');
}

function ddLoadSceneCatalog() {
    api.getDeviceScenes(dd.device, dd.info.sku).then(function(resp) {
        var scenes = (resp.data && resp.data.scenes) || [];
        var sel = document.getElementById('dd-scene-select');
        if (!sel) return;
        sel.innerHTML = '<option value="" selected disabled>choose a scene...</option>';
        scenes.forEach(function(s) {
            var opt = document.createElement('option');
            opt.value = s.instance + '|' + s.name;
            opt.textContent = s.name + (s.instance === 'diyScene' ? ' (DIY)' : '');
            opt.setAttribute('data-scene', JSON.stringify(s));
            sel.appendChild(opt);
        });
    }).catch(function(err) {
        console.error('Scene catalog failed:', err.message);
    });
}

function ddApplyScene(btn) {
    var sel = document.getElementById('dd-scene-select');
    if (!sel || !sel.value) return;
    var scene = JSON.parse(sel.options[sel.selectedIndex].getAttribute('data-scene'));
    btn.disabled = true;
    api.controlDevice(dd.device, {
        sku: dd.info.sku,
        type: CAP.SCENE,
        instance: scene.instance,
        value: scene.value
    }).then(function() {
        btn.textContent = 'Applied';
    }).catch(function(err) {
        btn.textContent = 'Failed';
        console.error('Scene apply failed:', err.message);
    }).finally(function() {
        setTimeout(function() { btn.disabled = false; btn.textContent = 'Apply'; }, 2000);
    });
}

// ── Music mode (struct form) ─────────────────────────────────

function ddMusicWidget(c, p) {
    var fields = (p && p.fields) || [];
    var html = '';
    fields.forEach(function(f) {
        var fid = 'dd-music-' + f.fieldName;
        if (f.dataType === 'ENUM' && f.fieldName === 'autoColor') {
            html += ddControlRow('Auto Color', '<input type="checkbox" id="' + fid + '" class="settings-checkbox">');
        } else if (f.dataType === 'ENUM') {
            var opts = (f.options || []).map(function(o) {
                return '<option value="' + o.value + '">' + escapeHtml(o.name) + '</option>';
            }).join('');
            html += ddControlRow(humanizeInstance(f.fieldName), '<select id="' + fid + '" class="settings-select text-xs">' + opts + '</select>');
        } else if (f.dataType === 'INTEGER' && f.fieldName === 'rgb') {
            html += ddControlRow('Color', '<input type="color" id="' + fid + '" class="w-16 h-8 rounded cursor-pointer" style="background-color: #0f172a; border: 1px solid #475569;">');
        } else if (f.dataType === 'INTEGER') {
            var r = f.range || { min: 0, max: 100 };
            html += ddControlRow(humanizeInstance(f.fieldName),
                '<span class="text-xs mr-2" style="color: #e2e8f0;" id="' + fid + '-value">' + r.max + '</span>' +
                '<input type="range" id="' + fid + '" min="' + r.min + '" max="' + r.max + '" value="' + r.max + '"' +
                ' oninput="document.getElementById(\'' + fid + '-value\').textContent = this.value" style="width: 10rem; vertical-align: middle;">');
        }
    });
    html += '<div class="flex justify-end mt-2">' +
        '<button onclick="ddApplyMusicMode(this)" class="text-xs px-3 py-1.5 rounded transition-colors" ' +
        'style="background: rgba(59, 130, 246, 0.15); color: #60a5fa; border: 1px solid rgba(59, 130, 246, 0.3); cursor: pointer;"' +
        ' data-fields="' + escapeHtml(JSON.stringify(fields.map(function(f) { return { name: f.fieldName, dataType: f.dataType }; }))) + '">Apply Music Mode</button></div>';
    return html;
}

function ddApplyMusicMode(btn) {
    var fields = JSON.parse(btn.getAttribute('data-fields'));
    var value = {};
    fields.forEach(function(f) {
        var el = document.getElementById('dd-music-' + f.name);
        if (!el) return;
        if (f.name === 'autoColor') {
            value.autoColor = el.checked ? 1 : 0;
        } else if (el.type === 'color') {
            value[f.name] = hexToRgbInt(el.value);
        } else {
            value[f.name] = parseInt(el.value, 10) || 0;
        }
    });
    btn.disabled = true;
    api.controlDevice(dd.device, { sku: dd.info.sku, type: CAP.MUSIC, instance: 'musicMode', value: value })
        .then(function() { btn.textContent = 'Applied'; })
        .catch(function(err) { btn.textContent = 'Failed'; console.error(err.message); })
        .finally(function() {
            setTimeout(function() { btn.disabled = false; btn.textContent = 'Apply Music Mode'; }, 2000);
        });
}

// ── Shared control sender ────────────────────────────────────

function ddSendControl(el, value, onOk) {
    api.controlDevice(dd.device, {
        sku: dd.info.sku,
        type: el.getAttribute('data-cap-type'),
        instance: el.getAttribute('data-cap-instance'),
        value: value
    }).then(function() {
        if (onOk) onOk();
    }).catch(function(err) {
        console.error('Control failed:', err.message);
    });
}

function ddUnsupportedRow(c, note) {
    return ddControlRow(humanizeInstance(c.instance),
        '<span class="text-xs" style="color: #64748b;">' + note + '</span>');
}

// ── Header actions ───────────────────────────────────────────

function ddTestBlink(btn) {
    btn.disabled = true;
    btn.textContent = 'Blinking...';
    api.testDevice(dd.device, dd.info.sku).then(function() {
        btn.textContent = 'Sent';
    }).catch(function(err) {
        btn.textContent = 'Failed';
        console.error(err.message);
    }).finally(function() {
        setTimeout(function() { btn.disabled = false; btn.textContent = 'Test Blink'; }, 3000);
    });
}

function ddLoadAssignedSceneOptions(sel) {
    if (sel.getAttribute('data-loaded')) return;
    sel.setAttribute('data-loaded', '1');
    api.getDeviceScenes(dd.device, dd.info.sku).then(function(resp) {
        var scenes = (resp.data && resp.data.scenes) || [];
        sel.innerHTML = '<option value="">(none — restore last state)</option>';
        scenes.forEach(function(s) {
            var opt = document.createElement('option');
            opt.value = s.instance + '|' + s.name;
            opt.textContent = s.name + (s.instance === 'diyScene' ? ' (DIY)' : '');
            opt.setAttribute('data-scene', JSON.stringify(s));
            if (dd.assignedScene && dd.assignedScene.name === s.name && dd.assignedScene.instance === s.instance) {
                opt.selected = true;
            }
            sel.appendChild(opt);
        });
    }).catch(function(err) {
        sel.removeAttribute('data-loaded');
        console.error('Scene list failed:', err.message);
    });
}

function ddSaveAssignedScene(sel) {
    var body = {};
    if (sel.value) {
        var scene = JSON.parse(sel.options[sel.selectedIndex].getAttribute('data-scene'));
        body = { instance: scene.instance, name: scene.name, value: scene.value };
    }
    api.put('/api/v1/devices/' + encodeURIComponent(dd.device) + '/scene', body).then(function() {
        dd.assignedScene = sel.value ? body : null;
    }).catch(function(err) {
        console.error('Scene assignment failed:', err.message);
    });
}
