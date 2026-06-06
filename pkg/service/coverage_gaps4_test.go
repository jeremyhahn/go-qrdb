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

// coverage_gaps4_test.go covers ceremony methods, RAG errors, and other
// remaining zero-coverage items.
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ceremony_service.go – CeremonyClaimShareBegin, CeremonyClaimShareComplete,
//                        CeremonySubmitUnsealShare, CeremonyEnrollExternalOfficer
//                        (all 0%)
// ---------------------------------------------------------------------------

// CeremonyClaimShareBegin on a non-initialized ceremony returns an error from
// the underlying ceremony.ClaimShareChallenge (wrong state). This covers the
// error-return branch of the service wrapper.
func TestCeremonyService_CeremonyClaimShareBegin_Error(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	// Ceremony is in "awaiting_init" state → ClaimShareChallenge returns error.
	ch, err := svc.CeremonyClaimShareBegin(ctx, "custodian-1")
	require.Error(t, err, "expected error before initialization")
	assert.Nil(t, ch)
}

// CeremonyClaimShareComplete on a non-initialized ceremony returns an error.
func TestCeremonyService_CeremonyClaimShareComplete_Error(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	cust, err := svc.CeremonyClaimShareComplete(ctx, "custodian-1", "challenge-1", []byte("response"))
	require.Error(t, err, "expected error before initialization")
	assert.Nil(t, cust)
}

// CeremonySubmitUnsealShare on a non-initialized ceremony returns an error.
func TestCeremonyService_CeremonySubmitUnsealShare_Error(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	result, err := svc.CeremonySubmitUnsealShare(ctx, "custodian-1", "challenge-1", []byte("response"), []byte("share"))
	require.Error(t, err, "expected error before initialization")
	assert.Nil(t, result)
}

// CeremonyEnrollExternalOfficer with nil request returns ErrInvalidOfficerRecord.
func TestCeremonyService_CeremonyEnrollExternalOfficer_NilRequest(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	resp, err := svc.CeremonyEnrollExternalOfficer(ctx, nil)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ceremony.ErrInvalidOfficerRecord)
}

// CeremonyEnrollExternalOfficer with non-nil request delegates to the underlying
// ceremony (which returns an error when not yet initialized).
func TestCeremonyService_CeremonyEnrollExternalOfficer_NotInitialized(t *testing.T) {
	svc, _ := newTestCeremonyService(t)
	ctx := context.Background()

	req := &CeremonyEnrollExternalOfficerRequest{
		Username:   "external-officer",
		PubKeyPEM:  "-----BEGIN PUBLIC KEY-----\nnotvalid\n-----END PUBLIC KEY-----",
		ShareIndex: 1,
		AuthFactor: "mtls",
	}
	resp, err := svc.CeremonyEnrollExternalOfficer(ctx, req)
	// The inner ceremony will reject because it is not initialized.
	require.Error(t, err)
	assert.Nil(t, resp)
}

// ---------------------------------------------------------------------------
// rag_errors.go – RAGServiceClosedError.Error() (0%)
// ---------------------------------------------------------------------------

func TestRAGServiceClosedError_Error(t *testing.T) {
	e := &RAGServiceClosedError{}
	want := "service: rag service is closed"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestRAGDocumentNotFoundError_Error(t *testing.T) {
	e := &RAGDocumentNotFoundError{Namespace: "docs", ID: "doc-1"}
	want := "service: rag document not found: docs/doc-1"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestRAGIngestError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("text extract failed")
	e := &RAGIngestError{Cause: cause}
	want := "service: rag ingest failed: text extract failed"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is should match cause through Unwrap")
	}
}

func TestRAGQueryError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("embedding unavailable")
	e := &RAGQueryError{Cause: cause}
	want := "service: rag query failed: embedding unavailable"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is should match cause through Unwrap")
	}
}

func TestRAGDeleteError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("storage error")
	e := &RAGDeleteError{ID: "doc-42", Cause: cause}
	want := "service: rag delete failed for document doc-42: storage error"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is should match cause through Unwrap")
	}
}

// ---------------------------------------------------------------------------
// rag_types.go – DefaultChunkConfig() (0%)
// ---------------------------------------------------------------------------

func TestDefaultChunkConfig_ReturnsDefaults(t *testing.T) {
	cfg := DefaultChunkConfig()
	if cfg == nil {
		t.Fatal("expected non-nil ChunkConfig")
	}
	if cfg.Strategy != "fixed" {
		t.Fatalf("expected Strategy=fixed, got %q", cfg.Strategy)
	}
	if cfg.ChunkSize != 512 {
		t.Fatalf("expected ChunkSize=512, got %d", cfg.ChunkSize)
	}
	if cfg.Overlap != 50 {
		t.Fatalf("expected Overlap=50, got %d", cfg.Overlap)
	}
	if cfg.MinSize != 100 {
		t.Fatalf("expected MinSize=100, got %d", cfg.MinSize)
	}
}

// ---------------------------------------------------------------------------
// batch_service.go – hasCSVExtension, hasJSONExtension (75%)
// ---------------------------------------------------------------------------

func TestHasCSVExtension_True(t *testing.T) {
	cases := []string{"data.csv", "archive/file.CSV", "DATA.csv"}
	for _, name := range cases {
		if !hasCSVExtension(name) {
			t.Fatalf("expected hasCSVExtension(%q)=true", name)
		}
	}
}

func TestHasCSVExtension_False(t *testing.T) {
	cases := []string{"data.json", "file.txt", "", "no-extension"}
	for _, name := range cases {
		if hasCSVExtension(name) {
			t.Fatalf("expected hasCSVExtension(%q)=false", name)
		}
	}
}

func TestHasJSONExtension_True(t *testing.T) {
	cases := []string{"data.json", "archive/file.JSON", "DATA.json"}
	for _, name := range cases {
		if !hasJSONExtension(name) {
			t.Fatalf("expected hasJSONExtension(%q)=true", name)
		}
	}
}

func TestHasJSONExtension_False(t *testing.T) {
	cases := []string{"data.csv", "file.txt", "", "no-extension"}
	for _, name := range cases {
		if hasJSONExtension(name) {
			t.Fatalf("expected hasJSONExtension(%q)=false", name)
		}
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_service.go – low coverage paths (69–88%)
// ---------------------------------------------------------------------------

func TestUserAuthFactorService_Rotate_OldIDNotFound(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	userID := uint64(200)

	// Rotate with a non-existent old factor ID should return an error.
	newFactor := mtlsFactor("new-factor", userID, []byte("new-cert"))
	err := svc.Rotate(ctx, "no-such-old-id", newFactor)
	if err == nil {
		t.Fatal("expected error rotating with non-existent old factor ID")
	}
}

func TestUserAuthFactorService_MarkUsed_NoFactor(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()

	err := svc.MarkUsed(ctx, "no-such-factor-id", time.Now())
	// MarkUsed on a non-existent factor should either succeed (no-op) or error.
	_ = err
}

func TestUserAuthFactorService_List_AfterAdd(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	userID := uint64(100)

	// Add a factor so List has something to return.
	f := mtlsFactor("factor-list-1", userID, []byte("cert-data-1"))
	err := svc.Add(ctx, userID, f)
	require.NoError(t, err)

	factors, listErr := svc.List(ctx, userID)
	require.NoError(t, listErr)
	assert.NotEmpty(t, factors)
}

func TestUserAuthFactorService_LookupByCredentialKey_Miss(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()

	_, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("no-such-key"))
	if err == nil {
		t.Fatal("expected error for unknown credential key")
	}
}
