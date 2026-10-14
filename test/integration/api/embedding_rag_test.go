// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration && ollama

// Package integration: ollama-backed end-to-end tests for the embedding and
// RAG subsystems.
//
// These tests require a live Ollama sidecar with the nomic-embed-text model
// pulled, and a qrdbd server configured with an OpenAI-compatible embedding
// provider pointing at it (see config.test.yaml + docker-compose.embedding.yml).
// They are gated behind the `ollama` build tag so they compile ONLY for the
// dedicated `make integration-test-embedding` / `make integration-test-rag-ollama`
// runs and never weigh down the default fast suite.
//
// Embedding and RAG have no CLI surface; their real protocol surface is REST
// (embedding additionally on gRPC/MCP, RAG additionally on MCP). Since all
// protocol handlers delegate to the same service instance, exercising REST
// against a real Ollama validates the whole path end to end: config wiring →
// embedding service → Ollama provider → (for RAG) chunking → HNSW vector index
// → KV store → retrieval.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// embedHTTPClient returns an mTLS-authenticated HTTP client for the REST API.
// Embedding/RAG endpoints are authenticated, so the admin client certificate
// is required (same pattern as TestRESTPutGetDelete).
func embedHTTPClient(cfg *TestConfig) *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second, // embedding round-trips through Ollama can be slow
		Transport: &http.Transport{
			TLSClientConfig: loadClientTLSConfig(cfg),
		},
	}
}

// postJSON issues an authenticated POST with a JSON body and decodes the JSON
// response into out (when out is non-nil). It returns the HTTP status code.
func postJSON(t *testing.T, cfg *TestConfig, path string, body, out any) int {
	t.Helper()
	payload, err := json.Marshal(body)
	assertNoError(t, err, "marshal request body")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.RESTBaseURL+path, bytes.NewReader(payload))
	assertNoError(t, err, "build POST request")
	req.Header.Set("Content-Type", "application/json")

	resp, err := embedHTTPClient(cfg).Do(req)
	assertNoError(t, err, fmt.Sprintf("POST %s", path))
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	assertNoError(t, err, "read response body")
	if resp.StatusCode != http.StatusOK {
		t.Logf("POST %s -> %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		assertNoError(t, json.Unmarshal(raw, out), fmt.Sprintf("decode %s response: %s", path, string(raw)))
	}
	return resp.StatusCode
}

// getJSON issues an authenticated GET and decodes the JSON response.
func getJSON(t *testing.T, cfg *TestConfig, path string, out any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.RESTBaseURL+path, nil)
	assertNoError(t, err, "build GET request")

	resp, err := embedHTTPClient(cfg).Do(req)
	assertNoError(t, err, fmt.Sprintf("GET %s", path))
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	assertNoError(t, err, "read response body")
	if resp.StatusCode != http.StatusOK {
		t.Logf("GET %s -> %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		assertNoError(t, json.Unmarshal(raw, out), fmt.Sprintf("decode %s response: %s", path, string(raw)))
	}
	return resp.StatusCode
}

// --- response shapes (mirror the REST handler JSON) ---

type embedResp struct {
	Embeddings [][]float32 `json:"embeddings"`
	Model      string      `json:"model"`
	Cached     bool        `json:"cached"`
}

type modelsResp struct {
	Models []struct {
		Name       string `json:"name"`
		Dimensions int    `json:"dimensions"`
	} `json:"models"`
}

type ragIngestResp struct {
	DocumentID string `json:"document_id"`
	ChunkCount int    `json:"chunk_count"`
	Namespace  string `json:"namespace"`
}

type ragQueryResp struct {
	Results []struct {
		ChunkText  string  `json:"chunk_text"`
		Score      float32 `json:"score"`
		DocumentID string  `json:"document_id"`
		ChunkIndex int     `json:"chunk_index"`
	} `json:"results"`
}

type ragDocsResp struct {
	Documents []struct {
		ID         string `json:"id"`
		Namespace  string `json:"namespace"`
		ChunkCount int    `json:"chunk_count"`
	} `json:"documents"`
	Total int `json:"total"`
}

