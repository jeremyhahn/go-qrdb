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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd && !integration

package mcp

import (
	"encoding/json"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// WithEmbedding server option (0% coverage)
// ---------------------------------------------------------------------------

func TestServer_WithEmbedding(t *testing.T) {
	embSvc := &mockEmbeddingService{}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithEmbedding(embSvc))

	if s.embeddingSvc != embSvc {
		t.Error("embeddingSvc not set on server")
	}

	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_embed_text"]; !ok {
		t.Error("qrdb_embed_text not registered")
	}
}

// ---------------------------------------------------------------------------
// WithRAG server option (0% coverage) — also covers toolRAGIngest/toolRAGQuery
// ---------------------------------------------------------------------------

func TestServer_WithRAG(t *testing.T) {
	ragSvc := &mockRAGSvc{}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithRAG(ragSvc))

	if s.ragSvc != ragSvc {
		t.Error("ragSvc not set on server")
	}

	tools := s.MCPServer().ListTools()
	for _, name := range []string{
		"qrdb_rag_ingest",
		"qrdb_rag_query",
		"qrdb_rag_delete_document",
		"qrdb_rag_list_documents",
	} {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %s not registered", name)
		}
	}
}

// ---------------------------------------------------------------------------
// parseExactInt64 (50% coverage) — tests for uncovered branches
// ---------------------------------------------------------------------------

func TestParseExactInt64_MissingKey(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{})
	_, err := parseExactInt64(req, "start")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestParseExactInt64_Float64Normal(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": float64(1700000000000000000),
	})
	v, err := parseExactInt64(req, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v <= 0 {
		t.Errorf("expected positive value, got %d", v)
	}
}

func TestParseExactInt64_Float64MaxClamp(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": math.MaxFloat64,
	})
	v, err := parseExactInt64(req, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != math.MaxInt64 {
		t.Errorf("expected MaxInt64 (%d), got %d", int64(math.MaxInt64), v)
	}
}

func TestParseExactInt64_Float64MinClamp(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": -math.MaxFloat64,
	})
	v, err := parseExactInt64(req, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != math.MinInt64 {
		t.Errorf("expected MinInt64 (%d), got %d", int64(math.MinInt64), v)
	}
}

func TestParseExactInt64_String(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": "1234567890",
	})
	v, err := parseExactInt64(req, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 1234567890 {
		t.Errorf("expected 1234567890, got %d", v)
	}
}

func TestParseExactInt64_JSONNumber(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": json.Number("9876543210"),
	})
	v, err := parseExactInt64(req, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 9876543210 {
		t.Errorf("expected 9876543210, got %d", v)
	}
}

func TestParseExactInt64_Int64(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": int64(42),
	})
	v, err := parseExactInt64(req, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
}

func TestParseExactInt64_Int(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": 99,
	})
	v, err := parseExactInt64(req, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 99 {
		t.Errorf("expected 99, got %d", v)
	}
}

func TestParseExactInt64_DefaultCase_Error(t *testing.T) {
	// bool → %v → "true" → strconv.ParseInt fails with ErrSyntax.
	req := buildCallToolRequest(t, "test", map[string]any{
		"start": true,
	})
	_, err := parseExactInt64(req, "start")
	if err == nil {
		t.Fatal("expected error for bool input")
	}
}

// ---------------------------------------------------------------------------
// Server.Stop() without Start (branch coverage)
// ---------------------------------------------------------------------------

func TestServer_Stop_BeforeStart(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0")
	// Stop without calling Start should not panic.
	s.Stop()
}
