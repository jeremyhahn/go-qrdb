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

// QUIC SDK transport coverage tests. These exercise:
//   1. Closed-state shortcuts for every transport method (the universal
//      pattern `if t.closed.Load() { return &transport.ClosedError{} }`).
//   2. Pure proto-to-transport conversion helpers that have no I/O.
//   3. Internal helpers (applyWriteHeaders / applyReadHeaders / call).

package quic

import (
	"bytes"
	"context"
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/collectionconv"
)

// ===========================================================================
// transport_mvcc.go — protoConsistency / protoWriteMode pass-through
// ===========================================================================

func TestQUICProtoConsistency_NoOpts(t *testing.T) {
	got := protoConsistency()
	if got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("protoConsistency() = %v, want UNSPECIFIED", got)
	}
}

func TestQUICProtoWriteMode_NoOpts(t *testing.T) {
	got := protoWriteMode()
	if got != pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		t.Errorf("protoWriteMode() = %v, want UNSPECIFIED", got)
	}
}

// ===========================================================================
// transport.go — Exists short-circuit
// ===========================================================================

func TestQUICExists_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.Exists(context.Background(), "k")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ClosedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// transport_collection.go — closed paths + protoToCollectionInfo helper
// ===========================================================================

func TestQUICCollection_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateCollection: %v", err)
	}
	if _, err := tr.GetCollectionInfo(ctx, "db", "c"); !errors.As(err, &ce) {
		t.Errorf("GetCollectionInfo: %v", err)
	}
	if _, err := tr.ListCollections(ctx, "db"); !errors.As(err, &ce) {
		t.Errorf("ListCollections: %v", err)
	}
	if err := tr.DropCollection(ctx, "db", "c"); !errors.As(err, &ce) {
		t.Errorf("DropCollection: %v", err)
	}
	if _, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{}); !errors.As(err, &ce) {
		t.Errorf("InsertDocument: %v", err)
	}
	if _, err := tr.GetDocument(ctx, "db", "c", 1); !errors.As(err, &ce) {
		t.Errorf("GetDocument: %v", err)
	}
	if err := tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateDocument: %v", err)
	}
	if err := tr.DeleteDocument(ctx, "db", "c", 1); !errors.As(err, &ce) {
		t.Errorf("DeleteDocument: %v", err)
	}
	if _, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListDocuments: %v", err)
	}
}

func TestQUICProtoToCollectionInfo_Valid(t *testing.T) {
	in := &pb.CollectionInfoResponse{
		Name:          "users",
		Database:      "main",
		IdStrategy:    "auto",
		FieldHash:     "_id",
		Schema:        map[string]string{"name": "string"},
		DocumentCount: 42,
		CreatedAt:     "2026-01-01T00:00:00Z",
	}
	out := collectionconv.ToCollectionInfo(in)
	if out == nil {
		t.Fatal("nil result")
	}
	if out.Name != "users" || out.Database != "main" {
		t.Errorf("identity mismatch: %+v", out)
	}
	if out.DocumentCount != 42 {
		t.Errorf("DocumentCount = %d, want 42", out.DocumentCount)
	}
}

// ===========================================================================
// transport_barrier.go — closed paths (the subset not in transport_closed_test)
// ===========================================================================

func TestQUICBarrierExtras_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierInitializeShamir: %v", err)
	}
	if _, err := tr.BarrierUnsealWithShare(ctx, &transport.BarrierUnsealShareRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierUnsealWithShare: %v", err)
	}
	if err := tr.BarrierUnsealWithShares(ctx, &transport.BarrierUnsealSharesRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierUnsealWithShares: %v", err)
	}
	if _, err := tr.BarrierRekey(ctx, &transport.BarrierRekeyRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierRekey: %v", err)
	}
	if _, err := tr.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if err := tr.BarrierRecoverWithKeys(ctx, &transport.BarrierRecoverWithKeysRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierRecoverWithKeys: %v", err)
	}
	if err := tr.BarrierDeleteRecoveryKeys(ctx); !errors.As(err, &ce) {
		t.Errorf("BarrierDeleteRecoveryKeys: %v", err)
	}
	if _, err := tr.BarrierHasRecoveryKeys(ctx); !errors.As(err, &ce) {
		t.Errorf("BarrierHasRecoveryKeys: %v", err)
	}
	if _, err := tr.BarrierGenerateRootToken(ctx, &transport.BarrierGenerateRootTokenRequest{}); !errors.As(err, &ce) {
		t.Errorf("BarrierGenerateRootToken: %v", err)
	}
}

