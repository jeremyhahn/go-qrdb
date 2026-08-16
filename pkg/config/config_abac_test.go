// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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

	"gopkg.in/yaml.v3"
)

// TestAuthABACConfig_Validation verifies the auth.abac validation rule:
// enabled with an empty policy_dir is a configuration error.
func TestAuthABACConfig_Validation(t *testing.T) {
	tests := []struct {
		name      string
		abac      AuthABACConfig
		wantErr   bool
		wantField string
	}{
		{
			name: "disabled without dir is valid",
			abac: AuthABACConfig{Enabled: false},
		},
		{
			name: "enabled with dir is valid",
			abac: AuthABACConfig{Enabled: true, PolicyDir: "/etc/qrdb/abac"},
		},
		{
			name:      "enabled without dir is invalid",
			abac:      AuthABACConfig{Enabled: true},
			wantErr:   true,
			wantField: "auth.abac.policy_dir",
		},
		{
			name:      "enabled with whitespace dir is invalid",
			abac:      AuthABACConfig{Enabled: true, PolicyDir: "   "},
			wantErr:   true,
			wantField: "auth.abac.policy_dir",
		},
		{
			name: "disabled with dir is valid",
			abac: AuthABACConfig{Enabled: false, PolicyDir: "/etc/qrdb/abac"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Auth.ABAC = tt.abac
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				var vErr *ValidationError
				if !errors.As(err, &vErr) {
					t.Fatalf("error = %T, want *ValidationError", err)
				}
				if vErr.Field != tt.wantField {
					t.Errorf("Field = %q, want %q", vErr.Field, tt.wantField)
				}
			}
		})
	}
}

// TestAuthABACConfig_YAMLRoundTrip verifies the auth.abac YAML mapping.
func TestAuthABACConfig_YAMLRoundTrip(t *testing.T) {
	in := []byte("auth:\n  abac:\n    enabled: true\n    policy_dir: /etc/qrdb/abac\n")
	var cfg Config
	if err := yaml.Unmarshal(in, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if !cfg.Auth.ABAC.Enabled {
		t.Error("auth.abac.enabled not decoded")
	}
	if cfg.Auth.ABAC.PolicyDir != "/etc/qrdb/abac" {
		t.Errorf("auth.abac.policy_dir = %q, want /etc/qrdb/abac", cfg.Auth.ABAC.PolicyDir)
	}

	out, err := yaml.Marshal(&cfg.Auth.ABAC)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	var round AuthABACConfig
	if err := yaml.Unmarshal(out, &round); err != nil {
		t.Fatalf("yaml.Unmarshal(round) error = %v", err)
	}
	if round != cfg.Auth.ABAC {
		t.Errorf("round-trip mismatch: %+v != %+v", round, cfg.Auth.ABAC)
	}
}
