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

// Tests for the RAG transport surface, which is only compiled under the
// qrdb_vector build tag (see transport_rag.go and transport/rag_types.go).

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRagChunkConfig(t *testing.T) {
	if got := ragChunkConfig(&transport.RAGIngestRequest{Text: "hello"}); got != nil {
		t.Errorf("expected nil for a request with no chunk options, got %+v", got)
	}

	got := ragChunkConfig(&transport.RAGIngestRequest{
		Text:          "hello",
		ChunkStrategy: "sentence",
		ChunkSize:     512,
		ChunkOverlap:  50,
		ChunkMinSize:  100,
	})
	want := service.ChunkConfig{Strategy: "sentence", ChunkSize: 512, Overlap: 50, MinSize: 100}
	if got == nil || *got != want {
		t.Errorf("ragChunkConfig = %+v, want %+v", got, want)
	}

	// Any one option alone is enough to produce a config.
	if got := ragChunkConfig(&transport.RAGIngestRequest{ChunkMinSize: 1}); got == nil || got.MinSize != 1 {
		t.Errorf("expected a config carrying MinSize=1, got %+v", got)
	}
}

func TestEmbeddedRAG_NilService(t *testing.T) {
	tr := newExtTransport(t)
	ctx := context.Background()
	calls := map[string]func() error{
		"RAGIngestDocument": func() error {
			_, err := tr.RAGIngestDocument(ctx, &transport.RAGIngestRequest{})
			return err
		},
		"RAGQuery": func() error {
			_, err := tr.RAGQuery(ctx, &transport.RAGQueryRequest{})
			return err
		},
		"RAGDeleteDocument": func() error {
			_, err := tr.RAGDeleteDocument(ctx, &transport.RAGDeleteDocumentRequest{})
			return err
		},
		"RAGListDocuments": func() error {
			_, err := tr.RAGListDocuments(ctx, &transport.RAGListDocumentsRequest{})
			return err
		},
	}
	for name, call := range calls {
		var ne *NilServiceError
		if err := call(); !errors.As(err, &ne) {
			t.Errorf("%s: expected NilServiceError, got %T: %v", name, err, err)
		}
	}
}
