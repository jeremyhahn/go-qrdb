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

package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// thousandRunes is a 1000-rune document with no sentence or paragraph
// boundary, so the fixed chunker's count follows from size and overlap alone:
// ceil((1000 - size) / (size - overlap)) + 1.
var thousandRunes = strings.Repeat("abcdefghij", 100)

// buildRAGDefaultsNode builds a node whose RAG service is configured by
// ragCfg, over the fake embedding server.
func buildRAGDefaultsNode(t *testing.T, ragCfg qrdbconfig.RAGConfig) (*lifecycle.Node, error) {
	t.Helper()
	srv := fakeEmbeddingServer(t)
	t.Cleanup(srv.Close)

	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })

	hub := watch.NewHub()
	t.Cleanup(func() { hub.Close() })

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
		RAGConfig: ragCfg,
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	bindNodeRAG(node)
	return node, nil
}

func ragDefaultsNode(t *testing.T, ragCfg qrdbconfig.RAGConfig) *lifecycle.Node {
	t.Helper()
	node, err := buildRAGDefaultsNode(t, ragCfg)
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	return node
}

// ingestChunkCount ingests text under cfg and returns the chunk count.
func ingestChunkCount(t *testing.T, node *lifecycle.Node, docID, text string, cfg *service.ChunkConfig) int {
	t.Helper()
	resp, err := node.RAG.IngestDocument(context.Background(), &service.IngestDocumentRequest{
		DocumentID:  docID,
		Text:        text,
		Namespace:   "defaults-ns",
		Model:       "fake-model",
		Provider:    "fake",
		ChunkConfig: cfg,
	})
	if err != nil {
		t.Fatalf("IngestDocument %s: %v", docID, err)
	}
	return resp.ChunkCount
}

func TestRAGConfig_DefaultChunkSize(t *testing.T) {
	node := ragDefaultsNode(t, qrdbconfig.RAGConfig{DefaultChunkSize: 250})

	// Size 250, overlap 50: a step of 200 starts five chunks.
	if got := ingestChunkCount(t, node, "size-default", thousandRunes,
		&service.ChunkConfig{Strategy: "fixed", Overlap: 50, MinSize: 1}); got != 5 {
		t.Errorf("unset chunk size: %d chunks, want 5 from default_chunk_size 250", got)
	}
	// Size 500, overlap 50: a step of 450 starts three.
	if got := ingestChunkCount(t, node, "size-explicit", thousandRunes,
		&service.ChunkConfig{Strategy: "fixed", ChunkSize: 500, Overlap: 50, MinSize: 1}); got != 3 {
		t.Errorf("explicit chunk size 500: %d chunks, want 3", got)
	}
}

func TestRAGConfig_DefaultOverlap(t *testing.T) {
	node := ragDefaultsNode(t, qrdbconfig.RAGConfig{DefaultOverlap: 150})

	// Size 250, overlap 150: a step of 100 starts nine chunks.
	if got := ingestChunkCount(t, node, "overlap-default", thousandRunes,
		&service.ChunkConfig{Strategy: "fixed", ChunkSize: 250, MinSize: 1}); got != 9 {
		t.Errorf("unset overlap: %d chunks, want 9 from default_overlap 150", got)
	}
	// Size 250, overlap 50: a step of 200 starts five.
	if got := ingestChunkCount(t, node, "overlap-explicit", thousandRunes,
		&service.ChunkConfig{Strategy: "fixed", ChunkSize: 250, Overlap: 50, MinSize: 1}); got != 5 {
		t.Errorf("explicit overlap 50: %d chunks, want 5", got)
	}
}

func TestRAGConfig_DefaultChunkStrategy(t *testing.T) {
	node := ragDefaultsNode(t, qrdbconfig.RAGConfig{DefaultChunkStrategy: "paragraph"})

	// Five 10-rune paragraphs, 60 runes in all: the paragraph strategy keeps
	// each paragraph, the fixed one fits the whole text in one chunk.
	paragraphs := strings.Repeat("ten runes.\n\n", 5)
	cfg := func(strategy string) *service.ChunkConfig {
		return &service.ChunkConfig{Strategy: strategy, ChunkSize: 250, Overlap: 50, MinSize: 1}
	}
	if got := ingestChunkCount(t, node, "strategy-default", paragraphs, cfg("")); got != 5 {
		t.Errorf("unset strategy: %d chunks, want 5 from default_chunk_strategy paragraph", got)
	}
	if got := ingestChunkCount(t, node, "strategy-explicit", paragraphs, cfg("fixed")); got != 1 {
		t.Errorf("explicit strategy fixed: %d chunks, want 1", got)
	}
}

func TestRAGConfig_DefaultTopK(t *testing.T) {
	node := ragDefaultsNode(t, qrdbconfig.RAGConfig{DefaultTopK: 2})
	chunks := ingestChunkCount(t, node, "topk", thousandRunes,
		&service.ChunkConfig{Strategy: "fixed", ChunkSize: 250, Overlap: 50, MinSize: 1})
	if chunks != 5 {
		t.Fatalf("ingest made %d chunks, want 5", chunks)
	}

	query := func(topK int) int {
		t.Helper()
		resp, err := node.RAG.Query(context.Background(), &service.RAGQueryRequest{
			Query:     "abcdefghij",
			Namespace: "defaults-ns",
			TopK:      topK,
			Model:     "fake-model",
			Provider:  "fake",
		})
		if err != nil {
			t.Fatalf("Query top-k %d: %v", topK, err)
		}
		return len(resp.Results)
	}
	if got := query(0); got != 2 {
		t.Errorf("unset top-k: %d results, want 2 from default_top_k", got)
	}
	if got := query(4); got != 4 {
		t.Errorf("explicit top-k 4: %d results, want 4", got)
	}
}

func TestRAGConfig_ChunkDefaultsRefusedAtStartup(t *testing.T) {
	cases := map[string]qrdbconfig.RAGConfig{
		"overlap not below size": {DefaultChunkSize: 40, DefaultOverlap: 40},
		"unknown strategy":       {DefaultChunkStrategy: "semantic"},
	}
	for name, ragCfg := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := buildRAGDefaultsNode(t, ragCfg)
			var defaultsErr *lifecycle.RAGChunkDefaultsError
			if !errors.As(err, &defaultsErr) {
				t.Fatalf("BuildServices error = %v, want *RAGChunkDefaultsError", err)
			}
		})
	}
}
