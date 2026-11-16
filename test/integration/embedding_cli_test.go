// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// embedding_cli_test.go drives the `qrdb embedding` command tree (embed,
// batch-embed, models) through the real client CLI binary (cmd/qrdb),
// against every transport (REST, gRPC, QUIC, MCP, Unix) using the shared
// startMultiProtocolNode harness (multi_protocol_helpers_test.go). This
// closes Batch D's embedding gap identified by the CLI coverage audit
// (previously zero coverage of any kind).
//
// EMPIRICALLY CONFIRMED STANDALONE CONTRACT (pkg/server/lifecycle/
// embedding_rag.go's buildEmbeddingAndRAG): the embedding service is only
// constructed when the daemon's YAML config carries at least one
// embedding.providers entry (an OpenAI-compatible HTTP endpoint --
// pkg/embedding's only providers are network-bound: openai, voyage, ollama,
// lmstudio -- there is no offline/deterministic local embedder in this
// codebase). startMultiProtocolNode's config carries no embedding section,
// so on this harness service.EmbeddingServicer is nil end to end, and every
// protocol server independently refuses to serve the embedding surface:
//   - REST:        the /v1/embeddings* routes are only registered when
//     embeddingSvc != nil (pkg/api/rest/server.go), so an unconfigured
//     server 404s.
//   - gRPC / Unix: Embed/BatchEmbed/ListModels always exist on the wire but
//     each checks embeddingSvc == nil first and returns
//     codes.Unimplemented with ServiceNotConfiguredError{Service:"embedding"}
//     ("grpc: embedding service is not configured") -- gRPC and Unix (grpc-
//     over-UDS) share the exact same qrdbServiceServer handler code path.
//   - QUIC:        handlers are only added to the dispatch table when
//     embeddingSvc != nil (pkg/api/quic/dispatch.go), so an unconfigured
//     server returns quicErrors's "quic: unknown method: embedding.<Verb>".
//   - MCP:         tools are only registered when embeddingSvc != nil
//     (pkg/api/mcp/server.go), so calling an unconfigured server's
//     qrdb_embed_text/qrdb_batch_embed/qrdb_list_embedding_models tool
//     fails at the mcp-go client's CallTool step, wrapped by
//     toolCallError ("mcp transport: tool <name> failed: ...").
//
// This is exactly the "documented NotImplemented/config-required" fallback
// contract the CLI coverage audit anticipated for embedding: this file
// asserts that contract on all 5 protocols rather than a positive
// text-to-vector round trip this codebase cannot perform standalone without
// live network egress (which integration tests must never require).
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestEmbedding_CLI_PerProtocol drives the full `qrdb embedding` command
// tree -- embed, batch-embed, models -- through the CLI binary against
// every transport sharing one multiProtoNode, asserting the documented
// "service not configured" contract each protocol enforces when no
// embedding.providers are configured.
func TestEmbedding_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testEmbeddingUnconfiguredOverProtocol(t, node, proto)
		})
	}
}

// embeddingNotConfiguredSubstring returns the protocol-specific substring
// this harness's unconfigured embedding service must surface, per the
// empirical contract documented in this file's header comment.
func embeddingNotConfiguredSubstring(proto multiProtocol) string {
	switch proto.name {
	case "rest":
		return "404"
	case "grpc", "unix":
		return "embedding service is not configured"
	case "quic":
		return "unknown method"
	case "mcp":
		return "failed"
	default:
		return ""
	}
}

// testEmbeddingUnconfiguredOverProtocol exercises the full embedding
// command tree against a single protocol, asserting every subcommand fails
// closed with the protocol's documented not-configured error and that
// client-side flag/argument validation still runs before any server round
// trip.
func testEmbeddingUnconfiguredOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	want := embeddingNotConfiguredSubstring(proto)

	// --- embed -------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "embedding", "embed", "hello world")
	if err == nil {
		t.Fatalf("[%s] embedding embed succeeded against an unconfigured embedding service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] embedding embed error = %q, want it to contain %q", proto.name, out, want)
	}

	out, err = runCLIProto(t, node, proto, "embedding", "embed", "hello", "world", "--model", "text-embedding-3-small", "--provider", "openai")
	if err == nil {
		t.Fatalf("[%s] embedding embed --model --provider succeeded against an unconfigured embedding service, got: %s", proto.name, out)
	}

	// cobra.MinimumNArgs(1): no text argument must fail client-side, before
	// ever reaching the (unconfigured) server.
	out, err = runCLIProto(t, node, proto, "embedding", "embed")
	if err == nil {
		t.Fatalf("[%s] embedding embed with no text argument succeeded, want a client-side arg error (output: %s)", proto.name, out)
	}

	// --- batch-embed ---------------------------------------------------
	out, err = runCLIProto(t, node, proto, "embedding", "batch-embed", "one", "two", "three", "--batch-size", "2")
	if err == nil {
		t.Fatalf("[%s] embedding batch-embed succeeded against an unconfigured embedding service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] embedding batch-embed error = %q, want it to contain %q", proto.name, out, want)
	}

	out, err = runCLIProto(t, node, proto, "embedding", "batch-embed")
	if err == nil {
		t.Fatalf("[%s] embedding batch-embed with no text arguments succeeded, want a client-side arg error (output: %s)", proto.name, out)
	}

	// --- models --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "embedding", "models")
	if err == nil {
		t.Fatalf("[%s] embedding models succeeded against an unconfigured embedding service, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
		t.Fatalf("[%s] embedding models error = %q, want it to contain %q", proto.name, out, want)
	}

	out, err = runCLIProto(t, node, proto, "embedding", "models", "--provider", "openai")
	if err == nil {
		t.Fatalf("[%s] embedding models --provider succeeded against an unconfigured embedding service, got: %s", proto.name, out)
	}
}