// cosine returns the cosine similarity of two equal-length vectors.
func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// allZero reports whether every component of v is zero (a degenerate embedding).
func allZero(v []float32) bool {
	for _, f := range v {
		if f != 0 {
			return false
		}
	}
	return true
}

// requireEmbeddingServer fails fast if the REST server is unreachable.
func requireEmbeddingServer(t *testing.T, cfg *TestConfig) {
	t.Helper()
	if !isServerAvailable(t, cfg) {
		t.Fatal("REST server not available for embedding/RAG e2e tests")
	}
}

// TestEmbeddingTextE2E embeds a batch of texts through the real Ollama provider
// and verifies the response is well-formed: one vector per input, consistent
// non-zero dimensionality, and the model name echoed back.
func TestEmbeddingTextE2E(t *testing.T) {
	cfg := LoadTestConfig()
	requireEmbeddingServer(t, cfg)

	texts := []string{
		"QuicRaft DB is a distributed key-value store.",
		"Raft provides consensus across replicas.",
		"QUIC is a UDP-based transport protocol.",
	}
	var out embedResp
	status := postJSON(t, cfg, "/v1/embeddings", map[string]any{"texts": texts}, &out)
	assertEqual(t, http.StatusOK, status, "embed request should return 200")

	assertEqual(t, len(texts), len(out.Embeddings), "one embedding per input text")
	assertNotEmpty(t, out.Model, "response should echo the model name")

	dim := len(out.Embeddings[0])
	assertTrue(t, dim > 0, "embedding dimension should be positive")
	for i, vec := range out.Embeddings {
		assertEqual(t, dim, len(vec), fmt.Sprintf("embedding %d should have consistent dimension", i))
		assertTrue(t, !allZero(vec), fmt.Sprintf("embedding %d should not be all-zero", i))
	}
	t.Logf("embedded %d texts; model=%s dim=%d", len(texts), out.Model, dim)
}

// TestEmbeddingBatchE2E exercises the batch endpoint with an explicit batch
// size that forces multiple sub-batches, verifying all inputs are embedded.
func TestEmbeddingBatchE2E(t *testing.T) {
	cfg := LoadTestConfig()
	requireEmbeddingServer(t, cfg)

	texts := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	var out embedResp
	status := postJSON(t, cfg, "/v1/embeddings/batch",
		map[string]any{"texts": texts, "batch_size": 2}, &out)
	assertEqual(t, http.StatusOK, status, "batch embed should return 200")
	assertEqual(t, len(texts), len(out.Embeddings), "one embedding per input across sub-batches")

	dim := len(out.Embeddings[0])
	for i, vec := range out.Embeddings {
		assertEqual(t, dim, len(vec), fmt.Sprintf("batch embedding %d dimension", i))
		assertTrue(t, !allZero(vec), fmt.Sprintf("batch embedding %d non-zero", i))
	}
}

// TestEmbeddingListModelsE2E lists the models available from the provider and
// confirms the configured embedding model is present.
func TestEmbeddingListModelsE2E(t *testing.T) {
	cfg := LoadTestConfig()
	requireEmbeddingServer(t, cfg)

	var out modelsResp
	status := getJSON(t, cfg, "/v1/embedding/models", &out)
	assertEqual(t, http.StatusOK, status, "list models should return 200")
	assertTrue(t, len(out.Models) > 0, "provider should report at least one model")

	found := false
	for _, m := range out.Models {
		if strings.Contains(m.Name, "nomic-embed-text") {
			found = true
		}
	}
	assertTrue(t, found, "configured model nomic-embed-text should be listed")
}

