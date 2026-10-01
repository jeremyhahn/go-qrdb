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

// auth.spiffe_trust_domain is minted into certificate SANs and pinned by the
// authenticator, so it is validated at load with the same round trip both
// apply (go-qrdb#519 review) rather than surfacing as a refusal at issue time.

func TestLoad_SPIFFETrustDomainValid(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
  spiffe_trust_domain: "db.example.com"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.SPIFFETrustDomain != "db.example.com" {
		t.Fatalf("SPIFFETrustDomain = %q", cfg.Auth.SPIFFETrustDomain)
	}
}

func TestLoad_SPIFFETrustDomainThatDoesNotRoundTripRefused(t *testing.T) {
	for _, td := range []string{"db.example.com/tenant/other", "db.example.com?x=1", "user@db.example.com"} {
		t.Run(td, func(t *testing.T) {
			path := writeTemp(t, validYAML+`
auth:
  enabled: true
  spiffe_trust_domain: "`+td+`"
`)
			_, err := Load(path)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != "auth.spiffe_trust_domain" {
				t.Fatalf("Load = %v, want a ValidationError on auth.spiffe_trust_domain", err)
			}
		})
	}
}
