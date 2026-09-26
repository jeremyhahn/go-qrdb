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
)

// auth.mtls_binding_policy (go-qrdb#517). The config layer only carries the
// string; auth.ParseMTLSBindingPolicy resolves it at the boot. These pin that
// an omitted key stays empty (which resolves to "enrolled", the historical
// behaviour) and that an operator's value reaches the struct.

func TestLoad_MTLSBindingPolicyOmittedIsEmpty(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.MTLSBindingPolicy != "" {
		t.Fatalf("Auth.MTLSBindingPolicy = %q, want empty (the enrolled default)", cfg.Auth.MTLSBindingPolicy)
	}
}

func TestLoad_MTLSBindingPolicyFromYAML(t *testing.T) {
	for _, want := range []string{"enrolled", "required"} {
		t.Run(want, func(t *testing.T) {
			path := writeTemp(t, validYAML+`
auth:
  enabled: true
  mtls_binding_policy: "`+want+`"
`)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Auth.MTLSBindingPolicy != want {
				t.Fatalf("Auth.MTLSBindingPolicy = %q, want %q", cfg.Auth.MTLSBindingPolicy, want)
			}
		})
	}
}

// TestLoad_MTLSBindingPolicyUnknownRefusedAtLoad: an unrecognised posture is a
// configuration error, not something a boot later has to guess about.
func TestLoad_MTLSBindingPolicyUnknownRefusedAtLoad(t *testing.T) {
	for _, bad := range []string{"requierd", "strict", "on"} {
		t.Run(bad, func(t *testing.T) {
			path := writeTemp(t, validYAML+`
auth:
  enabled: true
  mtls_binding_policy: "`+bad+`"
`)
			_, err := Load(path)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != "auth.mtls_binding_policy" {
				t.Fatalf("Load = %v, want a ValidationError on auth.mtls_binding_policy", err)
			}
		})
	}
}
