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
    api.getMappings().then(function(resp) {
        var el = document.getElementById('mappings-count');
        if (el && resp.data && resp.data.mappings) el.textContent = resp.data.mappings.length;
    }).catch(function() {});
    api.getTokens().then(function(resp) {
        var el = document.getElementById('tokens-count');
        if (el && resp.data && resp.data.tokens) el.textContent = resp.data.tokens.length;
    }).catch(function() {});
});