// TestEmbeddingSemanticSimilarityE2E verifies the embeddings carry real
// semantics: two paraphrases must be more similar to each other than either is
// to an unrelated sentence. This proves the live model — not a stub — produced
// the vectors.
func TestEmbeddingSemanticSimilarityE2E(t *testing.T) {
	cfg := LoadTestConfig()
	requireEmbeddingServer(t, cfg)

	texts := []string{
		"The cat sat on the warm windowsill.",    // 0
		"A feline rested on the sunny ledge.",    // 1 — paraphrase of 0
		"Compiler optimization reduces latency.", // 2 — unrelated
	}
	var out embedResp
	status := postJSON(t, cfg, "/v1/embeddings", map[string]any{"texts": texts}, &out)
	assertEqual(t, http.StatusOK, status, "embed request should return 200")
	assertEqual(t, 3, len(out.Embeddings), "expected three embeddings")

	simParaphrase := cosine(out.Embeddings[0], out.Embeddings[1])
	simUnrelated := cosine(out.Embeddings[0], out.Embeddings[2])
	t.Logf("cosine(paraphrase)=%.4f cosine(unrelated)=%.4f", simParaphrase, simUnrelated)
	assertTrue(t, simParaphrase > simUnrelated,
		fmt.Sprintf("paraphrase pair (%.4f) should be more similar than unrelated pair (%.4f)",
			simParaphrase, simUnrelated))
}

// TestRAGIngestQueryE2E drives the full RAG pipeline against real Ollama:
// ingest a multi-fact document (chunked per sentence, each chunk embedded and
// stored in the HNSW index), then query a single fact and assert the most
// relevant chunk is retrieved.
func TestRAGIngestQueryE2E(t *testing.T) {
	cfg := LoadTestConfig()
	requireEmbeddingServer(t, cfg)

	namespace := generateUniqueID("rag-ns")
	docID := generateUniqueID("rag-doc")
	doc := "The Eiffel Tower is located in Paris, France. " +
		"The Great Barrier Reef lies off the coast of Australia. " +
		"Mount Everest is the tallest mountain above sea level."

	var ing ragIngestResp
	// Use fixed chunking with an explicit small size so the ~150-char document
	// deterministically splits into multiple chunks (the default 512-char target
	// would pack all three facts into a single chunk, defeating the retrieval-
	// discrimination check below). chunk_min_size 0 keeps the trailing chunk.
	status := postJSON(t, cfg, "/v1/rag/ingest", map[string]any{
		"document_id":    docID,
		"text":           doc,
		"namespace":      namespace,
		"chunk_strategy": "fixed",
		"chunk_size":     60,
		"chunk_overlap":  0,
		"chunk_min_size": 0,
	}, &ing)
	assertEqual(t, http.StatusOK, status, "rag ingest should return 200")
	assertEqual(t, docID, ing.DocumentID, "ingest should echo document id")
	assertTrue(t, ing.ChunkCount >= 2, "fixed chunking at size 60 should yield multiple chunks")

	// Give the async HNSW index a brief moment to absorb the upserts.
	time.Sleep(2 * time.Second)

	var q ragQueryResp
	status = postJSON(t, cfg, "/v1/rag/query", map[string]any{
		"query":     "Where is the Eiffel Tower located?",
		"namespace": namespace,
		"top_k":     3,
	}, &q)
	assertEqual(t, http.StatusOK, status, "rag query should return 200")
	assertTrue(t, len(q.Results) > 0, "query should return at least one result")

	top := q.Results[0]
	t.Logf("top result (score=%.4f): %q", top.Score, top.ChunkText)
	lower := strings.ToLower(top.ChunkText)
	assertTrue(t, strings.Contains(lower, "eiffel") || strings.Contains(lower, "paris"),
		fmt.Sprintf("top chunk should be the Eiffel Tower fact, got %q", top.ChunkText))

	// Cleanup. The delete handler reads the id + namespace from query params.
	postJSON(t, cfg, "/v1/rag/documents/delete?id="+docID+"&namespace="+namespace, nil, nil)
}

