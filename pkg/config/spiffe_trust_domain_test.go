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

// An empty auth.spiffe_trust_domain takes auth.cluster_trust_domain (owner
// ruling on go-qrdb#777), so no deployment issues a tenant certificate
// without the SPIFFE ID SAN the authenticator requires.

func TestLoad_EmptySPIFFETrustDomainTakesTheClusterTrustDomain(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
  cluster_trust_domain: "cluster.example.com"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.SPIFFETrustDomain != "" {
		t.Fatalf("SPIFFETrustDomain = %q, want the configured empty value kept", cfg.Auth.SPIFFETrustDomain)
	}
	if got := cfg.Auth.EffectiveSPIFFETrustDomain(); got != "cluster.example.com" {
		t.Fatalf("EffectiveSPIFFETrustDomain = %q, want cluster.example.com", got)
	}
}

func TestLoad_NeitherTrustDomainTakesTheClusterDefault(t *testing.T) {
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
	if got := cfg.Auth.EffectiveSPIFFETrustDomain(); got != DefaultClusterTrustDomain {
		t.Fatalf("EffectiveSPIFFETrustDomain = %q, want %q", got, DefaultClusterTrustDomain)
	}
}

func TestLoad_ExplicitSPIFFETrustDomainIsKept(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
  cluster_trust_domain: "cluster.example.com"
  spiffe_trust_domain: "tenants.example.com"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Auth.EffectiveSPIFFETrustDomain(); got != "tenants.example.com" {
		t.Fatalf("EffectiveSPIFFETrustDomain = %q, want tenants.example.com", got)
	}
	if got := cfg.Auth.EffectiveClusterTrustDomain(); got != "cluster.example.com" {
		t.Fatalf("EffectiveClusterTrustDomain = %q, want cluster.example.com", got)
	}
}

func TestLoad_ClusterTrustDomainThatDoesNotRoundTripRefused(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
  cluster_trust_domain: "cluster.example.com/tenant/other"
`)
	_, err := Load(path)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "auth.cluster_trust_domain" {
		t.Fatalf("Load = %v, want a ValidationError on auth.cluster_trust_domain", err)
	}
}

func TestLoad_ClusterTrustDomainIsValidatedBesideAnExplicitSPIFFEOne(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
  cluster_trust_domain: "user@cluster.example.com"
  spiffe_trust_domain: "tenants.example.com"
`)
	_, err := Load(path)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "auth.cluster_trust_domain" {
		t.Fatalf("Load = %v, want a ValidationError on auth.cluster_trust_domain", err)
	}
}

func TestAuthConfig_EffectiveTrustDomainsWithoutLoad(t *testing.T) {
	var zero AuthConfig
	if got := zero.EffectiveSPIFFETrustDomain(); got != DefaultClusterTrustDomain {
		t.Fatalf("zero EffectiveSPIFFETrustDomain = %q, want %q", got, DefaultClusterTrustDomain)
	}
	a := AuthConfig{ClusterTrustDomain: "cluster.example.com"}
	if got := a.EffectiveSPIFFETrustDomain(); got != "cluster.example.com" {
		t.Fatalf("EffectiveSPIFFETrustDomain = %q, want cluster.example.com", got)
	}
}
