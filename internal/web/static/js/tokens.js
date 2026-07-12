/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee — Tokens page
'use strict';

document.addEventListener('DOMContentLoaded', function() {
    loadTokens();
});

function loadTokens() {
    api.getTokens().then(function(resp) {
        renderTokens((resp.data && resp.data.tokens) || []);
    }).catch(function(err) {
        document.getElementById('tokens-list').innerHTML =
            '<p style="color: #f87171;">Could not load tokens: ' + escapeHtml(err.message) + '</p>';
    });
}

function renderTokens(tokens) {
    var list = document.getElementById('tokens-list');
    var empty = document.getElementById('tokens-empty');

    var countEl = document.getElementById('tokens-count');
    if (countEl) countEl.textContent = tokens.length;

    if (!tokens.length) {
        list.innerHTML = '';
        empty.classList.remove('hidden');
        return;
    }
    empty.classList.add('hidden');

    list.innerHTML = tokens.map(function(t) {
        return '<div class="device-card px-5 py-4">' +
            '<div class="flex items-center justify-between">' +
                '<div class="flex items-center space-x-4">' +
                    '<span class="device-card-name">' + escapeHtml(t.name) + '</span>' +
                    '<span class="text-sm" style="color: #64748b; font-family: monospace;">' + escapeHtml(t.masked) + '</span>' +
                    '<span class="text-xs" style="color: #64748b;">' + t.inUse + ' mapping(s)</span>' +
                '</div>' +
                '<button onclick="confirmRevokeToken(\'' + escapeHtml(t.name) + '\')" ' +
                    'class="text-xs px-3 py-1.5 rounded transition-colors" ' +
                    'style="background: rgba(239, 68, 68, 0.1); color: #f87171; border: 1px solid rgba(239, 68, 68, 0.3); cursor: pointer;">Revoke</button>' +
            '</div>' +
        '</div>';
    }).join('');
}

// ── Create modal ─────────────────────────────────────────────

function openTokenCreateModal() {
    var content = document.getElementById('token-create-modal-content');
    document.getElementById('token-create-modal-title').textContent = 'New Token';
    content.innerHTML =
        '<div id="token-create-error" class="hidden mb-4 p-3 rounded-lg text-sm" ' +
            'style="background: rgba(239, 68, 68, 0.15); border: 1px solid rgba(239, 68, 68, 0.3); color: #f87171;"></div>' +
        '<div class="mb-4">' +
            '<label class="block text-sm mb-1.5" style="color: #94a3b8;">Name</label>' +
            '<input type="text" id="token-create-name" class="settings-input w-64" placeholder="gss-main">' +
            '<p class="text-xs mt-1" style="color: #64748b;">One token per caller — e.g. gss-main, gssm-home.</p>' +
        '</div>' +
        '<div class="flex justify-end space-x-3">' +
            '<button onclick="closeTokenCreateModal()" class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                'style="background: rgba(51, 65, 85, 0.5); color: #e2e8f0; border: 1px solid rgba(71, 85, 105, 0.5); cursor: pointer;">Cancel</button>' +
            '<button onclick="createToken()" class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                'style="background: linear-gradient(90deg, #3b82f6, #2563eb); color: white; border: none; cursor: pointer;">Create</button>' +
        '</div>';

    var modal = document.getElementById('token-create-modal');
    modal.classList.remove('hidden');
    modal.style.display = 'flex';
    setTimeout(function() {
        var input = document.getElementById('token-create-name');
        if (input) input.focus();
    }, 50);
}

function closeTokenCreateModal() {
    var modal = document.getElementById('token-create-modal');
    if (modal) {
        modal.classList.add('hidden');
        modal.style.display = 'none';
    }
}

