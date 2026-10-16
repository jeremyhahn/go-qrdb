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

// Targeted coverage tests for the Unix transport covering 0%-coverage paths:
// database barrier (SealDatabase / UnsealDatabase / DatabaseBarrierStatus),
// ceremony custodian (ClaimShareBegin / ClaimShareComplete / SubmitUnsealShare /
// protoCustodianToInfo), credentials CredentialStore / CredentialRetrieve,
// auth GetUserByCertThumbprint / AddCertBinding, and the ClosedError guards for
// all these new methods.

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Helpers
// ===========================================================================

// newClosedTransportNew creates a closed Transport (mirrors the helper in
// transport_closed_test.go but avoids redeclaring it).
func newClosed() *Transport {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	return tr
}

// ===========================================================================
// ClosedError guards — database barrier
// ===========================================================================

func TestUnixDatabaseBarrier_AfterClose(t *testing.T) {
	tr := newClosed()
	var ce *transport.ClosedError

	if err := tr.SealDatabase(context.Background(), "db"); !errors.As(err, &ce) {
		t.Errorf("SealDatabase: expected ClosedError, got %T: %v", err, err)
	}
	if err := tr.UnsealDatabase(context.Background(), "db", "secret"); !errors.As(err, &ce) {
		t.Errorf("UnsealDatabase: expected ClosedError, got %T: %v", err, err)
	}
	if _, err := tr.DatabaseBarrierStatus(context.Background(), "db"); !errors.As(err, &ce) {
		t.Errorf("DatabaseBarrierStatus: expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// ClosedError guards — ceremony custodian
// ===========================================================================

// ===========================================================================
// ClosedError guards — credentials CredentialStore / CredentialRetrieve
// ===========================================================================

func TestUnixCredentialStorageOps_AfterClose(t *testing.T) {
	tr := newClosed()
	var ce *transport.ClosedError

	if err := tr.CredentialStore(context.Background(), "k", []byte("v")); !errors.As(err, &ce) {
		t.Errorf("CredentialStore: expected ClosedError, got %T: %v", err, err)
	}
	if _, err := tr.retrieveBytes(context.Background(), "k"); !errors.As(err, &ce) {
		t.Errorf("CredentialRetrieve: expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// ClosedError guards — auth GetUserByCertThumbprint / AddCertBinding
// ===========================================================================

func TestUnixAuthCertOps_AfterClose(t *testing.T) {
	tr := newClosed()
	var ce *transport.ClosedError

	if _, err := tr.GetUserByCertThumbprint(context.Background(), "AABBCC"); !errors.As(err, &ce) {
		t.Errorf("GetUserByCertThumbprint: expected ClosedError, got %T: %v", err, err)
	}
	if _, err := tr.AddCertBinding(context.Background(), 1, "CERT", 0); !errors.As(err, &ce) {
		t.Errorf("AddCertBinding: expected ClosedError, got %T: %v", err, err)
	}
	if _, err := tr.RemoveCertBinding(context.Background(), 1, "CERT"); !errors.As(err, &ce) {
		t.Errorf("RemoveCertBinding: expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// protoCustodianToInfo — unit tests
// ===========================================================================

// ===========================================================================
// Ceremony custodian round-trips (real gRPC server)
// ===========================================================================

// mockUserSvcWithCert extends the existing mockUserSvc with thumbprint and binding methods.
type mockUserSvcWithCert struct {
	mockUserSvc
	certThumbprintFn    func(ctx context.Context, thumbprint string) (*user.User, error)
	addCertBindingFn    func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
	removeCertBindingFn func(ctx context.Context, userID uint64, thumbprint string) (*user.User, error)
}

func (m *mockUserSvcWithCert) UserByCertThumbprint(ctx context.Context, thumbprint string) (*user.User, error) {
	if m.certThumbprintFn != nil {
		return m.certThumbprintFn(ctx, thumbprint)
	}
	return &user.User{ID: 1, Username: "cert-user"}, nil
}

func (m *mockUserSvcWithCert) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
	if m.addCertBindingFn != nil {
		return m.addCertBindingFn(ctx, userID, thumbprint, notAfterUnix)
	}
	return &user.User{ID: userID, Username: "cert-user"}, nil
}

func (m *mockUserSvcWithCert) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*user.User, error) {
	if m.removeCertBindingFn != nil {
		return m.removeCertBindingFn(ctx, userID, thumbprint)
	}
	return &user.User{ID: userID, Username: "cert-user"}, nil
}

var _ user.UserServicer = (*mockUserSvcWithCert)(nil)

func TestUnixRT_GetUserByCertThumbprint(t *testing.T) {
	usvc := &mockUserSvcWithCert{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	got, err := tr.GetUserByCertThumbprint(context.Background(), "AABBCC")
	if err != nil {
		t.Fatalf("GetUserByCertThumbprint: %v", err)
	}
	if got == nil || got.Username != "cert-user" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestUnixRT_AddCertBinding(t *testing.T) {
	usvc := &mockUserSvcWithCert{}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	got, err := tr.AddCertBinding(context.Background(), 1, "AABBCC", 0)
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if got == nil || got.ID != 1 {
		t.Errorf("unexpected result: %+v", got)
	}
}

// ===========================================================================
// Ceremony custodian round-trips
// ===========================================================================

// ===========================================================================
// CredentialStore / CredentialRetrieve round-trips
// ===========================================================================

func TestUnixRT_CredentialStore(t *testing.T) {
	var storedName string
	var storedValue string
	svc := &mockCredentialSvc{
		storeFn: func(_ context.Context, name string, value []byte) error {
			storedName = name
			storedValue = string(value)
			return nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if err := tr.CredentialStore(context.Background(), "mykey", []byte("myvalue")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	if storedName != "mykey" {
		t.Errorf("expected name mykey, got %q", storedName)
	}
	if storedValue != "myvalue" {
		t.Errorf("expected value myvalue, got %q", storedValue)
	}
}

func TestUnixRT_CredentialStore_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		storeFn: func(_ context.Context, _ string, _ []byte) error {
			return errors.New("store failed")
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if err := tr.CredentialStore(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_CredentialRetrieve(t *testing.T) {
	svc := &mockCredentialSvc{
		retrieveFn: func(_ context.Context, name string) ([]byte, error) {
			if name == "mykey" {
				return []byte("storedvalue"), nil
			}
			return nil, errors.New("not found")
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	val, err := tr.retrieveBytes(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("CredentialRetrieve: %v", err)
	}
	if string(val) != "storedvalue" {
		t.Errorf("expected storedvalue, got %q", string(val))
	}
}

func TestUnixRT_CredentialRetrieve_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		retrieveFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("not found")
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	if _, err := tr.retrieveBytes(context.Background(), "missing"); err == nil {
		t.Fatal("expected error from server")
	}
}

// ===========================================================================
// mockCeremonySvc bridge methods — ensure the existing mock satisfies the
// CeremonyServicer interface with custodian methods that return real data.
// The existing mock in transport_ceremony_roundtrip_test.go returns empty
// structs for the custodian methods; here we override for meaningful tests.
// ===========================================================================

// custodianCeremonySvc wraps mockCeremonySvc with non-trivial custodian stubs.
type custodianCeremonySvc struct {
	mockCeremonySvc
}

var _ service.CeremonyServicer = (*custodianCeremonySvc)(nil)

// TestUnixRT_RemoveCertBinding round-trips the withdrawal verb over the unix
// socket, asserting both arguments reach the service: a thumbprint that did
// not arrive withdraws nothing and reports a not-found (go-qrdb#518).
func TestUnixRT_RemoveCertBinding(t *testing.T) {
	var gotID uint64
	var gotThumbprint string
	usvc := &mockUserSvcWithCert{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			gotID, gotThumbprint = userID, tp
			return &user.User{ID: userID, Username: "cert-user"}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	got, err := tr.RemoveCertBinding(context.Background(), 7, "AABBCC")
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	if got == nil || got.Username != "cert-user" {
		t.Errorf("unexpected result: %+v", got)
	}
	if gotID != 7 || gotThumbprint != "AABBCC" {
		t.Fatalf("service saw (%d, %q), want (7, \"AABBCC\")", gotID, gotThumbprint)
	}
}
