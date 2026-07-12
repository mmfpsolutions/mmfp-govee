/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

// Package security provides AES-256-GCM encryption for sensitive configuration
// values stored on disk. Encrypted values carry an "ENC:" prefix in config
// files; plaintext values (no prefix) are accepted for backward compatibility
// and auto-migrated to encrypted form on first config load.
//
// Threat model — what this DOES protect against:
//   - Backups of the config directory leaking working credentials.
//   - Accidental git commits / pastes in chat / screenshots of config.json.
//   - Docker volume snapshots / container images captured without the binary.
//   - Casual reads by anyone with file-system access who isn't a Go reverse-
//     engineer.
//
// Threat model — what this does NOT protect against:
//   - Someone who has the MMFP Govee binary can extract the embedded AES key with
//     effort and decrypt everything. Production builds use `-ldflags="-s -w"`
//     (see Dockerfile) which strips symbols and meaningfully raises the bar,
//     but the ceiling is still "determined RE-skilled attacker wins."
//
// This matches the threat model GSS accepts for its pkg/crypto package — we
// trade a tiny CPU cost (~1-3 microseconds per AES decrypt) for a meaningful
// defense-in-depth posture on disk and in transit-through-logs. Plaintext only
// briefly exists on the stack of the function that needs it; nothing long-
// lived holds it in heap memory.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

const encPrefix = "ENC:"

// encryptionKey is the AES-256 key embedded in the binary. Distinct from any
// other product's key so each tool manages its own secret persistence
// independently. See the package doc-comment for the threat-model framing.
var encryptionKey = [32]byte{0x92, 0x52, 0xbe, 0x12, 0x23, 0x53, 0x88, 0x79, 0xca, 0x97, 0xd7, 0x38, 0x45, 0xac, 0xf2, 0x16, 0xbd, 0xdf, 0xc3, 0x27, 0x28, 0x97, 0x47, 0xf0, 0xbd, 0x70, 0x80, 0xda, 0xc7, 0x28, 0xe1, 0xd6}

// Encrypt encrypts a plaintext string using AES-256-GCM with a random nonce.
// Returns the encrypted value with the "ENC:" prefix so callers can detect
// the encrypted form without trying to decrypt and failing.
func Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(encryptionKey[:])
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Seal prepends the nonce to the ciphertext, so the encoded payload is
	// self-contained: callers don't need to track the nonce separately.
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	return encPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts an ENC:-prefixed value. Returns an error if the value
// is missing the prefix, decodes badly, or fails the GCM auth tag check
// (key mismatch or tampered ciphertext).
func Decrypt(encrypted string) (string, error) {
	if !strings.HasPrefix(encrypted, encPrefix) {
		return "", fmt.Errorf("value is not encrypted (missing %s prefix)", encPrefix)
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encrypted, encPrefix))
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	block, err := aes.NewCipher(encryptionKey[:])
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("encrypted data too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed (wrong key or corrupted data): %w", err)
	}

	return string(plaintext), nil
}

// IsEncrypted reports whether a value carries the "ENC:" prefix indicating it
// is already encrypted. Used by the migration helpers to skip values that
// have already been processed.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, encPrefix)
}

// DecryptIfEncrypted decrypts the value if it has the "ENC:" prefix; otherwise
// returns it as-is. This is the call site every consumer of an encrypted
// config field should use — it transparently handles both legacy plaintext
// (pre-migration) and the post-migration encrypted form.
func DecryptIfEncrypted(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	return Decrypt(value)
}
