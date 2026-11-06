/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * Licensed under the MIT License. See LICENSE for details.
 */

// MMFP Govee shared utilities
'use strict';

function escapeHtml(str) {
    if (str === null || str === undefined) return '';
    return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

// 24-bit RGB int → "#rrggbb" (color inputs / swatches)
function rgbIntToHex(n) {
    n = Number(n) || 0;
    return '#' + n.toString(16).padStart(6, '0');
}

// "#rrggbb" → 24-bit RGB int
function hexToRgbInt(hex) {
    return parseInt(String(hex).replace('#', ''), 16) || 0;
}

function formatTimestamp(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return String(iso);
    return d.toLocaleString();
}

// Small color swatch span for tables/cards
function colorSwatch(rgbInt) {
    return '<span style="display:inline-block; width:0.9rem; height:0.9rem; border-radius:3px; vertical-align:-2px; ' +
        'border:1px solid rgba(148,163,184,0.4); background:' + rgbIntToHex(rgbInt) + ';"></span>';
}

// ── Sensors (shared by the Sensors tab and the Devices-page readout) ──

// How long ago the server actually asked Govee for a reading. Readings are
// served from a 5-minute cache, so without this a minutes-old value looks
// live. Rendered from the server's age, not a browser clock, so a wrong client
// time can't make a fresh reading look old.
function sensorAgeLabel(secs) {
    if (typeof secs !== 'number' || secs < 0) return '';
    if (secs < 60) return 'just now';
    if (secs < 3600) return Math.floor(secs / 60) + 'm ago';
    return Math.floor(secs / 3600) + 'h ago';
}

// One reading as display text: "81.0°F", "41%". The server owns labels,
// units and precision; a value Govee sent in a non-number shape arrives as
// `raw` and is shown as-is rather than hidden.
function formatSensorReading(r) {
    if (r.value === undefined || r.value === null) return r.raw ? String(r.raw) : '--';
    var text = (typeof r.decimals === 'number') ? r.value.toFixed(r.decimals) : String(r.value);
    return text + (r.unit || '');
}

// All of a sensor's readings as escaped HTML, "81.0°F · 41%". Each value
// carries its label as a tooltip.
function formatSensorReadings(readings, valueColor) {
    return (readings || []).map(function(r) {
        return '<span title="' + escapeHtml(r.label) + '" style="color: ' + valueColor + '; font-weight: 600;">' +
            escapeHtml(formatSensorReading(r)) + '</span>';
    }).join('<span style="color: #64748b;"> &middot; </span>');
}

// ── Health page helpers (copied unchanged from MMFP Ledger / GSSM utils.js) ──


/**
 * Format number with thousands separators
 */
function formatNumber(num) {
    if (num === undefined || num === null) return '--';
    return Number(num).toLocaleString();
}

/**
 * Format a byte count to human-readable (B/KB/MB/GB/TB).
 */
function formatBytes(bytes) {
    if (bytes === undefined || bytes === null) return '--';
    var units = ['B', 'KB', 'MB', 'GB', 'TB'];
    var i = 0;
    var val = bytes;
    while (val >= 1024 && i < units.length - 1) { val /= 1024; i++; }
    return val.toFixed(i === 0 ? 0 : 2) + ' ' + units[i];
}

/**
 * Wire the collapsible section-toggle headers used by the config-style pages
 * (Config / Notifications / Historicals / Health). All four share an identical
 * structure — a `.<prefix>-section-toggle` header with `data-section`, a body
 * `#body-<section>`, and a `.<prefix>-section-arrow` chevron — so this handles
 * the toggle + persistence wiring for all of them.
 *
 * Mirrors MIM's fix: a delegated `click` on a <div> is unreliable in iOS/PWA —
 * the browser's double-tap-zoom disambiguation delays or swallows it (or fires a
 * zoom), so taps feel hit-or-miss and need a second press. We ALSO handle the
 * tap on `touchend` and `preventDefault()` it, which suppresses the zoom, the
 * ~300ms delay, and the synthesized ghost click. An 8px movement threshold keeps
 * page scrolling working (a drag that starts on a header is a scroll, not a tap),
 * and a 300ms per-header debounce stops a touchend + ghost-click double-toggle.
 *
 * @param {string} prefix   e.g. 'config' → .config-section-toggle / -arrow
 * @param {function} onSave  persists collapsed state (page-specific)
 */
function initSectionToggleHandler(prefix, onSave) {
    var toggleSel = '.' + prefix + '-section-toggle';
    var arrowSel = '.' + prefix + '-section-arrow';

    function doToggle(toggle) {
        // Debounce: a single tap can deliver both touchend (handled) and a
        // synthesized click — preventDefault doesn't always suppress the latter
        // in every PWA context, so guard against a double-fire per header.
        var now = Date.now();
        if (now - (toggle._lastTap || 0) < 300) return;
        toggle._lastTap = now;

        var body = document.getElementById('body-' + toggle.getAttribute('data-section'));
        if (!body) return;
        var arrow = toggle.querySelector(arrowSel);
        var isHidden = body.style.display === 'none';
        body.style.display = isHidden ? '' : 'none';
        if (arrow) arrow.style.transform = isHidden ? '' : 'rotate(-90deg)';
        if (onSave) onSave();
    }

    // Desktop / fallback wherever a click arrives without a preceding touch.
    document.addEventListener('click', function(e) {
        var toggle = e.target.closest(toggleSel);
        if (toggle) doToggle(toggle);
    });

    // Touch (iOS / PWA).
    var sx = 0, sy = 0, moved = false, onHdr = false;
    document.addEventListener('touchstart', function(e) {
        onHdr = !!e.target.closest(toggleSel);
        if (!onHdr) return;
        moved = false;
        var t = e.touches[0];
        sx = t.clientX; sy = t.clientY;
    }, { passive: true });
    document.addEventListener('touchmove', function(e) {
        if (!onHdr) return;
        var t = e.touches[0];
        if (Math.abs(t.clientX - sx) > 8 || Math.abs(t.clientY - sy) > 8) moved = true;
    }, { passive: true });
    document.addEventListener('touchend', function(e) {
        if (!onHdr) return;
        onHdr = false;
        if (moved) return; // it was a scroll, not a tap
        var toggle = e.target.closest(toggleSel);
        if (!toggle) return;
        e.preventDefault(); // kill zoom + ghost click; we own this tap
        doToggle(toggle);
    });
}
