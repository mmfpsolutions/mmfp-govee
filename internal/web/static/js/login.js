/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee Login Page
'use strict';

document.addEventListener('DOMContentLoaded', function() {
    var form = document.getElementById('login-form');
    var btn = document.getElementById('login-btn');
    var errorEl = document.getElementById('login-error');
    var usernameInput = document.getElementById('login-username');
    var passwordInput = document.getElementById('login-password');

    form.addEventListener('submit', function(e) {
        e.preventDefault();
        handleLogin();
    });

    // Also handle Enter key on inputs
    passwordInput.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') handleLogin();
    });

    function handleLogin() {
        var username = usernameInput.value.trim();
        var password = passwordInput.value;

        if (!username || !password) {
            showError('Please enter both username and password');
            return;
        }

        btn.disabled = true;
        btn.textContent = 'Signing in...';
        btn.style.opacity = '0.6';
        hideError();

        hashPassword(password).then(function(hashedPassword) {
            return fetch('/api/v1/auth/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    username: username,
                    hashedPassword: hashedPassword
                })
            });
        }).then(function(response) {
            return response.json().then(function(data) {
                if (response.ok) {
                    window.location.href = '/';
                } else {
                    var msg = (data.errors && data.errors[0] && data.errors[0].message) || 'Invalid username or password';
                    showError(msg);
                    resetButton();
                }
            });
        }).catch(function() {
            showError('Login failed. Please try again.');
            resetButton();
        });
    }

    function showError(msg) {
        errorEl.textContent = msg;
        errorEl.classList.remove('hidden');
    }

    function hideError() {
        errorEl.classList.add('hidden');
    }

    function resetButton() {
        btn.disabled = false;
        btn.textContent = 'Sign In';
        btn.style.opacity = '';
    }

    function hashPassword(password) {
        var encoder = new TextEncoder();
        var data = encoder.encode(password);
        return crypto.subtle.digest('SHA-256', data).then(function(hashBuffer) {
            var hashArray = Array.from(new Uint8Array(hashBuffer));
            return hashArray.map(function(b) {
                return b.toString(16).padStart(2, '0');
            }).join('');
        });
    }
});
