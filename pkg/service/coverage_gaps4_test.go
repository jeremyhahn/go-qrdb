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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

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
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()
	userID := uint64(200)

	// Rotate with a non-existent old factor ID should return an error.
	newFactor := mtlsFactor("new-factor", userID, []byte("new-cert"))
	err := svc.Rotate(ctx, "no-such-old-id", newFactor)
	if err == nil {
		t.Fatal("expected error rotating with non-existent old factor ID")
	}
}

func TestUserAuthFactorService_List_AfterAdd(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
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
	svc, _ := newTestUserAuthFactorService(t, newStubUserStateUpdater())
	ctx := context.Background()

	_, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("no-such-key"))
	if err == nil {
		t.Fatal("expected error for unknown credential key")
	}
}