function createToken() {
    var name = document.getElementById('token-create-name').value.trim();
    api.createToken(name).then(function(resp) {
        var secret = resp.data && resp.data.secret;
        var url = window.location.protocol + '//' + window.location.hostname + ':8787/hook';
        // Show the secret ONCE with copy-paste GSS/GSSM setup instructions.
        document.getElementById('token-create-modal-title').textContent = 'Token Created';
        document.getElementById('token-create-modal-content').innerHTML =
            '<p class="text-sm mb-3" style="color: #94a3b8;">Copy the secret now — it is shown <span class="text-white font-semibold">only once</span> and stored encrypted.</p>' +
            '<div class="mb-4 p-3 rounded-lg" style="background-color: #0f172a; border: 1px solid #475569;">' +
                '<div class="text-xs mb-1" style="color: #64748b;">Secret</div>' +
                '<div class="text-sm text-white" style="font-family: monospace; word-break: break-all;" id="token-secret-value">' + escapeHtml(secret) + '</div>' +
            '</div>' +
            '<div class="mb-4 p-3 rounded-lg" style="background-color: #0f172a; border: 1px solid #475569;">' +
                '<div class="text-xs mb-1" style="color: #64748b;">GSSM setup (its webhook form only takes a URL — token rides the URL)</div>' +
                '<div class="text-xs" style="color: #94a3b8; font-family: monospace; word-break: break-all;">' +
                    escapeHtml(url) + '?token=' + escapeHtml(secret) +
                '</div>' +
            '</div>' +
            '<div class="mb-4 p-3 rounded-lg" style="background-color: #0f172a; border: 1px solid #475569;">' +
                '<div class="text-xs mb-1" style="color: #64748b;">Callers that can set headers (keeps the token out of URLs/logs)</div>' +
                '<div class="text-xs" style="color: #94a3b8; font-family: monospace; word-break: break-all;">' +
                    'URL: ' + escapeHtml(url) + '<br>' +
                    'Header: X-MMFP-Token: ' + escapeHtml(secret) +
                '</div>' +
                '<p class="text-xs mt-2" style="color: #64748b;">Adjust the hostname/port if this machine is reached differently from your GSS/GSSM host.</p>' +
                '<p class="text-xs mt-1" style="color: #64748b;">Note: the senders’ Test buttons emit fixed events — GSS sends <span style="font-family: monospace;">startup</span>, GSSM sends <span style="font-family: monospace;">test</span>. They return 200 here but only fire lights if a mapping includes that event. Use a mapping’s own Test button to preview its effect.</p>' +
            '</div>' +
            '<div class="flex justify-end space-x-3">' +
                '<button onclick="copyTokenSecret(this)" class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                    'style="background: rgba(51, 65, 85, 0.5); color: #e2e8f0; border: 1px solid rgba(71, 85, 105, 0.5); cursor: pointer;">Copy Secret</button>' +
                '<button onclick="closeTokenCreateModal(); loadTokens();" class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                    'style="background: linear-gradient(90deg, #3b82f6, #2563eb); color: white; border: none; cursor: pointer;">Done</button>' +
            '</div>';
    }).catch(function(err) {
        var el = document.getElementById('token-create-error');
        if (el) {
            el.textContent = err.message;
            el.classList.remove('hidden');
        }
    });
}

function copyTokenSecret(btn) {
    var el = document.getElementById('token-secret-value');
    if (!el) return;
    navigator.clipboard.writeText(el.textContent).then(function() {
        btn.textContent = 'Copied';
        setTimeout(function() { btn.textContent = 'Copy Secret'; }, 2000);
    });
}

// ── Revoke confirm modal ─────────────────────────────────────

function closeTokenConfirmModal() {
    var modal = document.getElementById('token-confirm-modal');
    if (modal) {
        modal.classList.add('hidden');
        modal.style.display = 'none';
    }
}

function confirmRevokeToken(name) {
    var title = document.getElementById('token-confirm-modal-title');
    var content = document.getElementById('token-confirm-modal-content');
    if (title) title.textContent = 'Revoke ' + name;
    if (content) {
        content.innerHTML =
            '<p style="color: #94a3b8;" class="mb-4">Revoke token <span class="text-white font-semibold">' + escapeHtml(name) + '</span>?</p>' +
            '<p style="color: #64748b;" class="text-sm mb-6">Webhooks presenting this secret are rejected immediately. Mappings referencing it must be repointed first.</p>' +
            '<div class="flex justify-end space-x-3">' +
                '<button onclick="closeTokenConfirmModal()" ' +
                    'class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                    'style="background: rgba(51, 65, 85, 0.5); color: #e2e8f0; border: 1px solid rgba(71, 85, 105, 0.5); cursor: pointer;">Cancel</button>' +
                '<button onclick="revokeToken(\'' + escapeHtml(name) + '\')" ' +
                    'class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                    'style="background: rgba(239, 68, 68, 0.15); color: #f87171; border: 1px solid rgba(239, 68, 68, 0.3); cursor: pointer;">Revoke</button>' +
            '</div>';
    }
    var modal = document.getElementById('token-confirm-modal');
    modal.classList.remove('hidden');
    modal.style.display = 'flex';
}

function revokeToken(name) {
    api.deleteToken(name).then(function() {
        closeTokenConfirmModal();
        loadTokens();
    }).catch(function(err) {
        var content = document.getElementById('token-confirm-modal-content');
        if (content) {
            content.innerHTML =
                '<p style="color: #f87171;" class="mb-4">' + escapeHtml(err.message) + '</p>' +
                '<div class="flex justify-end">' +
                    '<button onclick="closeTokenConfirmModal()" class="px-4 py-2 rounded text-sm font-medium transition-colors" ' +
                        'style="background: rgba(51, 65, 85, 0.5); color: #e2e8f0; border: 1px solid rgba(71, 85, 105, 0.5); cursor: pointer;">Close</button>' +
                '</div>';
        }
    });
}
