//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTestNS = errors.New("test namespace error")

// TestIngestor_EnsureNamespaceCalledBeforeFirstUpsert verifies that Ingest calls
// EnsureNamespace before the first UpsertVector. If EnsureNamespace is not called,
// a namespace-not-found error would propagate from UpsertVector. With the call
// in place, the mock EnsureNamespace succeeds and upsert proceeds normally.
func TestIngestor_EnsureNamespaceCalledBeforeFirstUpsert(t *testing.T) {
	ensureCalled := false
	upsertCalled := false

	vecStore := &mockVecStore{
		ensureNamespaceFn: func(ctx context.Context, name string, dim int, metric string) error {
			ensureCalled = true
			assert.Equal(t, "my-docs", name)
			assert.Equal(t, 3, dim) // 3 floats per test embedding
			assert.Equal(t, "cosine", metric)
			return nil
		},
		upsertFn: func(ctx context.Context, req *UpsertVecRequest) (*UpsertVecResponse, error) {
			upsertCalled = true
			return &UpsertVecResponse{Success: true}, nil
		},
	}

	embProvider := newMockEmbProvider(nil)
	kvStore := newMockKVStore()

	ing := NewIngestor(embProvider, vecStore, kvStore, 0)

	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Namespace: "my-docs",
		Document: &Document{
			ID:   "doc1",
			Text: "Hello world this is a test.",
		},
	})
	require.NoError(t, err)
	assert.True(t, ensureCalled, "EnsureNamespace must be called before UpsertVector")
	assert.True(t, upsertCalled, "UpsertVector must be called after EnsureNamespace")
}

// TestIngestor_EnsureNamespaceIdempotent verifies that Ingest succeeds when
// EnsureNamespace returns success on each call (simulating repeated ingestion
// to the same namespace where the namespace already exists).
func TestIngestor_EnsureNamespaceIdempotent(t *testing.T) {
	callCount := 0
	vecStore := &mockVecStore{
		ensureNamespaceFn: func(ctx context.Context, name string, dim int, metric string) error {
			callCount++
			return nil // always succeeds (idempotent)
		},
	}

	embProvider := newMockEmbProvider(nil)
	kvStore := newMockKVStore()
	ing := NewIngestor(embProvider, vecStore, kvStore, 0)

	for i := 0; i < 3; i++ {
		_, err := ing.Ingest(context.Background(), &IngestRequest{
			Namespace: "docs",
			Document: &Document{
				ID:   "doc",
				Text: "some text to chunk and embed",
			},
		})
		require.NoError(t, err)
	}
	assert.Equal(t, 3, callCount, "EnsureNamespace called once per Ingest")
}

// TestIngestor_EnsureNamespaceError verifies that an EnsureNamespace failure
// stops ingestion and returns an EmbeddingError to the caller.
func TestIngestor_EnsureNamespaceError(t *testing.T) {
	vecStore := &mockVecStore{
		ensureNSErr: &EmbeddingError{Cause: errTestNS},
	}

	embProvider := newMockEmbProvider(nil)
	kvStore := newMockKVStore()
	ing := NewIngestor(embProvider, vecStore, kvStore, 0)

	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Namespace: "docs",
		Document: &Document{
			ID:   "doc1",
			Text: "some text",
		},
	})
	require.Error(t, err, "EnsureNamespace failure must propagate")
}
