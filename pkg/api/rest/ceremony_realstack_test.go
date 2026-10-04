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

//go:build !integration

package rest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// realStackAdminLookup is a minimal firstboot.AdminLookup/CertBinder double
// over a single in-memory admin record -- a legitimate STORE-SHAPED test
// double (go-qrdb#560 review B1, re-review: "no mocks that return
// ErrPINInvalid themselves"). It never answers the SO-PIN question itself;
// that question is answered by the REAL pin.PINBackend wired into the REAL
// ceremony.CeremonyService below, through the REAL
// service.CeremonyServiceWithAdminRebind.
type realStackAdminLookup struct {
	admin *user.User
}

func (a *realStackAdminLookup) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	if tenantID != tenantid.System || username != user.BootstrapAdminUsername {
		return nil, &user.UserNotFoundError{Lookup: username}
	}
	return a.admin, nil
}

func (a *realStackAdminLookup) BindAdminCert(_ context.Context, _ string, _ uint64, thumbprint string) error {
	a.admin.CertBindings = append(a.admin.CertBindings, user.CertBinding{
		Thumbprint: thumbprint, Kind: user.CertBindingSPKIThumbprint,
	})
	return nil
}

// newRealCeremonyCA builds a real, self-signed ceremony.LocalCA for the
// REAL-stack reissue tests.
func newRealCeremonyCA(t *testing.T) ceremony.CeremonyCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Ceremony CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ceremony.NewLocalCA(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// newRealReissueStack builds a REAL ceremony.CeremonyService, already
// initialized single-admin under soPIN, wrapped in the REAL
// service.CeremonyServiceWithAdminRebind -- no PIN-check double anywhere:
// the SO PIN is verified by the REAL pin.SoftwareBackend
// ReissueAdminCredential (via CeremonyServiceWithAdminRebind) reaches
// through pin.RequireProvisionedSOPIN. Returns the wrapped CeremonyServicer
// REST wires via WithCeremonyService, and the CA REST wires via
// WithCeremonyCA.
func newRealReissueStack(t *testing.T, soPIN string) (service.CeremonyServicer, ceremony.CeremonyCA) {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := seal.NewStorageBarrier(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	core.SetPINBackend(pintest.NewBackend(t))
	ca := newRealCeremonyCA(t)

	if _, err := core.Initialize(context.Background(), ca, &ceremony.InitializeOptions{
		SOPin: soPIN, UserPin: "test-user-pin", Hostname: "db.example.com", AdminGenerate: true,
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	admin := &user.User{ID: 1, Username: user.BootstrapAdminUsername, TenantID: tenantid.System, Lifecycle: user.LifecycleActive}
	lookup := &realStackAdminLookup{admin: admin}
	wrapped := service.NewCeremonyServiceWithAdminRebind(service.NewCeremonyService(core), core, lookup, lookup, nil)
	return wrapped, ca
}

// TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN is the
// go-qrdb#560 re-review's B1 fix: unlike
// TestHandleCeremonyInitialize_SOPINRefusalPropagates (a mock
// CeremonyServicer that returns pin.ErrPINInvalid directly, proving only
// that REST RENDERS that error as 401, not that anything actually checks a
// PIN), this drives a REAL ceremony.CeremonyService, a REAL
// pin.SoftwareBackend and the REAL service.CeremonyServiceWithAdminRebind
// wrapper through the REST handler. Removing pin.RequireProvisionedSOPIN
// from ReissueAdminCredential (or reverting VerifyReissueSOPIN's
// ordering) turns this red; the mock-based sibling test cannot.
func TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN(t *testing.T) {
	wrapped, ca := newRealReissueStack(t, "correct-horse-battery")
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(wrapped), WithCeremonyCA(ca))

	// Wrong SO PIN: refused, never bound.
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		strings.NewReader(`{"so_pin":"wrong-pin","admin_generate":true}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusUnauthorized)

	// Missing SO PIN entirely: also refused.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		strings.NewReader(`{"admin_generate":true}`))
	rec2 := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec2, req2)
	assertStatus(t, rec2, http.StatusUnauthorized)

	// The correct SO PIN still reissues and binds cleanly -- proving the
	// refusals above were about the PIN, not a stack that always refuses.
	req3 := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		bytes.NewReader([]byte(`{"so_pin":"correct-horse-battery","admin_generate":true}`)))
	rec3 := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec3, req3)
	assertStatus(t, rec3, http.StatusOK)
}

// TestHandleCeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound
// is the OTHER half of the go-qrdb#560 re-review's B1 ordering fix: the SO
// PIN is verified BEFORE anything that would tell an unauthenticated
// caller whether the admin already holds a binding. Against an admin that
// IS already bound, a wrong SO PIN must still answer the PIN failure (401
// Unauthenticated), never service.AdminAlreadyBoundError's own distinct
// signal -- which would leak the admin's binding state to a caller who
// never authenticated at all.
func TestHandleCeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound(t *testing.T) {
	wrapped, ca := newRealReissueStack(t, "correct-horse-battery")
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(wrapped), WithCeremonyCA(ca))

	// Bind the admin first, with the correct PIN, so the "already bound"
	// branch becomes reachable.
	bindReq := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		strings.NewReader(`{"so_pin":"correct-horse-battery","admin_generate":true}`))
	bindRec := httptest.NewRecorder()
	s.handleCeremonyInitialize(bindRec, bindReq)
	assertStatus(t, bindRec, http.StatusOK)

	// A wrong SO PIN against this now-bound admin must still answer the
	// PIN failure, not leak AdminAlreadyBoundError to an unauthenticated
	// caller.
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		strings.NewReader(`{"so_pin":"wrong-pin","admin_generate":true}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusUnauthorized)
}
