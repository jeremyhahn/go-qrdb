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
	"errors"
	"testing"
	"time"
)

func TestParseSignedInitTTL_DefaultsAndParses(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":      5 * time.Minute,
		"  ":    5 * time.Minute,
		"5m":    5 * time.Minute,
		"30s":   30 * time.Second,
		" 1ns ": time.Nanosecond,
	} {
		got, err := ParseSignedInitTTL(in)
		if err != nil || got != want {
			t.Fatalf("ParseSignedInitTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestParseSignedInitTTL_RefusesNonPositiveTooLongAndGarbage(t *testing.T) {
	for _, in := range []string{"0", "-1s", "5m1s", "1h", "soon"} {
		if _, err := ParseSignedInitTTL(in); err == nil {
			t.Fatalf("ParseSignedInitTTL(%q) accepted", in)
		}
	}
}

func TestTenantConfig_SignedInitTTLDuration(t *testing.T) {
	if got := (TenantConfig{SignedInitTTL: "10s"}).SignedInitTTLDuration(); got != 10*time.Second {
		t.Fatalf("SignedInitTTLDuration(10s) = %v", got)
	}
	if got := (TenantConfig{SignedInitTTL: "1h"}).SignedInitTTLDuration(); got != MaxSignedInitTTL {
		t.Fatalf("SignedInitTTLDuration(1h) = %v, want the default %v", got, MaxSignedInitTTL)
	}
}

func TestValidate_RefusesABadSignedInitTTL(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tenant.SignedInitTTL != DefaultSignedInitTTLString {
		t.Fatalf("default tenant.signed_init_ttl = %q, want %q", cfg.Tenant.SignedInitTTL, DefaultSignedInitTTLString)
	}
	cfg.Tenant.SignedInitTTL = "10m"
	var ve *ValidationError
	if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "tenant.signed_init_ttl" {
		t.Fatalf("Validate() = %v, want a ValidationError on tenant.signed_init_ttl", err)
	}
	cfg.Tenant.SignedInitTTL = "1m"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with 1m = %v", err)
	}
}
