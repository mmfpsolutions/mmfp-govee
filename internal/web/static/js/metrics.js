/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * Licensed under the MIT License. See LICENSE for details.
 */

// MMFP Govee Health & Metrics Page
// MMFP Ledger's metrics.js (itself GSSM's without charts), without the
// Database card, plus GSSM's Services card filled with Govee's services.
// Everything shown is Govee's own in-memory state: the page makes no Govee
// call, so it still works during a Govee outage. See
// design-documents/health/health-plan.md.
'use strict';

var METRICS_SECTION_KEY = 'govee-metrics-sections';

// ─── Init ───────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', function() {
    initMetricsSectionToggles();
    loadMetrics();
});

function loadMetrics() {
    api.getMetrics().then(function(resp) {
        renderMetrics((resp && resp.data) || {});
    }).catch(function(err) {
        document.getElementById('metrics-loading').innerHTML =
            '<p style="color: #ef4444;">Failed to load health snapshot: ' + escapeHtml(err.message) + '</p>';
    });
}

function renderMetrics(m) {
    renderRuntime(m.runtime || {});
    renderServices(m.services || {});
    renderMemory(m.memory || {});
    renderGC(m.gc || {});

    document.getElementById('metrics-loading').classList.add('hidden');
    document.getElementById('metrics-content-inner').classList.remove('hidden');
    restoreMetricsSectionStates();
}

// ─── Small builders ─────────────────────────────────────────────

function metricRow(label, valueHtml) {
    return '<div class="flex items-center justify-between text-sm">' +
        '<span style="color: #94a3b8;">' + escapeHtml(label) + '</span>' +
        '<span class="font-medium" style="color: #e2e8f0;">' + valueHtml + '</span>' +
        '</div>';
}

function metricTile(label, valueHtml) {
    return '<div class="stat-card">' +
        '<h3 class="text-sm font-medium mb-2" style="color: #94a3b8;">' + escapeHtml(label) + '</h3>' +
        '<div class="text-sm font-semibold" style="color: #e2e8f0;">' + valueHtml + '</div></div>';
}

// GSSM's, unchanged: green when ok, grey when off.
function statusDot(ok, onText, offText) {
    var color = ok ? '#22c55e' : '#64748b';
    return '<span style="color: ' + color + ';">● ' + (ok ? onText : offText) + '</span>';
}

// Red: a service that should be working and isn't (GSSM's services only
// ever needed on/off; Govee's can also be broken, which is not "off").
function faultDot(text, title) {
    return '<span style="color: #ef4444;"' + (title ? ' title="' + escapeHtml(title) + '"' : '') + '>● ' +
        escapeHtml(text) + '</span>';
}

function detail(text) {
    return '<span style="color: #94a3b8;"> · ' + escapeHtml(text) + '</span>';
}

// ─── Sections ───────────────────────────────────────────────────

function renderRuntime(rt) {
    setText('m-uptime', rt.uptime || '--');
    setText('m-goroutines', rt.goroutines != null ? formatNumber(rt.goroutines) : '--');
    setText('m-cpu', rt.numCPU != null ? String(rt.numCPU) : '--');
    setText('m-goversion', rt.goVersion || '--');
}

function renderServices(svc) {
    var html = '';

    // Webhook listener — the app's reason to exist. A bind failure keeps the
    // app running (no restart loop) and shows here instead.
    var wh = svc.webhook || {};
    html += metricRow('Webhook listener', wh.running
        ? statusDot(true, 'Running') + detail(':' + wh.port)
        : faultDot('Not running') + detail('could not bind :' + wh.port));

    // LAN transport — off is a choice (grey); enabled-but-not-running means
    // UDP 4002 could not be bound and every device is on the cloud (red).
    var lan = svc.lan || {};
    var lanHtml;
    if (lan.running) {
        lanHtml = statusDot(true, 'Running') + detail(formatNumber(lan.devices) + (lan.devices === 1 ? ' device' : ' devices'));
        var scanned = sensorAgeLabel(lan.lastScanSecs);
        if (scanned) lanHtml += detail('scanned ' + scanned);
    } else if (!lan.enabled) {
        lanHtml = statusDot(false, '', 'Off') + detail('disabled in config');
    } else {
        lanHtml = faultDot('Not running') + detail('cloud only');
    }
    html += metricRow('LAN transport', lanHtml);

    // Govee cloud — "Normal" means no run of outage failures, NOT a proof
    // that Govee answers (this page makes no call).
    var cloud = svc.cloud || {};
    var cloudHtml = cloud.paused
        ? faultDot('Paused — Govee not answering', cloud.lastError) +
            (cloud.retryInSecs ? detail('retrying in ' + cloud.retryInSecs + 's') : '')
        : statusDot(true, 'Normal');
    cloudHtml += detail(formatNumber(cloud.callsToday || 0) + ' / ' + formatNumber(cloud.callsLimit || 0) + ' calls today');
    html += metricRow('Govee cloud', cloudHtml);

    // Device catalog age — the same CachedAt the Devices page shows.
    var catalog = svc.catalog || {};
    var fetched = sensorAgeLabel(catalog.fetchedSecs);
    html += metricRow('Device catalog', fetched
        ? '<span style="color: #e2e8f0;">Fetched ' + escapeHtml(fetched) + '</span>'
        : '<span style="color: #64748b;">Not fetched yet</span>');

    document.getElementById('m-services').innerHTML = html;
}

