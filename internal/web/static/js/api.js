/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// MMFP Govee API Client
'use strict';

class APIClient {
    constructor(baseURL) {
        this.baseURL = baseURL || '';
    }

    async get(endpoint) {
        var response = await fetch(this.baseURL + endpoint);
        var data = await response.json().catch(function() { return {}; });
        if (!response.ok) {
            var msg = (data.errors && data.errors[0] && data.errors[0].message) || ('HTTP ' + response.status);
            throw new Error(msg);
        }
        return data;
    }

    async post(endpoint, body) {
        var response = await fetch(this.baseURL + endpoint, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: body ? JSON.stringify(body) : undefined
        });
        var data = await response.json().catch(function() { return {}; });
        if (!response.ok) {
            var msg = (data.errors && data.errors[0] && data.errors[0].message) || ('HTTP ' + response.status);
            var err = new Error(msg);
            err.status = response.status;
            err.code = (data.errors && data.errors[0] && data.errors[0].code) || null;
            throw err;
        }
        return data;
    }

    async put(endpoint, body) {
        var response = await fetch(this.baseURL + endpoint, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: body ? JSON.stringify(body) : undefined
        });
        var data = await response.json().catch(function() { return {}; });
        if (!response.ok) {
            var msg = (data.errors && data.errors[0] && data.errors[0].message) || ('HTTP ' + response.status);
            throw new Error(msg);
        }
        return data;
    }

    async del(endpoint) {
        var response = await fetch(this.baseURL + endpoint, { method: 'DELETE' });
        var data = await response.json().catch(function() { return {}; });
        if (!response.ok) {
            var msg = (data.errors && data.errors[0] && data.errors[0].message) || ('HTTP ' + response.status);
            throw new Error(msg);
        }
        return data;
    }

    async getStatus() {
        return this.get('/api/v1/status');
    }

    async getDevices() {
        return this.get('/api/v1/devices');
    }

    async refreshDevices() {
        return this.post('/api/v1/devices/refresh');
    }

    async testDevice(device, sku) {
        return this.post('/api/v1/devices/' + encodeURIComponent(device) + '/test', { sku: sku });
    }

    async getDeviceScenes(device, sku, refresh) {
        return this.get('/api/v1/devices/' + encodeURIComponent(device) + '/scenes?sku=' +
            encodeURIComponent(sku) + (refresh ? '&refresh=1' : ''));
    }

    async getDevicesStatus() {
        return this.get('/api/v1/devices/status');
    }

    async getDeviceState(device, sku) {
        return this.get('/api/v1/devices/' + encodeURIComponent(device) + '/state?sku=' + encodeURIComponent(sku));
    }

    async controlDevice(device, body) {
        return this.post('/api/v1/devices/' + encodeURIComponent(device) + '/control', body);
    }

    async getEvents() {
        return this.get('/api/v1/events');
    }

    async getMappings() {
        return this.get('/api/v1/mappings');
    }

    async createMapping(data) {
        return this.post('/api/v1/mappings', data);
    }

    async updateMapping(id, data) {
        return this.put('/api/v1/mappings/' + encodeURIComponent(id), data);
    }

    async deleteMapping(id) {
        return this.del('/api/v1/mappings/' + encodeURIComponent(id));
    }

    async testMapping(id) {
        return this.post('/api/v1/mappings/' + encodeURIComponent(id) + '/test');
    }

    async getTokens() {
        return this.get('/api/v1/tokens');
    }

    async createToken(name) {
        return this.post('/api/v1/tokens', { name: name });
    }

    async deleteToken(name) {
        return this.del('/api/v1/tokens/' + encodeURIComponent(name));
    }

    async getSettings() {
        return this.get('/api/v1/settings');
    }

    async updateSettings(body) {
        return this.put('/api/v1/settings', body);
    }

    async getActivity(limit) {
        return this.get('/api/v1/activity' + (limit ? '?limit=' + limit : ''));
    }
}

var api = new APIClient();
