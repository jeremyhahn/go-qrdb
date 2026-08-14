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
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAuthConfig_SODualFactor_DefaultOff verifies the SO dual-factor
// flag defaults to false so enabling the feature is always an explicit
// operator decision.
func TestAuthConfig_SODualFactor_DefaultOff(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Auth.SODualFactor {
		t.Fatal("auth.so_dual_factor must default to false")
	}
}

// TestAuthConfig_SODualFactor_YAMLRoundTrip verifies the flag parses
// from its documented YAML key.
func TestAuthConfig_SODualFactor_YAMLRoundTrip(t *testing.T) {
	var cfg Config
	src := "auth:\n  so_dual_factor: true\n"
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Auth.SODualFactor {
		t.Fatal("auth.so_dual_factor: true did not parse")
	}

	var off Config
	if err := yaml.Unmarshal([]byte("auth:\n  so_dual_factor: false\n"), &off); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if off.Auth.SODualFactor {
		t.Fatal("auth.so_dual_factor: false did not parse")
	}
}
