/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee header — nav counts + logout
'use strict';

async function handleLogout() {
    try {
        await api.post('/api/v1/auth/logout');
    } catch (e) {}
    window.location.href = '/login';
}

// Fill the Devices / Mappings / Tokens tab counts (best-effort)
document.addEventListener('DOMContentLoaded', function() {
    // The Devices count used to be set ONLY by devices.js, so the badge
    // vanished on every other tab while Mappings and Tokens kept theirs.
    //
    // This reads the cached device catalog (/api/v1/devices), which is served
    // from memory or disk — NOT /api/v1/devices/status, which fans a live
    // state read out to every device and would put seconds of Govee latency
    // on every page load in the app.
    api.getDevices().then(function(resp) {
        var el = document.getElementById('devices-count');
        if (el && resp.data && resp.data.devices) el.textContent = resp.data.devices.length;
    }).catch(function() {});
    api.getMappings().then(function(resp) {
        var el = document.getElementById('mappings-count');
        if (el && resp.data && resp.data.mappings) el.textContent = resp.data.mappings.length;
    }).catch(function() {});
    api.getTokens().then(function(resp) {
        var el = document.getElementById('tokens-count');
        if (el && resp.data && resp.data.tokens) el.textContent = resp.data.tokens.length;
    }).catch(function() {});
});
