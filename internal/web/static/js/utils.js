/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
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
