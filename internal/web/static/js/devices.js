/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee — Devices dashboard (list view).
// Columns: Status (clickable power icon) | Device Name | Model | Device ID |
// LAN Control.
// The device name links to the controller page. Status doubles as the power
// control: green=on, red=off,
// gray=offline; state comes from a per-device sweep loaded AFTER the rows
// render (one Govee call per device — manual refresh only). Test Blink lives
// on the device controller page.
'use strict';

document.addEventListener('DOMContentLoaded', function() {
    loadDevices(false);
});

function loadDevices(refresh) {
    var call = refresh ? api.refreshDevices() : api.getDevices();
    call.then(function(resp) {
        renderDevices(resp.data || {});
        // Sensors load AFTER the sweep resolves, never alongside it. The
        // power icons are what the page is for; the pinned sensor is a
        // bonus and must not compete with them for the Govee client's global
        // call spacing.
        loadStatuses().then(loadSensors);
    }).catch(function(err) {
        showDevicesError(err.message);
    });
}

function refreshDevices() {
    var btn = document.getElementById('devices-refresh-btn');
    // The icon IS the progress indicator — don't touch textContent, that
    // would wipe out the inline SVG. Tailwind's animate-spin goes on the
    // <svg>, not the button, so the spin doesn't drag the tooltip with it.
    //
    // animate-spin turns clockwise, which runs AGAINST the arrowheads on this
    // glyph. Tailwind has no reverse-spin utility, so the direction comes from
    // an arbitrary property rather than a hand-rolled keyframe.
    var icon = btn.querySelector('svg');
    btn.disabled = true;
    if (icon) icon.classList.add('animate-spin', '[animation-direction:reverse]');
    btn.title = 'Fetching...';
    api.refreshDevices().then(function(resp) {
        renderDevices(resp.data || {});
        // RETURNED so the spinner keeps turning until the statuses and the
        // readout have actually landed — stopping it when the device list
        // alone came back would claim the page was done while power icons
        // were still blank.
        return loadStatuses().then(loadSensors);
    }).catch(function(err) {
        showDevicesError(err.message);
    }).finally(function() {
        btn.disabled = false;
        if (icon) icon.classList.remove('animate-spin', '[animation-direction:reverse]');
        btn.title = 'Fetch from Govee';
    });
}

function showDevicesError(msg) {
    document.getElementById('devices-loading').classList.add('hidden');
    var el = document.getElementById('devices-error');
    el.textContent = 'Could not load devices: ' + msg;
    el.classList.remove('hidden');
    document.getElementById('devices-content-inner').classList.remove('hidden');
}

// Device IDs contain colons — not valid inside an element id.
function cssSafeDevice(device) {
    return device.replace(/[^A-Za-z0-9]/g, '_');
}

// The status cell IS the power control: a power-symbol button.
// green = on (click → off) · red = off (click → on) · gray = offline/unknown.
var POWER_ICON =
    '<svg class="w-5 h-5" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">' +
    '<path stroke-linecap="round" d="M12 3v9"/>' +
    '<path stroke-linecap="round" d="M18.36 6.64a9 9 0 1 1-12.72 0"/></svg>';

function paintPowerIcon(btn, state) {
    // state: "on" | "off" | "offline" | "unknown"
    var colors = {
        on:      { color: '#4ade80', title: 'On — click to turn off' },
        off:     { color: '#f87171', title: 'Off — click to turn on' },
        offline: { color: '#64748b', title: 'Offline' },
        unknown: { color: '#475569', title: 'State unknown — click to turn on' }
    };
    var c = colors[state] || colors.unknown;
    btn.setAttribute('data-state', state);
    btn.style.color = c.color;
    btn.title = c.title;
    btn.disabled = (state === 'offline');
    btn.style.cursor = (state === 'offline') ? 'not-allowed' : 'pointer';
}