// ===========================================================================
// transport_ceremony.go — closed paths
// ===========================================================================

func TestQUICCeremonyExtras_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.CeremonyClaimCertBegin(ctx, &transport.ClaimCertBeginRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyClaimCertBegin: %v", err)
	}
	if _, err := tr.CeremonyClaimCertComplete(ctx, &transport.ClaimCertCompleteRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyClaimCertComplete: %v", err)
	}
	if _, err := tr.CeremonyBeginClaimShare(ctx, &transport.BeginClaimShareRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyBeginClaimShare: %v", err)
	}
	if _, err := tr.CeremonyCompleteClaimShare(ctx, &transport.CompleteClaimShareRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyCompleteClaimShare: %v", err)
	}
	if _, err := tr.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonySignCSR: %v", err)
	}
	if _, err := tr.CeremonyEnrollExternalOfficer(ctx, &transport.EnrollExternalOfficerRequest{}); !errors.As(err, &ce) {
		t.Errorf("CeremonyEnrollExternalOfficer: %v", err)
	}
}

// ===========================================================================
// transport_auth.go — GetUserByUsername closed path
// ===========================================================================

func TestQUICGetUserByUsername_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	_, err := tr.GetUserByUsername(context.Background(), "t", "u")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("GetUserByUsername: %v", err)
	}
}

// ===========================================================================
// transport_user_auth_factor.go — closed paths + userFactorFromQUICWire
// ===========================================================================

func TestQUICUserAuthFactor_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{}); !errors.As(err, &ce) {
		t.Errorf("AddAuthFactor: %v", err)
	}
	if _, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListAuthFactors: %v", err)
	}
	if _, err := tr.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{}); !errors.As(err, &ce) {
		t.Errorf("GetAuthFactor: %v", err)
	}
	if _, err := tr.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{}); !errors.As(err, &ce) {
		t.Errorf("RemoveAuthFactor: %v", err)
	}
	if _, err := tr.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{}); !errors.As(err, &ce) {
		t.Errorf("RotateAuthFactor: %v", err)
	}
	if _, err := tr.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{}); !errors.As(err, &ce) {
		t.Errorf("RelabelAuthFactor: %v", err)
	}
	if _, err := tr.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{}); !errors.As(err, &ce) {
		t.Errorf("LoginAuthFactor: %v", err)
	}
}

func TestQUICUserFactorFromWire_Minimal(t *testing.T) {
	rec := &quicUserFactorRecord{
		ID:           "f1",
		UserID:       7,
		Type:         "webauthn",
		Label:        "yubikey",
		Credential:   []byte("cred"),
		Metadata:     map[string]string{"k": "v"},
		RegisteredAt: "2026-01-01T00:00:00Z",
		CreatedBy:    "admin",
	}
	out := userFactorFromQUICWire(rec)
	if out.ID != "f1" || out.UserID != 7 || out.Type != "webauthn" {
		t.Errorf("identity mismatch: %+v", out)
	}
	if !bytes.Equal(out.Credential, []byte("cred")) {
		t.Errorf("credential mismatch: %v", out.Credential)
	}
	if out.RegisteredAt.IsZero() {
		t.Errorf("expected parsed RegisteredAt")
	}
	if out.LastUsedAt != nil || out.RevokedAt != nil {
		t.Errorf("expected nil LastUsedAt and RevokedAt, got %+v / %+v", out.LastUsedAt, out.RevokedAt)
	}
}

