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

package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// revocationStore is the replicated revocation state a node reads: a set of
// revoked serials. It answers only IsRevoked; the cache never asks it for
// anything else.
type revocationStore struct {
	cert.CertProvider

	mu      sync.Mutex
	revoked map[string]bool
}

func (s *revocationStore) IsRevoked(_ context.Context, serial string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[serial], nil
}

func (s *revocationStore) revoke(serial string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[serial] = true
}

// TestDelegation_RevocationRefusesNextCallUnderDefaultCacheTTL: with the
// production revocation cache at its default TTL, a delegation that has
// already authenticated -- so a not-revoked answer for its serial sits in
// the cache -- is refused on the very next call once its serial is revoked.
// An ordinary certificate keeps the cache window: the same cache still
// answers a cached not-revoked for it.
func TestDelegation_RevocationRefusesNextCallUnderDefaultCacheTTL(t *testing.T) {
	ctx := context.Background()
	store := &revocationStore{revoked: map[string]bool{}}
	cache := cert.NewRevocationCache(store, cert.DefaultRevocationCacheTTL)
	if cache.TTL() <= 0 {
		t.Fatalf("default revocation cache TTL = %v, want the nonzero default", cache.TTL())
	}

	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: "svc-billing", Scope: []string{string(permission.KVGet)}})
	a := d.NewAuthenticator(auth.WithRevocationChecker(cache))

	if _, err := a.Authenticate(d.Context(ctx)); err != nil {
		t.Fatalf("first call: %v", err)
	}
	serial := cert.FormatSerial(d.Certificate.SerialNumber)
	store.revoke(serial)

	// The cache still holds the not-revoked answer the first call stored:
	// the ordinary-certificate path would admit it inside the TTL.
	if revoked, err := cache.IsCertRevoked(ctx, d.Certificate); err != nil || revoked {
		t.Fatalf("cached answer = %v, %v; want the stale not-revoked answer the TTL keeps", revoked, err)
	}

	if _, err := a.Authenticate(d.Context(ctx)); !errors.Is(err, auth.ErrCertificateRevoked) {
		t.Fatalf("next call after revoke = %v, want ErrCertificateRevoked", err)
	}
	if _, err := a.VerifyDelegation(ctx, d.Certificate.Raw); !errors.Is(err, auth.ErrCertificateRevoked) {
		t.Fatalf("forwarded delegation after revoke = %v, want ErrCertificateRevoked", err)
	}
}
