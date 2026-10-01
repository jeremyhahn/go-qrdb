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

//go:build qrdb_vector

package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/rag"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ragVectors is one database's vector store: namespaces hold vectors by ID,
// and a search returns every vector of the namespace. Only the verbs the RAG
// pipeline drives are implemented.
type ragVectors struct {
	VectorService
	mu   sync.Mutex
	vecs map[string]map[uint64]map[string]interface{}
}

func newRAGVectors() *ragVectors {
	return &ragVectors{vecs: map[string]map[uint64]map[string]interface{}{}}
}

func (v *ragVectors) CreateNamespace(_ context.Context, req *CreateNamespaceRequest) (*CreateNamespaceResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vecs[req.Namespace] == nil {
		v.vecs[req.Namespace] = map[uint64]map[string]interface{}{}
	}
	return &CreateNamespaceResponse{Namespace: req.Namespace}, nil
}

func (v *ragVectors) UpsertVector(_ context.Context, req *UpsertVectorRequest) (*UpsertVectorResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vecs[req.Namespace] == nil {
		v.vecs[req.Namespace] = map[uint64]map[string]interface{}{}
	}
	v.vecs[req.Namespace][req.ID] = req.Metadata
	return &UpsertVectorResponse{Success: true}, nil
}

func (v *ragVectors) SearchSimilar(_ context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []*VectorSearchResult
	for id, meta := range v.vecs[req.Namespace] {
		out = append(out, &VectorSearchResult{ID: id, Metadata: meta})
	}
	return &SearchSimilarResponse{Results: out}, nil
}

// ragOneDimEmbedder embeds every text as the same unit vector.
type ragOneDimEmbedder struct{}

func (ragOneDimEmbedder) Embed(_ context.Context, req *rag.EmbedRequest) (*rag.EmbedResponse, error) {
	out := make([][]float32, len(req.Texts))
	for i := range out {
		out[i] = []float32{1}
	}
	return &rag.EmbedResponse{Embeddings: out}, nil
}

func (ragOneDimEmbedder) BatchEmbed(_ context.Context, req *rag.BatchEmbedRequest) (*rag.BatchEmbedResponse, error) {
	out := make([][]float32, len(req.Texts))
	for i := range out {
		out[i] = []float32{1}
	}
	return &rag.BatchEmbedResponse{Embeddings: out}, nil
}

// ragPipeline is the RAG pipeline as the node composes it: one ingestor, one
// retriever and one service, all resolving through one binding to the
// registry, which holds a KV and a vector store per (tenant, database).
type ragPipeline struct {
	ingestor  *rag.Ingestor
	retriever *rag.Retriever
	svc       RAGServicer
	reg       *ragScopeRegistry
}

func newRAGPipeline(scopes ...string) *ragPipeline {
	reg := newRAGScopeRegistry()
	for _, s := range scopes {
		reg.kv[s] = newRAGMockKV()
		reg.vectors[s] = newRAGVectors()
	}
	b := NewRAGBinding()
	b.Bind(reg)
	store := NewScopedRAGStore(b)
	vectors := NewScopedVectorStoreAdapter(b)
	return &ragPipeline{
		ingestor:  rag.NewIngestor(ragOneDimEmbedder{}, vectors, store, 0),
		retriever: rag.NewRetriever(ragOneDimEmbedder{}, vectors, store),
		svc:       NewRAGService(nil, nil, store),
		reg:       reg,
	}
}

func ragCtx(tenantID, db string) context.Context {
	return tenant.WithDatabase(tenant.WithTenantID(context.Background(), tenantID), db)
}

func ingest(p *ragPipeline, ctx context.Context, text string) error {
	_, err := p.ingestor.Ingest(ctx, &rag.IngestRequest{
		Document:  &rag.Document{ID: "handbook", Text: text},
		Namespace: "kb",
	})
	return err
}

func retrieveTexts(t *testing.T, p *ragPipeline, ctx context.Context) []string {
	t.Helper()
	resp, err := p.retriever.Retrieve(ctx, &rag.RetrieveRequest{Query: "q", Namespace: "kb"})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	out := make([]string, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, r.ChunkText)
	}
	return out
}

