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

// auth.cluster_trust_domain is minted into certificate SANs and pinned by the
// authenticator, so it is validated at load with the same round trip both
// apply (go-qrdb#519 review) rather than surfacing as a refusal at issue time.

func TestLoad_ClusterTrustDomainValid(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
  cluster_trust_domain: "cluster.example.com"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Auth.EffectiveClusterTrustDomain(); got != "cluster.example.com" {
		t.Fatalf("EffectiveClusterTrustDomain = %q, want cluster.example.com", got)
	}
}

func TestLoad_NoClusterTrustDomainTakesTheDefault(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Auth.EffectiveClusterTrustDomain(); got != DefaultClusterTrustDomain {
		t.Fatalf("EffectiveClusterTrustDomain = %q, want %q", got, DefaultClusterTrustDomain)
	}
}

func TestLoad_ClusterTrustDomainThatDoesNotRoundTripRefused(t *testing.T) {
	for _, td := range []string{"cluster.example.com/tenant/other", "cluster.example.com?x=1", "user@cluster.example.com"} {
		t.Run(td, func(t *testing.T) {
			path := writeTemp(t, validYAML+`
auth:
  enabled: true
  cluster_trust_domain: "`+td+`"
`)
			_, err := Load(path)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != "auth.cluster_trust_domain" {
				t.Fatalf("Load = %v, want a ValidationError on auth.cluster_trust_domain", err)
			}
		})
	}
}

// The cluster has one trust domain setting. auth.spiffe_trust_domain was
// folded into auth.cluster_trust_domain and deleted, so a file still carrying
// it fails the way any unknown key does.
func TestLoad_SPIFFETrustDomainKeyIsUnknown(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
  spiffe_trust_domain: "tenants.example.com"
`)
	_, err := Load(path)
	var ue *UnknownFieldError
	if !errors.As(err, &ue) || ue.Path != "auth.spiffe_trust_domain" {
		t.Fatalf("Load = %v, want an UnknownFieldError on auth.spiffe_trust_domain", err)
	}
}

func TestAuthConfig_EffectiveClusterTrustDomain(t *testing.T) {
	var zero AuthConfig
	if got := zero.EffectiveClusterTrustDomain(); got != DefaultClusterTrustDomain {
		t.Errorf("default trust domain = %q, want %q", got, DefaultClusterTrustDomain)
	}
	a := AuthConfig{ClusterTrustDomain: "db.example.com"}
	if got := a.EffectiveClusterTrustDomain(); got != "db.example.com" {
		t.Errorf("configured trust domain = %q, want db.example.com", got)
	}
}
