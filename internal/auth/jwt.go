/*
 * Copyright 2026 Scott Walter, MMFP Solutions LLC
 *
 * This program is free software; you can redistribute it and/or modify it
 * under the terms of the GNU General Public License as published by the Free
 * Software Foundation; either version 3 of the License, or (at your option)
 * any later version.  See LICENSE for more details.
 */

package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTConfig holds JWT configuration
type JWTConfig struct {
	JsonWebTokenKey string `json:"jsonWebTokenKey"`
	ExpiresIn       string `json:"expiresIn"`
}

// JWTService handles JWT creation and verification
type JWTService struct {
	secretKey string
	expiresIn time.Duration
}

// Claims represents JWT claims
type Claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

var jwtService *JWTService

// InitJWTService initializes the JWT service with configuration
func InitJWTService(configDir string) error {
	keyFilePath := filepath.Join(configDir, "jsonWebTokenKey.json")

	data, err := os.ReadFile(keyFilePath)
	if err != nil {
		return fmt.Errorf("fatal: could not load JWT secret key from jsonWebTokenKey.json: %w", err)
	}

	var keyData JWTConfig
	if err := json.Unmarshal(data, &keyData); err != nil {
		return fmt.Errorf("fatal: could not parse jsonWebTokenKey.json: %w", err)
	}

	if keyData.JsonWebTokenKey == "" || keyData.ExpiresIn == "" {
		return fmt.Errorf("fatal: jsonWebTokenKey or expiresIn key not found in jsonWebTokenKey.json")
	}

	// Parse expiration duration (e.g., "1h", "30m", "24h")
	duration, err := time.ParseDuration(keyData.ExpiresIn)
	if err != nil {
		return fmt.Errorf("fatal: invalid expiresIn format: %w", err)
	}

	jwtService = &JWTService{
		secretKey: keyData.JsonWebTokenKey,
		expiresIn: duration,
	}

	return nil
}

// GetJWTService returns the initialized JWT service
func GetJWTService() *JWTService {
	return jwtService
}

// CreateToken creates a new JWT token for the given username
func (j *JWTService) CreateToken(username string) (string, error) {
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(j.expiresIn)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(j.secretKey))
	if err != nil {
		return "", fmt.Errorf("JWT creation error: %w", err)
	}

	return tokenString, nil
}

// VerifyToken verifies a JWT token and returns the claims
func (j *JWTService) VerifyToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(j.secretKey), nil
	})

	if err != nil {
		return nil, fmt.Errorf("JWT verification error: %w", err)
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// AccessCredentials represents user credentials
type AccessCredentials map[string]string

// LoadAccessCredentials loads user credentials from access.json
func LoadAccessCredentials(configDir string) (AccessCredentials, error) {
	accessFilePath := filepath.Join(configDir, "access.json")

	data, err := os.ReadFile(accessFilePath)
	if err != nil {
		return nil, fmt.Errorf("security warning: access.json file not found: %w", err)
	}

	var accessData AccessCredentials
	if err := json.Unmarshal(data, &accessData); err != nil {
		return nil, fmt.Errorf("error parsing access.json: %w", err)
	}

	return accessData, nil
}