// Every vector verb resolves before it touches a store: an unbound binding
// refuses them all.
func TestScopedVectorStoreAdapter_UnboundRefuses(t *testing.T) {
	a := NewScopedVectorStoreAdapter(NewRAGBinding())
	ctx := context.Background()
	var unsupported *CapabilityUnsupportedError
	if _, err := a.UpsertVector(ctx, &rag.UpsertVecRequest{Namespace: "kb"}); !errors.As(err, &unsupported) {
		t.Errorf("UpsertVector = %v", err)
	}
	if err := a.EnsureNamespace(ctx, "kb", 1, ""); !errors.As(err, &unsupported) {
		t.Errorf("EnsureNamespace = %v", err)
	}
	if _, err := a.SearchSimilar(ctx, &rag.SearchVecRequest{Namespace: "kb"}); !errors.As(err, &unsupported) {
		t.Errorf("SearchSimilar = %v", err)
	}
}

// TestRAG_ChunkTextConfinedPerTenant pins SEC-7: chunk text and document
// metadata were keyed rag:{namespace}:{doc}:{i} with no tenant or database,
// so two tenants using the same namespace and document ID overwrote each
// other's chunk text and a retrieval returned the other tenant's text.
func TestRAG_ChunkTextConfinedPerTenant(t *testing.T) {
	p := newRAGPipeline("acme/app", "globex/app")
	acme, globex := ragCtx("acme", "app"), ragCtx("globex", "app")
	for ctx, text := range map[context.Context]string{acme: "acme confidential roadmap", globex: "globex public brochure"} {
		if err := ingest(p, ctx, text); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}
	if got := retrieveTexts(t, p, acme); len(got) != 1 || got[0] != "acme confidential roadmap" {
		t.Fatalf("Retrieve for acme = %q", got)
	}
	if got := retrieveTexts(t, p, globex); len(got) != 1 || got[0] != "globex public brochure" {
		t.Fatalf("Retrieve for globex = %q", got)
	}
}

// TestRAG_VectorsConfinedPerDatabase pins that vectors are scoped per
// database, not only per tenant: the same document in two databases of one
// tenant has two vectors, and ingesting it into one database does not
// overwrite the other database's vector.
func TestRAG_VectorsConfinedPerDatabase(t *testing.T) {
	p := newRAGPipeline("acme/app", "acme/billing")
	if err := ingest(p, ragCtx("acme", "app"), "app text"); err != nil {
		t.Fatalf("Ingest app: %v", err)
	}
	if err := ingest(p, ragCtx("acme", "billing"), "billing text"); err != nil {
		t.Fatalf("Ingest billing: %v", err)
	}
	for _, scope := range []string{"acme/app", "acme/billing"} {
		vecs := p.reg.vectors[scope].(*ragVectors).vecs["kb"]
		if len(vecs) != 1 {
			t.Fatalf("%s holds %d vectors in kb, want 1", scope, len(vecs))
		}
	}
	if got := retrieveTexts(t, p, ragCtx("acme", "app")); len(got) != 1 || got[0] != "app text" {
		t.Fatalf("Retrieve for acme/app = %q", got)
	}
}

// A sealed database, a database the caller does not have, and a database of
// another tenant are refused on ingest, query, list and delete.
func TestRAG_RefusesSealedAndForeignDatabases(t *testing.T) {
	p := newRAGPipeline("acme/app")
	p.reg.sealed["acme/app"] = true
	for _, ctx := range []context.Context{ragCtx("acme", "app"), ragCtx("acme", "nosuchdb"), ragCtx("globex", "app")} {
		scope := tenant.FromContext(ctx) + "/" + tenant.DatabaseFromContext(ctx)
		if err := ingest(p, ctx, "text"); err == nil {
			t.Errorf("Ingest into %s succeeded", scope)
		}
		if _, err := p.retriever.Retrieve(ctx, &rag.RetrieveRequest{Query: "q", Namespace: "kb"}); err == nil {
			t.Errorf("Retrieve from %s succeeded", scope)
		}
		if _, err := p.svc.ListDocuments(ctx, &ListRAGDocumentsRequest{Namespace: "kb"}); err == nil {
			t.Errorf("ListDocuments in %s succeeded", scope)
		}
		if _, err := p.svc.DeleteDocument(ctx, &DeleteRAGDocumentRequest{Namespace: "kb", DocumentID: "handbook"}); err == nil {
			t.Errorf("DeleteDocument in %s succeeded", scope)
		}
	}
	var sealed *BarrierSealedError
	if err := ingest(p, ragCtx("acme", "app"), "text"); !errors.As(err, &sealed) {
		t.Fatalf("Ingest into a sealed database = %v, want *BarrierSealedError", err)
	}
}
