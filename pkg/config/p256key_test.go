// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// validP256Hex returns the hex form of a freshly generated uncompressed P-256
// public key, the shape a first node publishes.
func validP256Hex(t *testing.T) string {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return hex.EncodeToString(key.PublicKey().Bytes())
}

func TestValidate_BootstrapP256Keys(t *testing.T) {
	valid := validP256Hex(t)
	offCurve := "04" + strings.Repeat("11", 64)

	tests := []struct {
		name      string
		mutate    func(c *Config)
		wantField string // empty means the configuration is valid
	}{
		{
			name: "anchor p256 key with noise addr",
			mutate: func(c *Config) {
				c.Bootstrap.P256Key = valid
				c.Bootstrap.NoiseAddr = "10.0.0.1:63006"
			},
		},
		{
			name: "anchor p256 key beside the noise key",
			mutate: func(c *Config) {
				c.Bootstrap.P256Key = valid
				c.Bootstrap.NoiseKey = "aa"
				c.Bootstrap.NoiseAddr = "10.0.0.1:63006"
			},
		},
		{
			name: "anchor p256 key without noise addr",
			mutate: func(c *Config) {
				c.Bootstrap.P256Key = valid
			},
			wantField: "bootstrap.noise_addr",
		},
		{
			name: "anchor p256 key not hex",
			mutate: func(c *Config) {
				c.Bootstrap.P256Key = "not-hex"
				c.Bootstrap.NoiseAddr = "10.0.0.1:63006"
			},
			wantField: "bootstrap.p256_key",
		},
		{
			name: "anchor p256 key wrong length",
			mutate: func(c *Config) {
				c.Bootstrap.P256Key = "aabbcc"
				c.Bootstrap.NoiseAddr = "10.0.0.1:63006"
			},
			wantField: "bootstrap.p256_key",
		},
		{
			name: "anchor p256 key off the curve",
			mutate: func(c *Config) {
				c.Bootstrap.P256Key = offCurve
				c.Bootstrap.NoiseAddr = "10.0.0.1:63006"
			},
			wantField: "bootstrap.p256_key",
		},
		{
			name: "join p256 key alone satisfies join addr",
			mutate: func(c *Config) {
				c.Bootstrap.JoinAddr = "127.0.0.1:63007"
				c.Bootstrap.JoinP256Key = valid
			},
		},
		{
			name: "join p256 key beside the join key",
			mutate: func(c *Config) {
				c.Bootstrap.JoinAddr = "127.0.0.1:63007"
				c.Bootstrap.JoinKey = "aa"
				c.Bootstrap.JoinP256Key = valid
			},
		},
		{
			name: "join p256 key without join addr",
			mutate: func(c *Config) {
				c.Bootstrap.JoinP256Key = valid
			},
			wantField: "bootstrap.join_addr",
		},
		{
			name: "join p256 key off the curve",
			mutate: func(c *Config) {
				c.Bootstrap.JoinAddr = "127.0.0.1:63007"
				c.Bootstrap.JoinP256Key = offCurve
			},
			wantField: "bootstrap.join_p256_key",
		},
		{
			name: "join addr with neither key",
			mutate: func(c *Config) {
				c.Bootstrap.JoinAddr = "127.0.0.1:63007"
			},
			wantField: "bootstrap.join_key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Validate() = %v (%T), want *ValidationError", err, err)
			}
			if verr.Field != tt.wantField {
				t.Errorf("ValidationError.Field = %q, want %q", verr.Field, tt.wantField)
			}
		})
	}
}

func TestValidateP256PublicKeyHex_EmptyIsValid(t *testing.T) {
	if err := validateP256PublicKeyHex("bootstrap.p256_key", ""); err != nil {
		t.Fatalf("an unset key must be valid, got %v", err)
	}
}
