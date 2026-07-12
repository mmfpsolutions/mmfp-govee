/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee — Settings page
'use strict';

document.addEventListener('DOMContentLoaded', function() {
    loadSettings();
});

function loadSettings() {
    api.getSettings().then(function(resp) {
        var s = resp.data || {};
        document.getElementById('settings-key-state').textContent =
            s.goveeApiKeySet ? '(a key is stored)' : '(no key yet — required)';
        document.getElementById('settings-calls-today').textContent =
            s.goveeCallsToday + ' of ' + s.goveeCallsLimit;
        document.getElementById('settings-web-port').textContent = s.webServerPort;
        document.getElementById('settings-hook-port').textContent = s.webhookPort;
        document.getElementById('settings-log-level').value = s.logLevel || 'info';
        document.getElementById('settings-auth-enabled').checked = !s.disableAuthentication;
        if (s.quietHours) {
            document.getElementById('settings-quiet-enabled').checked = !!s.quietHours.enabled;
            if (s.quietHours.start) document.getElementById('settings-quiet-start').value = s.quietHours.start;
            if (s.quietHours.end) document.getElementById('settings-quiet-end').value = s.quietHours.end;
        }
    }).catch(function(err) {
        showSettingsError('Could not load settings: ' + err.message);
    });
}

function showSettingsError(msg) {
    var el = document.getElementById('settings-error');
    el.textContent = msg;
    el.classList.remove('hidden');
    document.getElementById('settings-saved').classList.add('hidden');
}

function saveSettings() {
    var body = {
        disableAuthentication: !document.getElementById('settings-auth-enabled').checked,
        quietHours: {
            enabled: document.getElementById('settings-quiet-enabled').checked,
            start: document.getElementById('settings-quiet-start').value,
            end: document.getElementById('settings-quiet-end').value
        },
        logLevel: document.getElementById('settings-log-level').value
    };
    var key = document.getElementById('settings-govee-key').value.trim();
    if (key) body.goveeApiKey = key;

    var btn = document.getElementById('settings-save-btn');
    btn.disabled = true;
    btn.style.opacity = '0.6';

    api.updateSettings(body).then(function() {
        document.getElementById('settings-govee-key').value = '';
        var el = document.getElementById('settings-saved');
        el.textContent = 'Settings saved.';
        el.classList.remove('hidden');
        document.getElementById('settings-error').classList.add('hidden');
        loadSettings();
    }).catch(function(err) {
        showSettingsError(err.message);
    }).finally(function() {
        btn.disabled = false;
        btn.style.opacity = '';
    });
}
