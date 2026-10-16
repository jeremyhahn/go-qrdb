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

package embedded

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// deleteFactorTransport is an embedded transport over the real factor
// service on a store that supports the multi-guarded batch Delete commits
// through.
func deleteFactorTransport(t *testing.T) *Transport {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kvstoretest.NewGuardedMemoryStore()})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return &Transport{userAuthFactorSvc: svc}
}

// certBinding is the add request binding a freshly issued certificate to
// userID the way a caller binds one: the certificate's digest as the
// credential and its SubjectPublicKeyInfo digest as the SPKI credential.
func certBinding(t *testing.T, userID uint64) *transport.AuthFactorAddRequest {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "operator"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	cred := sha256.Sum256(cert.Raw)
	spki := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return &transport.AuthFactorAddRequest{
		UserID: userID, Type: "mtls", Label: "cert", Credential: cred[:], SPKICredential: spki[:],
	}
}

// TestDeleteAuthFactor_rebindsTheSameCertificate is #684's verification: a
// factor added and then deleted leaves nothing behind, so binding the same
// certificate to the same user again succeeds.
func TestDeleteAuthFactor_rebindsTheSameCertificate(t *testing.T) {
	tr := deleteFactorTransport(t)
	ctx := context.Background()
	bind := certBinding(t, 42)

	added, err := tr.AddAuthFactor(ctx, bind)
	if err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	if _, err := tr.AddAuthFactor(ctx, bind); !errors.As(err, new(*transport.FactorAlreadyEnrolledError)) {
		t.Fatalf("second bind before delete err = %v, want FactorAlreadyEnrolledError", err)
	}

	resp, err := tr.DeleteAuthFactor(ctx, &transport.AuthFactorDeleteRequest{FactorID: added.Factor.ID, Force: true})
	if err != nil || resp.Status != "deleted" {
		t.Fatalf("DeleteAuthFactor = %+v, %v; want status deleted", resp, err)
	}

	rebound, err := tr.AddAuthFactor(ctx, bind)
	if err != nil {
		t.Fatalf("rebind after delete: %v", err)
	}
	if rebound.Factor.ID == added.Factor.ID {
		t.Fatalf("rebind reused the deleted factor id %s", added.Factor.ID)
	}
	if _, err := tr.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{FactorID: added.Factor.ID}); !errors.As(err, new(*transport.FactorNotFoundError)) {
		t.Fatalf("Get of the deleted factor err = %v, want FactorNotFoundError", err)
	}
}

// TestDeleteAuthFactor_secondDeleteIsNotFound pins that a delete is not
// idempotent-silent: the record is gone, and the caller is told so.
func TestDeleteAuthFactor_secondDeleteIsNotFound(t *testing.T) {
	tr := deleteFactorTransport(t)
	ctx := context.Background()
	added, err := tr.AddAuthFactor(ctx, certBinding(t, 42))
	if err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	req := &transport.AuthFactorDeleteRequest{FactorID: added.Factor.ID, Force: true}
	if _, err := tr.DeleteAuthFactor(ctx, req); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if _, err := tr.DeleteAuthFactor(ctx, req); !errors.As(err, new(*transport.FactorNotFoundError)) {
		t.Fatalf("second delete err = %v, want FactorNotFoundError", err)
	}
}

// TestDeleteAuthFactor_neverReinstatesARevokedCertificate pins the ruling's
// other half: a revoked factor cannot be deleted, so its credential stays
// claimed and the revocation cannot be undone by delete-then-add.
func TestDeleteAuthFactor_neverReinstatesARevokedCertificate(t *testing.T) {
	tr := deleteFactorTransport(t)
	ctx := context.Background()
	bind := certBinding(t, 42)
	added, err := tr.AddAuthFactor(ctx, bind)
	if err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	if _, err := tr.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{FactorID: added.Factor.ID, Force: true}); err != nil {
		t.Fatalf("RemoveAuthFactor: %v", err)
	}

	if _, err := tr.DeleteAuthFactor(ctx, &transport.AuthFactorDeleteRequest{FactorID: added.Factor.ID, Force: true}); !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("delete of a revoked factor err = %v, want ErrFactorRevoked", err)
	}
	if _, err := tr.AddAuthFactor(ctx, bind); !errors.As(err, new(*transport.FactorAlreadyEnrolledError)) {
		t.Fatalf("rebind of a revoked certificate err = %v, want FactorAlreadyEnrolledError", err)
	}
}

func TestDeleteAuthFactor_refusesAnEmptyRequest(t *testing.T) {
	tr := deleteFactorTransport(t)
	for name, req := range map[string]*transport.AuthFactorDeleteRequest{
		"nil":      nil,
		"empty id": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tr.DeleteAuthFactor(context.Background(), req); !errors.As(err, new(*transport.FactorNotFoundError)) {
				t.Fatalf("err = %v, want FactorNotFoundError", err)
			}
		})
	}
}

func TestDeleteAuthFactor_withoutAServiceFails(t *testing.T) {
	tr := &Transport{}
	_, err := tr.DeleteAuthFactor(context.Background(), &transport.AuthFactorDeleteRequest{FactorID: "f"})
	if !errors.As(err, new(*MissingUserAuthFactorServiceError)) {
		t.Fatalf("err = %v, want MissingUserAuthFactorServiceError", err)
	}
}