// fakeEmbeddingProvider is an OpenAI-compatible /embeddings endpoint. Each
// input text ends in "-<n>"; its embedding is [n], so a reordered result is
// visible to the caller. It records the largest input it was sent.
type fakeEmbeddingProvider struct {
	mu       sync.Mutex
	maxBatch int
	calls    int
}

func (f *fakeEmbeddingProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls++
	if len(req.Input) > f.maxBatch {
		f.maxBatch = len(req.Input)
	}
	f.mu.Unlock()

	type datum struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	}
	data := make([]datum, len(req.Input))
	for i, text := range req.Input {
		n, err := strconv.Atoi(text[strings.LastIndex(text, "-")+1:])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data[i] = datum{Embedding: []float32{float32(n)}, Index: i}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  data,
		"model": req.Model,
		"usage": map[string]int{"prompt_tokens": len(req.Input), "total_tokens": len(req.Input)},
	})
}

func (f *fakeEmbeddingProvider) snapshot() (calls, maxBatch int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.maxBatch
}

// TestEmbedding_CLI_BatchEmbedSplitsOversize_PerProtocol proves, on one node
// with an embedding provider configured and over every transport, that
// `qrdb embedding batch-embed` with more texts than --batch-size is split
// into provider calls of at most --batch-size texts and returns every
// embedding in input order, not refused.
func TestEmbedding_CLI_BatchEmbedSplitsOversize_PerProtocol(t *testing.T) {
	t.Parallel()
	fake := &fakeEmbeddingProvider{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cfg := fmt.Sprintf(`embedding:
  default_provider: default
  providers:
    - name: default
      base_url: %s
      model: test-embedding
`, srv.URL)
	node := startMultiProtocolNodeWithConfig(t, "mvcc", cfg)

	const texts, batchSize = 5, 2
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			args := []string{"--output", "json", "embedding", "batch-embed"}
			for i := 0; i < texts; i++ {
				// Per-protocol texts: the shared node's cache must not
				// answer one transport from another's run.
				args = append(args, fmt.Sprintf("%s-%d", proto.name, i))
			}
			args = append(args, "--batch-size", strconv.Itoa(batchSize))

			callsBefore, _ := fake.snapshot()
			out, err := runCLIProto(t, node, proto, args...)
			if err != nil {
				t.Fatalf("[%s] embedding batch-embed: %v (output: %s)", proto.name, err, out)
			}
			start := strings.Index(out, "{")
			if start < 0 {
				t.Fatalf("[%s] embedding batch-embed printed no JSON: %s", proto.name, out)
			}
			var got struct {
				Embeddings [][]float32 `json:"embeddings"`
			}
			if err := json.Unmarshal([]byte(out[start:]), &got); err != nil {
				t.Fatalf("[%s] decode batch-embed output: %v (output: %s)", proto.name, err, out)
			}
			if len(got.Embeddings) != texts {
				t.Fatalf("[%s] batch-embed returned %d embeddings, want %d", proto.name, len(got.Embeddings), texts)
			}
			for i, emb := range got.Embeddings {
				if len(emb) != 1 || emb[0] != float32(i) {
					t.Fatalf("[%s] embedding %d = %v, want [%d] (input order lost)", proto.name, i, emb, i)
				}
			}
			callsAfter, maxBatch := fake.snapshot()
			if maxBatch > batchSize {
				t.Fatalf("[%s] provider was sent %d texts in one call, want at most %d", proto.name, maxBatch, batchSize)
			}
			if callsAfter == callsBefore {
				t.Fatalf("[%s] batch-embed never reached the provider", proto.name)
			}
		})
	}
}
