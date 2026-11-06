/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee — Sensors tab (list view).
// Columns: Pin | Status | Sensor Name | Model | Readings.
// Every read-only device in the Govee catalog. Readings are whatever each
// sensor declares (server-labelled); the star pins ONE sensor to the Devices
// page readout. Events-only sensors (the leak detector) are listed with no
// status and no readings: Govee's online flag for them is wrong (it said
// offline while the connectivity test passed, 2026-11-06).
// Plan: design-documents/sensors/sensors-plan.md
'use strict';

var sensorsState = {
    sensors: [],
    pinned: ''
};

document.addEventListener('DOMContentLoaded', function() {
    loadSensorsPage(false);
});

function loadSensorsPage(refresh) {
    return api.getSensors(refresh).then(function(resp) {
        sensorsState.sensors = (resp.data && resp.data.sensors) || [];
        sensorsState.pinned = (resp.data && resp.data.pinned) || '';
        renderSensors();
    }).catch(function(err) {
        document.getElementById('sensors-loading').classList.add('hidden');
        var el = document.getElementById('sensors-error');
        el.textContent = 'Could not load sensors: ' + err.message;
        el.classList.remove('hidden');
    });
}

function refreshSensors() {
    var btn = document.getElementById('sensors-refresh-btn');
    // Same spinner treatment as the Devices page Fetch icon.
    var icon = btn.querySelector('svg');
    btn.disabled = true;
    if (icon) icon.classList.add('animate-spin', '[animation-direction:reverse]');
    btn.title = 'Reading...';
    loadSensorsPage(true).finally(function() {
        btn.disabled = false;
        if (icon) icon.classList.remove('animate-spin', '[animation-direction:reverse]');
        btn.title = 'Read sensors now';
    });
}

function renderSensors() {
    document.getElementById('sensors-loading').classList.add('hidden');
    document.getElementById('sensors-error').classList.add('hidden');
    document.getElementById('sensors-content-inner').classList.remove('hidden');

    var sensors = sensorsState.sensors;
    var list = document.getElementById('sensors-list');
    var empty = document.getElementById('sensors-empty');
    var header = document.querySelector('.list-cols-sensors.list-header');

    if (!sensors.length) {
        list.innerHTML = '';
        header.classList.add('hidden');
        empty.style.display = 'block';
        document.getElementById('sensors-age').textContent = '';
        document.getElementById('sensors-pin-hint').style.display = 'none';
        return;
    }
    header.classList.remove('hidden');
    empty.style.display = 'none';

    // Header age = the OLDEST reading shown, so the page never claims to be
    // fresher than its stalest row.
    var oldest = null;
    sensors.forEach(function(s) {
        if (typeof s.ageSecs === 'number' && (oldest === null || s.ageSecs > oldest)) oldest = s.ageSecs;
    });
    var age = sensorAgeLabel(oldest);
    document.getElementById('sensors-age').textContent = age ? 'Last read ' + age : '';

    var pinnable = sensors.some(function(s) { return !s.eventsOnly; });
    document.getElementById('sensors-pin-hint').style.display =
        (pinnable && !sensorsState.pinned) ? 'block' : 'none';

    list.innerHTML = sensors.map(renderSensorRow).join('');
}

function renderSensorRow(s) {
    var muted = '<span style="color: #64748b;">&mdash;</span>';

    // Pin
    var pinCell;
    if (s.eventsOnly) {
        pinCell = '<div>' + muted + '</div>';
    } else {
        var isPinned = s.device === sensorsState.pinned;
        pinCell = '<div><button onclick="togglePin(\'' + escapeHtml(s.device) + '\')" ' +
            'title="' + (isPinned ? 'Pinned to the Devices page — click to unpin' : 'Pin to the Devices page') + '" ' +
            'aria-label="' + (isPinned ? 'Unpin' : 'Pin') + '" ' +
            'style="background: none; border: none; cursor: pointer; padding: 0; line-height: 1;">' +
            '<svg class="w-5 h-5" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor" ' +
                'fill="' + (isPinned ? 'currentColor' : 'none') + '" ' +
                'style="color: ' + (isPinned ? '#facc15' : '#64748b') + ';">' +
                '<path stroke-linejoin="round" d="M12 3l2.7 5.6 6.1.9-4.4 4.3 1 6.1L12 17l-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z"/>' +
            '</svg></button></div>';
    }

    // Status — events-only and never-read sensors show nothing rather than a
    // guess (a false "Offline" on a working leak detector is the one cue this
    // tab must not give).
    var statusCell;
    if (s.online === true) {
        statusCell = '<div class="text-xs" style="color: #4ade80;">&#9679; Online</div>';
    } else if (s.online === false) {
        statusCell = '<div class="text-xs" style="color: #64748b;">&#9679; Offline</div>';
    } else {
        statusCell = '<div class="text-xs">' + muted + '</div>';
    }

    // Readings
    var readings;
    if (s.eventsOnly) {
        readings = '<span class="italic" style="color: #64748b;">No readings</span>';
    } else if (s.readings.length) {
        // An offline sensor keeps its last value, dimmed.
        readings = formatSensorReadings(s.readings, s.online ? '#e2e8f0' : '#64748b');
    } else {
        readings = '<span style="color: #64748b;">--</span>';
    }
    if (s.lastError) {
        readings += ' <span class="text-xs" style="color: #f87171;" title="' + escapeHtml(s.lastError) + '">read failed</span>';
    }

    return '<div class="list-row list-cols-sensors" style="cursor: default;">' +
        pinCell +
        statusCell +
        '<div class="text-sm text-white" title="' + escapeHtml(s.deviceName) + '">' + escapeHtml(s.deviceName) + '</div>' +
        '<div class="sensors-col-model text-xs" style="color: #94a3b8;">' + escapeHtml(s.sku) + '</div>' +
        '<div class="text-sm">' + readings + '</div>' +
    '</div>';
}

function togglePin(device) {
    var next = device === sensorsState.pinned ? '' : device;
    api.setPinnedSensor(next).then(function() {
        sensorsState.pinned = next;
        renderSensors();
    }).catch(function(err) {
        var el = document.getElementById('sensors-error');
        el.textContent = 'Could not save the pinned sensor: ' + err.message;
        el.classList.remove('hidden');
    });
}