func TestQUICUserFactorFromWire_AllTimestamps(t *testing.T) {
	rec := &quicUserFactorRecord{
		ID:           "f1",
		Type:         "totp",
		RegisteredAt: "2026-01-01T00:00:00Z",
		LastUsedAt:   "2026-01-02T00:00:00Z",
		RevokedAt:    "2026-01-03T00:00:00Z",
	}
	out := userFactorFromQUICWire(rec)
	if out.LastUsedAt == nil || out.RevokedAt == nil {
		t.Fatalf("expected non-nil timestamps, got %+v / %+v", out.LastUsedAt, out.RevokedAt)
	}
	if out.LastUsedAt.IsZero() || out.RevokedAt.IsZero() {
		t.Errorf("expected parsed timestamps")
	}
}

func TestQUICUserFactorFromWire_BadTimestamps(t *testing.T) {
	rec := &quicUserFactorRecord{
		ID:           "f1",
		Type:         "totp",
		RegisteredAt: "not-a-timestamp",
		LastUsedAt:   "also-bad",
		RevokedAt:    "still-bad",
	}
	out := userFactorFromQUICWire(rec)
	// Bad RegisteredAt simply becomes zero
	if !out.RegisteredAt.IsZero() {
		t.Errorf("expected zero RegisteredAt for bad input")
	}
	// LastUsedAt and RevokedAt are only set when parsing succeeds
	if out.LastUsedAt != nil {
		t.Errorf("expected nil LastUsedAt for bad input, got %+v", out.LastUsedAt)
	}
	if out.RevokedAt != nil {
		t.Errorf("expected nil RevokedAt for bad input, got %+v", out.RevokedAt)
	}
}

// ===========================================================================
// transport_lock.go — protoToLockInfo helper
// ===========================================================================

func TestQUICProtoToLockInfo_Nil(t *testing.T) {
	if got := protoToLockInfo(nil); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestQUICProtoToLockInfo_Valid(t *testing.T) {
	in := &pb.LockInfo{
		Key:          "lock-a",
		FencingToken: 99,
		LeaseId:      7,
		HolderId:     "node-1",
		Reentrant:    true,
		AcquireCount: 3,
		Ttl:          30,
		AcquiredAt:   1_700_000_000,
	}
	out := protoToLockInfo(in)
	if out == nil {
		t.Fatal("nil result")
	}
	if out.Key != "lock-a" || out.FencingToken != 99 || out.HolderID != "node-1" {
		t.Errorf("lock identity mismatch: %+v", out)
	}
	if !out.Reentrant || out.AcquireCount != 3 || out.TTL != 30 {
		t.Errorf("lock detail mismatch: %+v", out)
	}
}

// ===========================================================================
// transport_mvcc.go — closed paths for less-covered methods
// ===========================================================================

func TestQUICMVCCExtras_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.ExecuteTransaction(ctx, &transport.TransactionRequest{}); !errors.As(err, &ce) {
		t.Errorf("ExecuteTransaction: %v", err)
	}
	if _, err := tr.ExecuteBatch(ctx, []transport.Op{}); !errors.As(err, &ce) {
		t.Errorf("ExecuteBatch: %v", err)
	}
}

// ===========================================================================
// transport.go — Request always returns NotImplemented
// ===========================================================================

func TestQUICRequest_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	err := tr.Request(context.Background(), "kv.Put", nil, nil)
	// Even closed, Request returns NotImplementedError because the QUIC
	// transport doesn't implement generic Request.
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Errorf("expected NotImplementedError, got %T: %v", err, err)
	}
}

// ===========================================================================
// transport.go — stripScheme private helper
// ===========================================================================

func TestQUICStripScheme(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://host:1", "host:1"},
		{"http://host:1", "host:1"},
		{"host:1", "host:1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := stripScheme(tc.in); got != tc.want {
			t.Errorf("stripScheme(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ===========================================================================
// errors.go — typed error formatting
// ===========================================================================

func TestQUICMarshalError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &marshalError{Err: inner}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !errors.Is(e, inner) {
		t.Error("Unwrap chain broken")
	}
}

func TestQUICUnmarshalError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &unmarshalError{Err: inner}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !errors.Is(e, inner) {
		t.Error("Unwrap chain broken")
	}
}