function renderDevices(data) {
    document.getElementById('devices-loading').classList.add('hidden');
    document.getElementById('devices-error').classList.add('hidden');
    document.getElementById('devices-content-inner').classList.remove('hidden');

    var devices = data.devices || [];
    var countEl = document.getElementById('devices-count');
    if (countEl) countEl.textContent = devices.length;

    var cachedEl = document.getElementById('devices-cached-at');
    if (cachedEl && data.cachedAt) {
        cachedEl.textContent = 'Fetched ' + new Date(data.cachedAt * 1000).toLocaleString();
    }

    var list = document.getElementById('devices-list');
    var empty = document.getElementById('devices-empty');
    if (!devices.length) {
        list.innerHTML = '';
        empty.classList.remove('hidden');
        return;
    }
    empty.classList.add('hidden');

    list.innerHTML = devices.map(function(d) {
        var safe = cssSafeDevice(d.device);
        var detailHref = '/devices/' + encodeURIComponent(d.device) + '/details';

        var statusCell = d.controllable
            ? '<div><button id="status-' + safe + '" data-state="unknown" ' +
                'data-device="' + escapeHtml(d.device) + '" data-sku="' + escapeHtml(d.sku) + '" ' +
                'onclick="statusPowerClicked(this)" title="Loading state..." ' +
                'style="background: none; border: none; padding: 2px; color: #475569; cursor: pointer;">' + POWER_ICON + '</button></div>'
            : '<div><span class="text-xs" style="color: #475569;">&mdash;</span></div>';

        var nameCell = d.controllable
            ? '<div><a href="' + detailHref + '" class="hover:underline text-sm font-medium" style="color: #e2e8f0; text-decoration: none;">' + escapeHtml(d.deviceName) + '</a>' +
              (d.assignedScene ? '<div class="text-xs" style="color: #64748b;">scene: ' + escapeHtml(d.assignedScene.name) + '</div>' : '') + '</div>'
            : '<div class="text-sm" style="color: #94a3b8;">' + escapeHtml(d.deviceName) + '</div>';

        // LAN Control: green check = served over UDP (fast, free, offline).
        // Blank = cloud. Toggle LAN Control for a device in the Govee Home app,
        // then hit Fetch and the check appears.
        var lanCell = d.lanControl
            ? '<div title="LAN Control active' + (d.lanIP ? ' — ' + escapeHtml(d.lanIP) : '') + '">' +
                '<svg class="w-5 h-5" style="color: #4ade80;" fill="none" stroke="currentColor" stroke-width="2.5" viewBox="0 0 24 24">' +
                '<path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/></svg></div>'
            : '<div><span class="text-xs" style="color: #475569;" title="Served by the Govee cloud API">&mdash;</span></div>';

        return '<div class="list-row list-cols-devices" style="cursor: default;">' +
            statusCell +
            nameCell +
            '<div class="devices-col-model text-xs" style="color: #94a3b8;">' + escapeHtml(d.sku) + '</div>' +
            '<div class="devices-col-id text-xs" style="color: #64748b; font-family: monospace; overflow: hidden; text-overflow: ellipsis;">' + escapeHtml(d.device) + '</div>' +
            lanCell +
        '</div>';
    }).join('');
}

// ── Pinned sensor readout (one sensor, chosen on the Sensors tab) ──

// Shows only the PINNED sensor; every sensor lives on the Sensors tab. The
// readout links there. Hidden when nothing is pinned.
function loadSensors() {
    // Returns the promise so the Fetch spinner can wait on it too.
    return api.getPinnedSensor().then(function(resp) {
        var s = resp.data && resp.data.sensor;
        var box = document.getElementById('devices-sensors');
        if (!box) return;
        if (!s) {
            box.style.display = 'none';
            return;
        }
        var value = (s.online && s.readings.length)
            ? formatSensorReadings(s.readings, '#e2e8f0')
            : '<span style="color: #e2e8f0; font-weight: 600;">--</span>';
        var age = sensorAgeLabel(s.ageSecs);
        box.innerHTML = '<a href="/sensors" class="text-sm hover:underline" style="color: #94a3b8;" title="' +
            escapeHtml(s.deviceName) + ' — all sensors">' +
            escapeHtml(shortSensorName(s.deviceName)) + ' ' + value +
            (age ? ' <span class="text-xs" style="color: #64748b;">(' + age + ')</span>' : '') +
            '</a>';
        box.style.display = 'flex';
    }).catch(function(err) {
        console.error('Sensor read failed:', err.message);
    });
}

// "Pool Thermometer" → "Pool", "Garage Thermo-Hygrometer" → "Garage". The
// device name is already in the tooltip, and the header has room for a value,
// not a sentence.
function shortSensorName(name) {
    return String(name)
        .replace(/\s*(thermo-hygrometer|hygrometer|thermometer|sensor|monitor|detector)\b.*$/i, '')
        .trim() || name;
}

// ── Status sweep (one state read per device, after rows render) ──

function loadStatuses() {
    // Returns the promise so callers can queue work after the sweep.
    return api.getDevicesStatus().then(function(resp) {
        var statuses = (resp.data && resp.data.statuses) || {};
        Object.keys(statuses).forEach(function(device) {
            applyStatus(device, statuses[device]);
        });
        // Devices absent from the response (read failed) stay "unknown"
        document.querySelectorAll('[id^="status-"]').forEach(function(btn) {
            if (btn.title === 'Loading state...') {
                paintPowerIcon(btn, 'unknown');
            }
        });
    }).catch(function(err) {
        console.error('Status sweep failed:', err.message);
    });
}

function applyStatus(device, st) {
    var btn = document.getElementById('status-' + cssSafeDevice(device));
    if (!btn) return;
    if (!st.online) {
        paintPowerIcon(btn, 'offline');
    } else if (st.powerOn === 1) {
        paintPowerIcon(btn, 'on');
    } else if (st.powerOn === 0) {
        paintPowerIcon(btn, 'off');
    } else {
        paintPowerIcon(btn, 'unknown');
    }
}

// Clicking the status icon toggles power: on → off, off/unknown → on.
// Offline is disabled (nothing to send to).
function statusPowerClicked(btn) {
    var state = btn.getAttribute('data-state');
    if (state === 'offline') return;
    var next = state === 'on' ? 0 : 1;
    api.controlDevice(btn.getAttribute('data-device'), {
        sku: btn.getAttribute('data-sku'),
        type: 'devices.capabilities.on_off',
        instance: 'powerSwitch',
        value: next
    }).then(function() {
        paintPowerIcon(btn, next === 1 ? 'on' : 'off');
    }).catch(function(err) {
        console.error('Power toggle failed:', err.message);
    });
}
