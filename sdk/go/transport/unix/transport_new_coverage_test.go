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
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
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

func TestUnixCeremonyCustodian_AfterClose(t *testing.T) {
	tr := newClosed()
	var ce *transport.ClosedError

	if _, err := tr.CeremonyClaimShareBegin(context.Background(), &transport.CeremonyClaimShareBeginRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyClaimShareBegin: expected ClosedError, got %T: %v", err, err)
	}
	if _, err := tr.CeremonyClaimShareComplete(context.Background(), &transport.CeremonyClaimShareCompleteRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyClaimShareComplete: expected ClosedError, got %T: %v", err, err)
	}
	if _, err := tr.CeremonySubmitUnsealShare(context.Background(), &transport.CeremonySubmitUnsealShareRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonySubmitUnsealShare: expected ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// ClosedError guards — credentials CredentialStore / CredentialRetrieve
// ===========================================================================

func TestUnixCredentialStorageOps_AfterClose(t *testing.T) {
	tr := newClosed()
	var ce *transport.ClosedError

	if err := tr.CredentialStore(context.Background(), "k", []byte("v")); !errors.As(err, &ce) {
		t.Errorf("CredentialStore: expected ClosedError, got %T: %v", err, err)
	}
	if _, err := tr.CredentialRetrieve(context.Background(), "k"); !errors.As(err, &ce) {
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
}

// ===========================================================================
// protoCustodianToInfo — unit tests
// ===========================================================================

func TestProtoCustodianToInfo_Nil(t *testing.T) {
	out := protoCustodianToInfo(nil)
	if out == nil {
		t.Fatal("expected non-nil result for nil input")
	}
	// All fields should be zero.
	if out.ID != "" || out.Username != "" {
		t.Errorf("expected empty result, got %+v", out)
	}
}

func TestProtoCustodianToInfo_Full(t *testing.T) {
	now := time.Now().Unix()
	in := &pb.CeremonyCustodianResponse{
		Id:                 "cust-1",
		Username:           "alice",
		TenantId:           "t1",
		Method:             "totp",
		EnrollmentId:       "enroll-1",
		PublicKeyPem:       "-----BEGIN PUBLIC KEY-----",
		ShareIndex:         3,
		EnrolledAtUnix:     now,
		ShareClaimed:       true,
		RevokedAtUnix:      now + 100,
		ShareClaimedAtUnix: now + 50,
	}
	out := protoCustodianToInfo(in)
	if out.ID != "cust-1" {
		t.Errorf("ID: got %q want cust-1", out.ID)
	}
	if out.Username != "alice" {
		t.Errorf("Username: got %q want alice", out.Username)
	}
	if out.TenantID != "t1" {
		t.Errorf("TenantID: got %q want t1", out.TenantID)
	}
	if out.Method != "totp" {
		t.Errorf("Method: got %q want totp", out.Method)
	}
	if out.ShareIndex != 3 {
		t.Errorf("ShareIndex: got %d want 3", out.ShareIndex)
	}
	if !out.ShareClaimed {
		t.Error("expected ShareClaimed=true")
	}
	if out.RevokedAt == nil {
		t.Error("expected RevokedAt to be set")
	}
	if out.ShareClaimedAt == nil {
		t.Error("expected ShareClaimedAt to be set")
	}
}

func TestProtoCustodianToInfo_ZeroOptionals(t *testing.T) {
	in := &pb.CeremonyCustodianResponse{
		Id:                 "c1",
		RevokedAtUnix:      0, // zero means unset
		ShareClaimedAtUnix: 0,
	}
	out := protoCustodianToInfo(in)
	if out.RevokedAt != nil {
		t.Errorf("expected nil RevokedAt for zero unix, got %v", out.RevokedAt)
	}
	if out.ShareClaimedAt != nil {
		t.Errorf("expected nil ShareClaimedAt for zero unix, got %v", out.ShareClaimedAt)
	}
}

// ===========================================================================
// Ceremony custodian round-trips (real gRPC server)
// ===========================================================================

// mockUserSvcWithCert extends the existing mockUserSvc with thumbprint and binding methods.
type mockUserSvcWithCert struct {
	mockUserSvc
	certThumbprintFn func(ctx context.Context, thumbprint string) (*user.User, error)
	addCertBindingFn func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
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

func TestUnixRT_CeremonyClaimShareBegin(t *testing.T) {
	svc := &mockCeremonySvc{} // uses existing mock which implements CeremonyClaimShareBegin
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	resp, err := tr.CeremonyClaimShareBegin(context.Background(), &transport.CeremonyClaimShareBeginRequest{
		CustodianID: "cust-1",
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareBegin: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestUnixRT_CeremonyClaimShareComplete(t *testing.T) {
	svc := &mockCeremonySvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	resp, err := tr.CeremonyClaimShareComplete(context.Background(), &transport.CeremonyClaimShareCompleteRequest{
		CustodianID:    "cust-1",
		ChallengeID:    "ch-1",
		FactorResponse: []byte("response"),
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareComplete: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestUnixRT_CeremonySubmitUnsealShare(t *testing.T) {
	svc := &mockCeremonySvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	resp, err := tr.CeremonySubmitUnsealShare(context.Background(), &transport.CeremonySubmitUnsealShareRequest{
		CustodianID:    "cust-1",
		ChallengeID:    "ch-1",
		FactorResponse: []byte("response"),
		ShareBytes:     []byte("share"),
	})
	if err != nil {
		t.Fatalf("CeremonySubmitUnsealShare: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

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

	val, err := tr.CredentialRetrieve(context.Background(), "mykey")
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

	if _, err := tr.CredentialRetrieve(context.Background(), "missing"); err == nil {
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
	claimBeginFn    func(ctx context.Context, custodianID string) (*service.CeremonyCustodianChallenge, error)
	claimCompleteFn func(ctx context.Context, custodianID, challengeID string, factorResponse []byte) (*custodian.Custodian, error)
	submitShareFn   func(ctx context.Context, custodianID, challengeID string, factorResponse, shareBytes []byte) (*service.CeremonyUnsealShareResult, error)
}

func (c *custodianCeremonySvc) CeremonyClaimShareBegin(ctx context.Context, custodianID string) (*service.CeremonyCustodianChallenge, error) {
	if c.claimBeginFn != nil {
		return c.claimBeginFn(ctx, custodianID)
	}
	return &service.CeremonyCustodianChallenge{CustodianID: custodianID, Nonce: []byte("deadbeef")}, nil
}

func (c *custodianCeremonySvc) CeremonyClaimShareComplete(ctx context.Context, custodianID, challengeID string, factorResponse []byte) (*custodian.Custodian, error) {
	if c.claimCompleteFn != nil {
		return c.claimCompleteFn(ctx, custodianID, challengeID, factorResponse)
	}
	return &custodian.Custodian{ID: custodianID, Username: "custodian-user"}, nil
}

func (c *custodianCeremonySvc) CeremonySubmitUnsealShare(ctx context.Context, custodianID, challengeID string, factorResponse, shareBytes []byte) (*service.CeremonyUnsealShareResult, error) {
	if c.submitShareFn != nil {
		return c.submitShareFn(ctx, custodianID, challengeID, factorResponse, shareBytes)
	}
	return &service.CeremonyUnsealShareResult{Submitted: 1, Required: 3, Unsealed: false}, nil
}

var _ service.CeremonyServicer = (*custodianCeremonySvc)(nil)

func TestUnixRT_CeremonyClaimShareBegin_WithData(t *testing.T) {
	svc := &custodianCeremonySvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	resp, err := tr.CeremonyClaimShareBegin(context.Background(), &transport.CeremonyClaimShareBeginRequest{
		CustodianID: "cust-42",
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareBegin: %v", err)
	}
	if resp.CustodianID != "cust-42" {
		t.Errorf("expected CustodianID cust-42, got %q", resp.CustodianID)
	}
}

func TestUnixRT_CeremonySubmitUnsealShare_WithData(t *testing.T) {
	svc := &custodianCeremonySvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	resp, err := tr.CeremonySubmitUnsealShare(context.Background(), &transport.CeremonySubmitUnsealShareRequest{
		CustodianID:    "cust-42",
		ChallengeID:    "ch-1",
		FactorResponse: []byte("response"),
		ShareBytes:     []byte("share"),
	})
	if err != nil {
		t.Fatalf("CeremonySubmitUnsealShare: %v", err)
	}
	if resp.Required != 3 {
		t.Errorf("expected Required=3, got %d", resp.Required)
	}
}
