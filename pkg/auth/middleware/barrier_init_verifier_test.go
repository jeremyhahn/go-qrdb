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

package middleware

import (
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The verifier a remote facade runs on its own connection: begin, prove,
// complete, with the key held to the connection certificate's.
func TestTenantBarrierInitVerifier_FacadeRoundTrip(t *testing.T) {
	v := NewTenantBarrierInitVerifier(nil)
	cert := initprooftest.Certificate(t, "alice")
	conn := initprooftest.Dial(t, cert)
	c, err := v.Begin("acme", &conn.Server)
	if err != nil {
		t.Fatal(err)
	}
	if n := v.Outstanding(); n != 1 {
		t.Fatalf("Outstanding = %d, want 1", n)
	}
	p := conn.Prove(t, cert.PrivateKey.(*ecdsa.PrivateKey), "acme", c.Nonce)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	key := func() (string, error) { return user.ComputeSPKIThumbprint(leaf), nil }
	proof := &TenantBarrierInitProof{Nonce: p.Nonce, PublicKey: p.PublicKey, Signature: p.Signature}
	if err := v.Complete("acme", &conn.Server, proof, key); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if n := v.Outstanding(); n != 0 {
		t.Fatalf("Outstanding after complete = %d, want 0", n)
	}
}

// Without a key source there is no key the proof could be by.
func TestTenantBarrierInitVerifier_NilKeySourceIsRefused(t *testing.T) {
	v := NewTenantBarrierInitVerifier(initproof.NewBook(nil, 0))
	cert := initprooftest.Certificate(t, "alice")
	conn := initprooftest.Dial(t, cert)
	c, err := v.Begin("acme", &conn.Server)
	if err != nil {
		t.Fatal(err)
	}
	p := conn.Prove(t, cert.PrivateKey.(*ecdsa.PrivateKey), "acme", c.Nonce)
	err = v.Complete("acme", &conn.Server, &TenantBarrierInitProof{Nonce: p.Nonce, PublicKey: p.PublicKey, Signature: p.Signature}, nil)
	var pe *tenant.FirstAdminProofError
	if !errors.As(err, &pe) || pe.Reason != initproof.ReasonNoPendingAdmin {
		t.Fatalf("err = %v; want ReasonNoPendingAdmin", err)
	}
}
