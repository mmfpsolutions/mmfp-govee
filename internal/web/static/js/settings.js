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

        // Quiet-hours diagnostics. Showing the APP's clock (not the browser's)
        // is the point: a container running UTC evaluated a 22:00-07:00 window
        // hours off and fired alerts at 5am with no visible clue.
        var t = document.getElementById('settings-app-time');
        t.textContent = (s.appTime || '--') + '  ' + (s.appTimezone || '');
        var qn = document.getElementById('settings-quiet-now');
        if (!s.quietHoursNow && s.quietHoursDesc === 'disabled') {
            qn.textContent = 'no — quiet hours disabled';
            qn.style.color = '#64748b';
        } else if (s.quietHoursNow) {
            qn.textContent = 'YES — effects are suppressed';
            qn.style.color = '#a78bfa';
        } else {
            qn.textContent = 'no — outside ' + (s.quietHoursDesc || 'the window');
            qn.style.color = '#94a3b8';
        }

        // LAN Control status. "Running" means the UDP socket is bound and
        // scanning; discovering 0 devices is normal and harmless (everything
        // falls back to the cloud API).
        var lanStatus = document.getElementById('lan-status');
        if (!s.lanEnabled) {
            lanStatus.textContent = 'disabled in config';
            lanStatus.style.color = '#64748b';
        } else if (s.lanRunning) {
            lanStatus.textContent = 'running';
            lanStatus.style.color = '#4ade80';
        } else {
            lanStatus.textContent = 'not running (UDP port unavailable)';
            lanStatus.style.color = '#facc15';
        }
        document.getElementById('lan-discovered').textContent =
            s.lanDiscovered + (s.lanDiscovered === 1 ? ' device' : ' devices');
        document.getElementById('lan-last-scan').textContent =
            s.lanLastScan ? new Date(s.lanLastScan * 1000).toLocaleString() : 'never';
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

// Manual LAN re-scan — the feedback loop after enabling LAN Control on a
// device in the Govee Home app. Free: one multicast packet.
function rescanLAN(btn) {
    btn.disabled = true;
    btn.textContent = 'Scanning...';
    api.post('/api/v1/lan/rescan').then(function(resp) {
        var n = (resp.data && resp.data.discovered) || 0;
        btn.textContent = n + ' found';
        loadSettings();
    }).catch(function(err) {
        btn.textContent = 'Failed';
        showSettingsError('LAN re-scan failed: ' + err.message);
    }).finally(function() {
        setTimeout(function() { btn.disabled = false; btn.textContent = 'Re-scan'; }, 2500);
    });
}