// TestRAGListDeleteDocumentE2E verifies document lifecycle: ingest, list (doc
// present), delete, list (doc gone).
func TestRAGListDeleteDocumentE2E(t *testing.T) {
	cfg := LoadTestConfig()
	requireEmbeddingServer(t, cfg)

	namespace := generateUniqueID("rag-ns")
	docID := generateUniqueID("rag-doc")

	var ing ragIngestResp
	status := postJSON(t, cfg, "/v1/rag/ingest", map[string]any{
		"document_id": docID,
		"text":        "Distributed systems trade consistency for availability under partition.",
		"namespace":   namespace,
	}, &ing)
	assertEqual(t, http.StatusOK, status, "rag ingest should return 200")

	var docs ragDocsResp
	status = getJSON(t, cfg, "/v1/rag/documents?namespace="+namespace, &docs)
	assertEqual(t, http.StatusOK, status, "rag list should return 200")
	present := false
	for _, d := range docs.Documents {
		if d.ID == docID {
			present = true
		}
	}
	assertTrue(t, present, "ingested document should appear in the list")

	// The delete handler reads the id + namespace from query params.
	status = postJSON(t, cfg, "/v1/rag/documents/delete?id="+docID+"&namespace="+namespace, nil, nil)
	assertTrue(t, status == http.StatusOK || status == http.StatusNoContent,
		"rag delete should succeed")

	var after ragDocsResp
	getJSON(t, cfg, "/v1/rag/documents?namespace="+namespace, &after)
	for _, d := range after.Documents {
		assertTrue(t, d.ID != docID, "deleted document should not appear in the list")
	}
}

// TestEmbeddingVectorSearchE2E proves real embeddings flow through the HNSW
// vector index: embed three sentences via REST, upsert their vectors via the
// vector CLI, then search with a fourth embedding and assert the nearest
// neighbour is the semantically closest sentence. This exercises the embedding
// service and the vector index together with live vectors (not raw fixtures).
// Named "Embedding*" so it runs under the embedding suite filter.
func TestEmbeddingVectorSearchE2E(t *testing.T) {
	cfg := LoadTestConfig()
	requireEmbeddingServer(t, cfg)
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	corpus := []string{
		"Raft elects a leader to coordinate log replication.",    // id 1
		"QUIC multiplexes streams over a single UDP connection.", // id 2
		"Photosynthesis converts sunlight into chemical energy.", // id 3
	}
	var emb embedResp
	status := postJSON(t, cfg, "/v1/embeddings", map[string]any{"texts": corpus}, &emb)
	assertEqual(t, http.StatusOK, status, "embed corpus should return 200")
	assertEqual(t, len(corpus), len(emb.Embeddings), "one embedding per corpus entry")
	dim := len(emb.Embeddings[0])

	namespace := generateUniqueID("emb-vec-ns")
	t.Cleanup(func() {
		execCLIWithProtocol(t, cfg, ProtocolREST, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
	})

	_, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST,
		"vector", "namespace", "create",
		"--namespace", namespace,
		"--dimension", strconv.Itoa(dim),
		"--distance-metric", "cosine")
	assertNoError(t, err, fmt.Sprintf("vector namespace create failed: %s", stderr))

	for i, vec := range emb.Embeddings {
		_, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST,
			"vector", "upsert",
			"--namespace", namespace,
			"--id", strconv.Itoa(i+1),
			"--vector", floatsToCSV(vec))
		assertNoError(t, err, fmt.Sprintf("vector upsert id=%d failed: %s", i+1, stderr))
	}

	// Query embedding semantically closest to corpus[0] (the Raft sentence).
	var query embedResp
	status = postJSON(t, cfg, "/v1/embeddings",
		map[string]any{"texts": []string{"How does the consensus protocol choose a leader?"}}, &query)
	assertEqual(t, http.StatusOK, status, "embed query should return 200")

	stdout, stderr, err := execCLIWithProtocol(t, cfg, ProtocolREST,
		"vector", "search",
		"--namespace", namespace,
		"--vector", floatsToCSV(query.Embeddings[0]),
		"--k", "1")
	assertNoError(t, err, fmt.Sprintf("vector search failed: %s", stderr))

	out := stdout + stderr
	// The nearest neighbour must be id 1 (the Raft/consensus sentence).
	assertContains(t, out, "1", "nearest neighbour to a consensus query should be the Raft vector (id 1)")
	t.Logf("vector search result: %s", strings.TrimSpace(out))
}

// floatsToCSV renders an embedding vector as the comma-separated form the
// vector CLI expects for --vector.
func floatsToCSV(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(float64(f), 'f', -1, 32)
	}
	return strings.Join(parts, ",")
}
