/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package security

import (
	"strings"
	"testing"
)

// ── Encrypt + Decrypt round-trip ────────────────────────────────────────

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	tests := []string{
		"digiuser:digipoolpass",
		"simple",
		"with spaces and symbols!@#$%^&*()",
		"unicode: 日本語 🔑",
		"very-long-string-" + strings.Repeat("x", 1000),
		"a", // single char
	}
	for _, plaintext := range tests {
		t.Run(plaintext[:min(len(plaintext), 30)], func(t *testing.T) {
			enc, err := Encrypt(plaintext)
			if err != nil {
				t.Fatalf("Encrypt failed: %v", err)
			}
			if !strings.HasPrefix(enc, encPrefix) {
				t.Errorf("encrypted output missing %s prefix: %s", encPrefix, enc)
			}
			dec, err := Decrypt(enc)
			if err != nil {
				t.Fatalf("Decrypt failed: %v", err)
			}
			if dec != plaintext {
				t.Errorf("round-trip mismatch: got %q, want %q", dec, plaintext)
			}
		})
	}
}

func TestEncrypt_EmptyString(t *testing.T) {
	// Encrypting empty should produce something with the prefix (the nonce
	// + auth tag are still emitted). Decrypting that should yield empty.
	enc, err := Encrypt("")
	if err != nil {
		t.Fatalf("Encrypt(\"\") failed: %v", err)
	}
	if !strings.HasPrefix(enc, encPrefix) {
		t.Errorf("empty encrypt missing prefix: %s", enc)
	}
	dec, err := Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt of empty failed: %v", err)
	}
	if dec != "" {
		t.Errorf("expected empty round-trip, got %q", dec)
	}
}

// ── Random nonce — same plaintext encrypts to different ciphertext ──────

func TestEncrypt_RandomNonce(t *testing.T) {
	// Without random nonces, AES-GCM is catastrophically broken (key + nonce
	// reuse leaks plaintext). Pin the behavior with a test so a future refactor
	// can't accidentally regress to a fixed/deterministic nonce.
	enc1, err1 := Encrypt("same-input")
	enc2, err2 := Encrypt("same-input")
	if err1 != nil || err2 != nil {
		t.Fatalf("encrypt errors: %v, %v", err1, err2)
	}
	if enc1 == enc2 {
		t.Errorf("two encryptions of the same plaintext produced identical ciphertext — nonce isn't random?")
	}
	// Both still decrypt to the same plaintext.
	d1, _ := Decrypt(enc1)
	d2, _ := Decrypt(enc2)
	if d1 != "same-input" || d2 != "same-input" {
		t.Errorf("decryption mismatch: %q, %q", d1, d2)
	}
}

// ── Decrypt error paths ─────────────────────────────────────────────────

func TestDecrypt_MissingPrefix(t *testing.T) {
	_, err := Decrypt("plaintext-without-prefix")
	if err == nil {
		t.Errorf("expected error decrypting non-prefixed value")
	}
}

func TestDecrypt_InvalidBase64(t *testing.T) {
	_, err := Decrypt(encPrefix + "!!!not-valid-base64!!!")
	if err == nil {
		t.Errorf("expected error decoding bad base64")
	}
}

func TestDecrypt_TooShort(t *testing.T) {
	// Less than the 12-byte nonce — should fail the length check before
	// reaching the GCM Open call.
	_, err := Decrypt(encPrefix + "YWJj") // "abc" base64'd = 3 bytes
	if err == nil {
		t.Errorf("expected error on too-short ciphertext")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	// GCM auth tag protects against bit-flipping attacks. Verify a single-
	// byte modification past the prefix is rejected.
	enc, err := Encrypt("important credential")
	if err != nil {
		t.Fatalf("setup encrypt failed: %v", err)
	}
	// Flip a character in the middle of the base64 payload.
	payload := []byte(enc)
	mid := len(payload) / 2
	if payload[mid] == 'A' {
		payload[mid] = 'B'
	} else {
		payload[mid] = 'A'
	}
	if _, err := Decrypt(string(payload)); err == nil {
		t.Errorf("expected error decrypting tampered ciphertext")
	}
}

// ── IsEncrypted boundary cases ──────────────────────────────────────────

func TestIsEncrypted(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"ENC", false},
		{"ENC:", true}, // bare prefix counts — decrypt will then fail, which is the right behavior
		{"ENC:somepayload", true},
		{"enc:lowercase", false}, // case-sensitive — only uppercase ENC: counts
		{"plaintext", false},
		{"digiuser:digipoolpass", false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := IsEncrypted(tt.value); got != tt.want {
				t.Errorf("IsEncrypted(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// ── DecryptIfEncrypted: the legacy-passthrough convenience ──────────────

func TestDecryptIfEncrypted_PlaintextPassthrough(t *testing.T) {
	// Legacy plaintext values must round-trip unchanged through the helper —
	// this is what makes the migration zero-touch (old configs keep working
	// until the next save).
	tests := []string{
		"digiuser:digipoolpass",
		"",
		"https://discord.com/api/webhooks/123/abc",
	}
	for _, plaintext := range tests {
		t.Run(plaintext[:min(len(plaintext), 30)], func(t *testing.T) {
			got, err := DecryptIfEncrypted(plaintext)
			if err != nil {
				t.Errorf("unexpected error on plaintext passthrough: %v", err)
			}
			if got != plaintext {
				t.Errorf("plaintext mutated: got %q, want %q", got, plaintext)
			}
		})
	}
}

func TestDecryptIfEncrypted_DecryptsEncryptedForm(t *testing.T) {
	enc, _ := Encrypt("a-real-secret")
	got, err := DecryptIfEncrypted(enc)
	if err != nil {
		t.Fatalf("DecryptIfEncrypted failed: %v", err)
	}
	if got != "a-real-secret" {
		t.Errorf("got %q, want %q", got, "a-real-secret")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