function renderMemory(mem) {
    var html = '<div class="grid grid-cols-1 md:grid-cols-2 gap-4 py-3">';
    html += '<div class="space-y-2">';
    html += metricRow('In Use', formatBytes(mem.inUseBytes));
    html += metricRow('From OS', formatBytes(mem.fromOsBytes));
    html += metricRow('Heap Objects', mem.heapObjects != null ? formatNumber(mem.heapObjects) : '--');
    html += metricRow('Log File', formatBytes(mem.logFileBytes));
    html += '</div>';
    // What "From OS" is made of; the four add up to it.
    html += '<div class="space-y-2">';
    html += metricRow('Heap In Use', formatBytes(mem.heapInUse));
    html += metricRow('Heap Reserved', formatBytes(mem.heapReserved));
    html += metricRow('Stack', formatBytes(mem.stack));
    html += metricRow('Other', formatBytes(mem.other));
    html += '</div></div>';
    document.getElementById('body-m-memory').innerHTML = html;
}

function renderGC(gc) {
    var html = '<div class="grid grid-cols-2 md:grid-cols-4 gap-4 py-3">';
    html += metricTile('GC Cycles', gc.numGC != null ? formatNumber(gc.numGC) : '--');
    html += metricTile('Forced GC', gc.numForcedGC != null ? formatNumber(gc.numForcedGC) : '--');
    html += metricTile('Pause Total', (gc.pauseTotalMs != null ? gc.pauseTotalMs.toFixed(2) : '--') + ' ms');
    html += metricTile('GC CPU', (gc.cpuPercent != null ? gc.cpuPercent.toFixed(3) : '--') + '%');
    html += '</div>';
    document.getElementById('body-m-gc').innerHTML = html;
}

// ─── Section toggle (persisted) — mirrors GSSM / Ledger ─────────

function initMetricsSectionToggles() {
    // Shared click + iOS/PWA touch handler (see utils.js initSectionToggleHandler).
    initSectionToggleHandler('metrics', saveMetricsSectionStates);
}

function saveMetricsSectionStates() {
    var collapsed = [];
    var toggles = document.querySelectorAll('.metrics-section-toggle');
    for (var i = 0; i < toggles.length; i++) {
        var section = toggles[i].getAttribute('data-section');
        var body = document.getElementById('body-' + section);
        if (body && body.style.display === 'none') collapsed.push(section);
    }
    try { localStorage.setItem(METRICS_SECTION_KEY, JSON.stringify(collapsed)); } catch (e) {}
}

function restoreMetricsSectionStates() {
    var collapsed = [];
    try { collapsed = JSON.parse(localStorage.getItem(METRICS_SECTION_KEY)) || []; } catch (e) {}
    for (var i = 0; i < collapsed.length; i++) {
        var body = document.getElementById('body-' + collapsed[i]);
        var card = document.getElementById('section-' + collapsed[i]);
        if (body) body.style.display = 'none';
        if (card) {
            var arrow = card.querySelector('.metrics-section-arrow');
            if (arrow) arrow.style.transform = 'rotate(-90deg)';
        }
    }
}

function setText(id, text) {
    var el = document.getElementById(id);
    if (el) el.textContent = text;
}
