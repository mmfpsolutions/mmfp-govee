/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee — Activity page
'use strict';

// Device ID → friendly name, from the (server-cached) device list. Loaded
// once; rows fall back to the raw ID until it lands / for unknown devices.
var activityDeviceNames = {};

document.addEventListener('DOMContentLoaded', function() {
    api.getDevices().then(function(resp) {
        ((resp.data && resp.data.devices) || []).forEach(function(d) {
            activityDeviceNames[d.device] = d.deviceName;
        });
    }).catch(function() { /* names stay as IDs */ }).finally(function() {
        loadActivity();
    });
    // Light auto-refresh while the page is visible
    setInterval(function() {
        if (!document.hidden) loadActivity();
    }, 15000);
});

function deviceNames(ids) {
    if (!ids || !ids.length) return '';
    return ids.map(function(id) {
        return escapeHtml(activityDeviceNames[id] || id);
    }).join(', ');
}

function resultColor(result) {
    if (result === 'effect ok' || result === 'queued') return '#4ade80';
    if (result === 'cooldown' || result === 'quiet hours' || result === 'no match') return '#facc15';
    if (result === 'rejected' || result.indexOf('effect failed') === 0) return '#f87171';
    return '#94a3b8';
}

function loadActivity() {
    api.getActivity(100).then(function(resp) {
        var records = (resp.data && resp.data.activity) || [];
        var rows = document.getElementById('activity-rows');
        var empty = document.getElementById('activity-empty');

        if (!records.length) {
            rows.innerHTML = '';
            empty.classList.remove('hidden');
            return;
        }
        empty.classList.add('hidden');

        rows.innerHTML = records.map(function(r) {
            return '<tr style="border-bottom: 1px solid rgba(51, 65, 85, 0.3);">' +
                '<td class="py-2 px-3 whitespace-nowrap" style="color: #94a3b8;">' + escapeHtml(formatTimestamp(r.timestamp)) + '</td>' +
                '<td class="py-2 px-3" style="color: #e2e8f0;">' + escapeHtml(r.tokenName || '-') + '</td>' +
                '<td class="py-2 px-3" style="color: #94a3b8;">' + escapeHtml(r.source || '-') + '</td>' +
                '<td class="py-2 px-3" style="color: #60a5fa;">' + escapeHtml(r.event || '-') + '</td>' +
                '<td class="py-2 px-3" style="color: #94a3b8;">' + escapeHtml(r.entity || '') + '</td>' +
                '<td class="py-2 px-3" style="color: #e2e8f0;">' + escapeHtml(r.mappingName || '') + '</td>' +
                '<td class="py-2 px-3" style="color: #94a3b8;">' + deviceNames(r.devices) + '</td>' +
                '<td class="py-2 px-3" style="color: ' + resultColor(r.result) + ';">' + escapeHtml(r.result) + '</td>' +
            '</tr>';
        }).join('');
    }).catch(function(err) {
        console.error('Activity load failed:', err.message);
    });
}
