//go:build qrdb_vector

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

// Coverage tests for the RAG transport surface, which is only compiled under
// the qrdb_vector build tag (see transport_rag.go and transport/rag_types.go).
// They live in their own file because the RAG methods compile only under the
// qrdb_vector tag.

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// ragChunkConfig pure function
// ===========================================================================

func TestRagChunkConfig_Nil(t *testing.T) {
	req := &transport.RAGIngestRequest{Text: "hello"}
	got := ragChunkConfig(req)
	if got != nil {
		t.Errorf("expected nil for default request, got %+v", got)
	}
}

func TestRagChunkConfig_NonNil(t *testing.T) {
	req := &transport.RAGIngestRequest{
		Text:          "hello",
		ChunkStrategy: "sentence",
		ChunkSize:     512,
		ChunkOverlap:  50,
		ChunkMinSize:  100,
	}
	got := ragChunkConfig(req)
	if got == nil {
		t.Fatal("expected non-nil ChunkConfig")
	}
	if got.Strategy != "sentence" {
		t.Errorf("expected Strategy=sentence, got %q", got.Strategy)
	}
	if got.ChunkSize != 512 {
		t.Errorf("expected ChunkSize=512, got %d", got.ChunkSize)
	}
}

// ===========================================================================
// RAG nil-service guards
// ===========================================================================

func TestEmbedded_RAGIngestDocument_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_RAGQuery_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_RAGDeleteDocument_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbedded_RAGListDocuments_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}
