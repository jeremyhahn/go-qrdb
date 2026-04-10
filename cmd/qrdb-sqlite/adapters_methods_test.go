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

//go:build sqlite_vtable

package main

// adapters_methods_test.go tests the adapter method implementations (Get, Put,
// Delete, Scan, GetEntity, etc.) using in-memory stub implementations of the
// transport client interfaces. This avoids any network I/O while still
// exercising the real adapter logic.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite/vtab"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Stub implementations of transport client interfaces
// ---------------------------------------------------------------------------

// stubKVClient is an in-memory implementation of transport.KVClient.
type stubKVClient struct {
	store   map[string][]byte
	getErr  error
	putErr  error
	delErr  error
	scanErr error
}

func newStubKVClient() *stubKVClient {
	return &stubKVClient{store: make(map[string][]byte)}
}

func (s *stubKVClient) Get(_ context.Context, key string, _ ...transport.ReadOption) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	v, ok := s.store[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (s *stubKVClient) Scan(_ context.Context, prefix string, _ ...transport.ReadOption) (map[string][]byte, error) {
	if s.scanErr != nil {
		return nil, s.scanErr
	}
	result := make(map[string][]byte)
	for k, v := range s.store {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result[k] = v
		}
	}
	return result, nil
}

func (s *stubKVClient) Put(_ context.Context, key string, value []byte, _ ...transport.WriteOption) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.store[key] = value
	return nil
}

func (s *stubKVClient) Delete(_ context.Context, key string, _ ...transport.WriteOption) error {
	if s.delErr != nil {
		return s.delErr
	}
	delete(s.store, key)
	return nil
}

func (s *stubKVClient) List(_ context.Context, _ string, _ ...transport.ReadOption) ([]string, error) {
	return nil, nil
}

func (s *stubKVClient) Exists(_ context.Context, key string, _ ...transport.ReadOption) (bool, error) {
	_, ok := s.store[key]
	return ok, nil
}

// Compile-time verification.
var _ transport.KVClient = (*stubKVClient)(nil)

// ---------------------------------------------------------------------------
// stubIndexClient
// ---------------------------------------------------------------------------

type stubIndexClient struct {
	queryResp *transport.IndexQueryResponse
	scanResp  *transport.IndexScanResponse
	queryErr  error
	scanErr   error
}

func (s *stubIndexClient) IndexCreate(_ context.Context, _, _ string, _ transport.IndexConfig) error {
	return nil
}
func (s *stubIndexClient) IndexDrop(_ context.Context, _ string) error { return nil }
func (s *stubIndexClient) IndexQuery(_ context.Context, _, _ string) (*transport.IndexQueryResponse, error) {
	return s.queryResp, s.queryErr
}
func (s *stubIndexClient) IndexScan(_ context.Context, _, _, _ string) (*transport.IndexScanResponse, error) {
	return s.scanResp, s.scanErr
}
func (s *stubIndexClient) IndexList(_ context.Context) (*transport.IndexListResponse, error) {
	return &transport.IndexListResponse{}, nil
}

var _ transport.IndexClient = (*stubIndexClient)(nil)

// ---------------------------------------------------------------------------
// stubCollectionClient
// ---------------------------------------------------------------------------

type stubCollectionClient struct {
	docs      map[uint64]map[string]any
	nextID    uint64
	getErr    error
	listErr   error
	insertErr error
	updateErr error
	deleteErr error
}

func newStubCollectionClient() *stubCollectionClient {
	return &stubCollectionClient{docs: make(map[uint64]map[string]any), nextID: 1}
}

func (s *stubCollectionClient) CreateCollection(_ context.Context, _ transport.CreateCollectionRequest) (*transport.CollectionInfo, error) {
	return nil, nil
}
func (s *stubCollectionClient) GetCollectionInfo(_ context.Context, _, _ string) (*transport.CollectionInfo, error) {
	return nil, nil
}
func (s *stubCollectionClient) ListCollections(_ context.Context, _ string) (*transport.ListCollectionsResponse, error) {
	return &transport.ListCollectionsResponse{}, nil
}
func (s *stubCollectionClient) DropCollection(_ context.Context, _, _ string) error { return nil }

func (s *stubCollectionClient) InsertDocument(_ context.Context, req transport.InsertDocumentRequest) (*transport.InsertDocumentResponse, error) {
	if s.insertErr != nil {
		return nil, s.insertErr
	}
	id := s.nextID
	s.nextID++
	s.docs[id] = req.Document
	return &transport.InsertDocumentResponse{ID: id}, nil
}

func (s *stubCollectionClient) GetDocument(_ context.Context, _, _ string, id uint64) (*transport.DocumentResponse, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	doc, ok := s.docs[id]
	if !ok {
		return nil, nil
	}
	return &transport.DocumentResponse{ID: id, Document: doc}, nil
}

func (s *stubCollectionClient) UpdateDocument(_ context.Context, req transport.UpdateDocumentRequest) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.docs[req.ID] = req.Document
	return nil
}

func (s *stubCollectionClient) DeleteDocument(_ context.Context, _, _ string, id uint64) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.docs, id)
	return nil
}

func (s *stubCollectionClient) ListDocuments(_ context.Context, req transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	docs := make([]transport.DocumentResponse, 0, len(s.docs))
	for id, doc := range s.docs {
		docs = append(docs, transport.DocumentResponse{ID: id, Document: doc})
	}
	return &transport.ListDocumentsResponse{Documents: docs}, nil
}

var _ transport.CollectionClient = (*stubCollectionClient)(nil)

// ---------------------------------------------------------------------------
// stubTSClient
// ---------------------------------------------------------------------------

type stubTSClient struct {
	points     []transport.TSDataPoint
	metrics    []string
	writeErr   error
	queryErr   error
	metricsErr error
}

func (s *stubTSClient) TSWrite(_ context.Context, p *transport.TSDataPoint) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.points = append(s.points, *p)
	return nil
}

func (s *stubTSClient) TSWriteBatch(_ context.Context, _ []transport.TSDataPoint) error { return nil }

func (s *stubTSClient) TSQuery(_ context.Context, req *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	var pts []transport.TSDataPoint
	for _, p := range s.points {
		if p.Metric == req.Metric && p.TimestampNs >= req.StartNs && p.TimestampNs <= req.EndNs {
			pts = append(pts, p)
		}
	}
	return &transport.TSQueryResponse{Points: pts}, nil
}

func (s *stubTSClient) TSAggregate(_ context.Context, _ *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	return nil, nil
}
func (s *stubTSClient) TSWritePoint(_ context.Context, _ *transport.TSDataPoint) error { return nil }
func (s *stubTSClient) TSRichQuery(_ context.Context, _ *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	return nil, nil
}
func (s *stubTSClient) TSDeleteSeries(_ context.Context, _ *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	return nil, nil
}
func (s *stubTSClient) TSGetRetention(_ context.Context, _ *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	return nil, nil
}
func (s *stubTSClient) TSSetRetention(_ context.Context, _ *transport.TSSetRetentionRequest) error {
	return nil
}

func (s *stubTSClient) TSListMetrics(_ context.Context, req *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	if s.metricsErr != nil {
		return nil, s.metricsErr
	}
	var result []string
	for _, m := range s.metrics {
		if req.Prefix == "" || len(m) >= len(req.Prefix) && m[:len(req.Prefix)] == req.Prefix {
			result = append(result, m)
		}
	}
	return &transport.TSListMetricsResponse{Metrics: result}, nil
}

var _ transport.TimeSeriesClient = (*stubTSClient)(nil)

// ---------------------------------------------------------------------------
// stubVectorClient
// ---------------------------------------------------------------------------

type stubVectorClient struct {
	namespaces map[string]*transport.NamespaceInfo
	vectors    map[string]map[uint64]*transport.VectorData // ns -> id -> data
	err        error
}

func newStubVectorClient() *stubVectorClient {
	return &stubVectorClient{
		namespaces: make(map[string]*transport.NamespaceInfo),
		vectors:    make(map[string]map[uint64]*transport.VectorData),
	}
}

func (s *stubVectorClient) CreateNamespace(_ context.Context, req *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.namespaces[req.Namespace] = &transport.NamespaceInfo{
		Namespace:      req.Namespace,
		Dimension:      req.Dimension,
		DistanceMetric: req.DistanceMetric,
		IndexType:      req.IndexType,
	}
	s.vectors[req.Namespace] = make(map[uint64]*transport.VectorData)
	return &transport.CreateNamespaceResponse{}, nil
}

func (s *stubVectorClient) DeleteNamespace(_ context.Context, req *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	delete(s.namespaces, req.Namespace)
	delete(s.vectors, req.Namespace)
	return &transport.DeleteNamespaceResponse{}, nil
}

func (s *stubVectorClient) ListNamespaces(_ context.Context, _ *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	ns := make([]*transport.NamespaceInfo, 0, len(s.namespaces))
	for _, info := range s.namespaces {
		ns = append(ns, info)
	}
	return &transport.ListNamespacesResponse{Namespaces: ns}, nil
}

func (s *stubVectorClient) GetNamespaceInfo(_ context.Context, req *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	info, ok := s.namespaces[req.Namespace]
	if !ok {
		return nil, nil
	}
	return &transport.GetNamespaceInfoResponse{
		Namespace:      info.Namespace,
		Dimension:      info.Dimension,
		DistanceMetric: info.DistanceMetric,
		IndexType:      info.IndexType,
	}, nil
}

func (s *stubVectorClient) UpsertVector(_ context.Context, req *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if _, ok := s.vectors[req.Namespace]; !ok {
		s.vectors[req.Namespace] = make(map[uint64]*transport.VectorData)
	}
	s.vectors[req.Namespace][req.ID] = &transport.VectorData{
		ID:       req.ID,
		Vector:   req.Vector,
		Metadata: req.Metadata,
	}
	return &transport.UpsertVectorResponse{ID: req.ID, Success: true}, nil
}

func (s *stubVectorClient) GetVector(_ context.Context, req *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	ns, ok := s.vectors[req.Namespace]
	if !ok {
		return &transport.GetVectorResponse{Found: false}, nil
	}
	v, ok := ns[req.ID]
	if !ok {
		return &transport.GetVectorResponse{Found: false}, nil
	}
	return &transport.GetVectorResponse{
		ID:       v.ID,
		Vector:   v.Vector,
		Metadata: v.Metadata,
		Found:    true,
	}, nil
}

func (s *stubVectorClient) DeleteVector(_ context.Context, req *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if ns, ok := s.vectors[req.Namespace]; ok {
		delete(ns, req.ID)
	}
	return &transport.DeleteVectorResponse{Success: true}, nil
}

func (s *stubVectorClient) ListVectors(_ context.Context, req *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	ns, ok := s.vectors[req.Namespace]
	if !ok {
		return &transport.ListVectorsResponse{}, nil
	}
	vecs := make([]*transport.VectorData, 0, len(ns))
	for _, v := range ns {
		vecs = append(vecs, v)
	}
	return &transport.ListVectorsResponse{Vectors: vecs}, nil
}

func (s *stubVectorClient) BatchUpsert(_ context.Context, _ *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) BatchDeleteVectors(_ context.Context, _ *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) UpdateMetadata(_ context.Context, _ *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) SearchSimilar(_ context.Context, _ *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) SearchByMetadata(_ context.Context, _ *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) HybridSearch(_ context.Context, _ *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) SearchByID(_ context.Context, _ *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) RebuildIndex(_ context.Context, _ *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) GetRebuildProgress(_ context.Context, _ *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) GetIndexStats(_ context.Context, _ *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) CompactNamespace(_ context.Context, _ *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	return nil, nil
}
func (s *stubVectorClient) GetCompactionProgress(_ context.Context, _ *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	return nil, nil
}

var _ transport.VectorClient = (*stubVectorClient)(nil)

// ---------------------------------------------------------------------------
// stubBatchClient
// ---------------------------------------------------------------------------

type stubBatchClient struct {
	executed []transport.Op
	err      error
}

func (s *stubBatchClient) ExecuteBatch(_ context.Context, ops []transport.Op, _ ...transport.WriteOption) (*transport.BatchResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.executed = append(s.executed, ops...)
	return &transport.BatchResponse{}, nil
}

func (s *stubBatchClient) ImportFile(_ context.Context, _ string, _ io.Reader, _ transport.ImportOptions) (*transport.ImportResult, error) {
	return nil, nil
}

var _ transport.BatchClient = (*stubBatchClient)(nil)

// ---------------------------------------------------------------------------
// stubAdminClient
// ---------------------------------------------------------------------------

type stubAdminClient struct {
	caps *transport.CapabilitiesResponse
	err  error
}

func (s *stubAdminClient) Health(_ context.Context) (*transport.HealthResponse, error) {
	return &transport.HealthResponse{Status: "ok"}, nil
}
func (s *stubAdminClient) Status(_ context.Context) (*transport.StatusResponse, error) {
	return nil, nil
}
func (s *stubAdminClient) Peers(_ context.Context) (*transport.PeersResponse, error) {
	return nil, nil
}
func (s *stubAdminClient) AddMember(_ context.Context, _, _ uint64, _ string) error { return nil }
func (s *stubAdminClient) RemoveMember(_ context.Context, _, _ uint64) error        { return nil }
func (s *stubAdminClient) RequestSnapshot(_ context.Context, _ uint64) error        { return nil }
func (s *stubAdminClient) Drain(_ context.Context) error                            { return nil }
func (s *stubAdminClient) Capabilities(_ context.Context) (*transport.CapabilitiesResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.caps != nil {
		return s.caps, nil
	}
	return &transport.CapabilitiesResponse{KV: true, TimeSeries: true, Vector: true}, nil
}
func (s *stubAdminClient) StartShard(_ context.Context, _ *transport.StartShardRequest) error {
	return nil
}
func (s *stubAdminClient) StopShard(_ context.Context, _ uint64) error { return nil }
func (s *stubAdminClient) GetShardState(_ context.Context, _ uint64) (*transport.ShardStateResponse, error) {
	return nil, nil
}
func (s *stubAdminClient) GetTopology(_ context.Context) (*transport.TopologyResponse, error) {
	return nil, nil
}

var _ transport.AdminClient = (*stubAdminClient)(nil)

// ---------------------------------------------------------------------------
// Direct struct adapter construction for tests
// (bypassing newXxxAdapter since that requires *sdk.Client)
// ---------------------------------------------------------------------------

func newKVAdapterDirect(kv transport.KVClient, p pragmaReader) *kvAdapter {
	return &kvAdapter{kv: kv, pragma: p}
}

func newEntityAdapterDirect(kv transport.KVClient, idx transport.IndexClient, p pragmaReader) *entityAdapter {
	return &entityAdapter{kv: kv, index: idx, pragma: p}
}

func newCollectionAdapterDirect(col transport.CollectionClient) *collectionAdapter {
	return &collectionAdapter{col: col}
}

func newDatapointAdapterDirect(ts transport.TimeSeriesClient) *datapointAdapter {
	return &datapointAdapter{ts: ts}
}

func newMetricsAdapterDirect(ts transport.TimeSeriesClient) *metricsAdapter {
	return &metricsAdapter{ts: ts}
}

func newNamespaceAdapterDirect(vec transport.VectorClient) *namespaceAdapter {
	return &namespaceAdapter{vec: vec}
}

func newVectorAdapterDirect(vec transport.VectorClient) *vectorAdapter {
	return &vectorAdapter{vec: vec}
}

func newBatchAdapterDirect(batch transport.BatchClient, p pragmaReader) *batchAdapter {
	return &batchAdapter{batch: batch, pragma: p}
}

func newDiscoveryAdapterDirect(
	admin transport.AdminClient,
	col transport.CollectionClient,
	ts transport.TimeSeriesClient,
	vec transport.VectorClient,
	database string,
) *discoveryAdapter {
	return &discoveryAdapter{
		admin:    admin,
		col:      col,
		ts:       ts,
		vec:      vec,
		database: database,
	}
}

// ---------------------------------------------------------------------------
// kvAdapter tests
// ---------------------------------------------------------------------------

func TestKVAdapter_Put_Get_Delete(t *testing.T) {
	kv := newStubKVClient()
	p := &stubPragma{consistency: "linearizable", writeMode: "sync"}
	a := newKVAdapterDirect(kv, p)
	ctx := context.Background()

	if err := a.Put(ctx, "k1", []byte("v1")); err != nil {
		t.Fatalf("Put error: %v", err)
	}
	got, err := a.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("Get = %q, want %q", got, "v1")
	}
	if err := a.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	got, err = a.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get after Delete error: %v", err)
	}
	if got != nil {
		t.Errorf("Get after Delete = %v, want nil", got)
	}
}

func TestKVAdapter_Scan(t *testing.T) {
	kv := newStubKVClient()
	p := &stubPragma{consistency: "linearizable"}
	a := newKVAdapterDirect(kv, p)
	ctx := context.Background()

	_ = a.Put(ctx, "users/1", []byte("alice"))
	_ = a.Put(ctx, "users/2", []byte("bob"))
	_ = a.Put(ctx, "orders/1", []byte("o1"))

	result, err := a.Scan(ctx, "users/")
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Scan returned %d entries, want 2", len(result))
	}
}

func TestKVAdapter_Get_Error(t *testing.T) {
	kv := newStubKVClient()
	kv.getErr = errors.New("backend unavailable")
	p := &stubPragma{consistency: "linearizable"}
	a := newKVAdapterDirect(kv, p)

	_, err := a.Get(context.Background(), "k")
	if err == nil {
		t.Fatal("Get should propagate backend error")
	}
}

func TestKVAdapter_Put_Error(t *testing.T) {
	kv := newStubKVClient()
	kv.putErr = errors.New("write failed")
	p := &stubPragma{}
	a := newKVAdapterDirect(kv, p)

	err := a.Put(context.Background(), "k", []byte("v"))
	if err == nil {
		t.Fatal("Put should propagate backend error")
	}
}

func TestKVAdapter_Delete_Error(t *testing.T) {
	kv := newStubKVClient()
	kv.delErr = errors.New("delete failed")
	p := &stubPragma{}
	a := newKVAdapterDirect(kv, p)

	err := a.Delete(context.Background(), "k")
	if err == nil {
		t.Fatal("Delete should propagate backend error")
	}
}

func TestKVAdapter_Scan_Error(t *testing.T) {
	kv := newStubKVClient()
	kv.scanErr = errors.New("scan failed")
	p := &stubPragma{}
	a := newKVAdapterDirect(kv, p)

	_, err := a.Scan(context.Background(), "pfx/")
	if err == nil {
		t.Fatal("Scan should propagate backend error")
	}
}

// ---------------------------------------------------------------------------
// entityAdapter tests
// ---------------------------------------------------------------------------

func TestEntityAdapter_PutGetDelete(t *testing.T) {
	kv := newStubKVClient()
	idx := &stubIndexClient{}
	p := &stubPragma{consistency: "linearizable"}
	a := newEntityAdapterDirect(kv, idx, p)
	ctx := context.Background()

	fields := map[string]any{"name": "Alice", "age": float64(30)}
	if err := a.PutEntity(ctx, "user", 1, fields); err != nil {
		t.Fatalf("PutEntity error: %v", err)
	}

	got, err := a.GetEntity(ctx, "user", 1)
	if err != nil {
		t.Fatalf("GetEntity error: %v", err)
	}
	if got["name"] != "Alice" {
		t.Errorf("GetEntity name = %v, want Alice", got["name"])
	}

	if err := a.DeleteEntity(ctx, "user", 1); err != nil {
		t.Fatalf("DeleteEntity error: %v", err)
	}
	got, err = a.GetEntity(ctx, "user", 1)
	if err != nil {
		t.Fatalf("GetEntity after delete: %v", err)
	}
	if got != nil {
		t.Errorf("GetEntity after delete should return nil, got %v", got)
	}
}

func TestEntityAdapter_ScanEntities(t *testing.T) {
	kv := newStubKVClient()
	idx := &stubIndexClient{}
	p := &stubPragma{}
	a := newEntityAdapterDirect(kv, idx, p)
	ctx := context.Background()

	_ = a.PutEntity(ctx, "product", 1, map[string]any{"name": "Widget"})
	_ = a.PutEntity(ctx, "product", 2, map[string]any{"name": "Gadget"})

	rows, err := a.ScanEntities(ctx, "product")
	if err != nil {
		t.Fatalf("ScanEntities error: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("ScanEntities returned %d rows, want 2", len(rows))
	}
}

func TestEntityAdapter_ScanEntities_ScanError(t *testing.T) {
	kv := newStubKVClient()
	kv.scanErr = errors.New("scan failed")
	a := newEntityAdapterDirect(kv, &stubIndexClient{}, &stubPragma{})

	_, err := a.ScanEntities(context.Background(), "product")
	if err == nil {
		t.Fatal("ScanEntities should propagate scan error")
	}
}

func TestEntityAdapter_ScanEntities_SkipsInvalidKeys(t *testing.T) {
	kv := newStubKVClient()
	// Inject a key that doesn't match the entity prefix pattern.
	kv.store["product/"] = []byte(`{"name":"bad"}`)    // empty suffix → ID 0
	kv.store["product/abc"] = []byte(`{"name":"bad"}`) // non-numeric suffix
	kv.store["product/1"] = []byte(`{"name":"ok"}`)    // valid
	// Also add an entry with invalid JSON.
	kv.store["product/2"] = []byte(`{bad-json}`)

	idx := &stubIndexClient{}
	p := &stubPragma{}
	a := newEntityAdapterDirect(kv, idx, p)

	rows, err := a.ScanEntities(context.Background(), "product")
	if err != nil {
		t.Fatalf("ScanEntities error: %v", err)
	}
	// Only the valid key "product/1" should be returned.
	if len(rows) != 1 {
		t.Errorf("ScanEntities returned %d rows, want 1 (invalid keys skipped)", len(rows))
	}
}

func TestEntityAdapter_GetEntity_DecodeError(t *testing.T) {
	kv := newStubKVClient()
	kv.store["user/1"] = []byte(`{bad-json}`)
	idx := &stubIndexClient{}
	p := &stubPragma{}
	a := newEntityAdapterDirect(kv, idx, p)

	_, err := a.GetEntity(context.Background(), "user", 1)
	if err == nil {
		t.Fatal("GetEntity with invalid JSON should return error")
	}
	var decErr *AdapterDecodeError
	if !errors.As(err, &decErr) {
		t.Errorf("error should be *AdapterDecodeError, got %T: %v", err, err)
	}
}

func TestEntityAdapter_GetEntity_GetError(t *testing.T) {
	kv := newStubKVClient()
	kv.getErr = errors.New("kv unavailable")
	a := newEntityAdapterDirect(kv, &stubIndexClient{}, &stubPragma{})

	_, err := a.GetEntity(context.Background(), "user", 1)
	if err == nil {
		t.Fatal("GetEntity should propagate kv error")
	}
}

func TestEntityAdapter_PutEntity_EncodeError(t *testing.T) {
	kv := newStubKVClient()
	a := newEntityAdapterDirect(kv, &stubIndexClient{}, &stubPragma{})

	// json.Marshal cannot encode a channel.
	fields := map[string]any{"bad": make(chan int)}
	err := a.PutEntity(context.Background(), "user", 1, fields)
	if err == nil {
		t.Fatal("PutEntity with un-marshalable data should return error")
	}
	var encErr *AdapterEncodeError
	if !errors.As(err, &encErr) {
		t.Errorf("error should be *AdapterEncodeError, got %T: %v", err, err)
	}
}

func TestEntityAdapter_QueryByIndex(t *testing.T) {
	kv := newStubKVClient()
	// Pre-populate entity so the fetch succeeds.
	b, _ := json.Marshal(map[string]any{"name": "Alice"})
	kv.store["user/1"] = b

	idx := &stubIndexClient{
		queryResp: &transport.IndexQueryResponse{Keys: []string{"user/1"}},
	}
	p := &stubPragma{}
	a := newEntityAdapterDirect(kv, idx, p)

	rows, err := a.QueryByIndex(context.Background(), "user", "name", "Alice")
	if err != nil {
		t.Fatalf("QueryByIndex error: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("QueryByIndex returned %d rows, want 1", len(rows))
	}
}

func TestEntityAdapter_QueryByIndex_Error(t *testing.T) {
	kv := newStubKVClient()
	idx := &stubIndexClient{queryErr: errors.New("index unavailable")}
	a := newEntityAdapterDirect(kv, idx, &stubPragma{})

	_, err := a.QueryByIndex(context.Background(), "user", "name", "x")
	if err == nil {
		t.Fatal("QueryByIndex should propagate index error")
	}
}

func TestEntityAdapter_ScanByIndex(t *testing.T) {
	kv := newStubKVClient()
	b, _ := json.Marshal(map[string]any{"age": float64(25)})
	kv.store["user/2"] = b

	idx := &stubIndexClient{
		scanResp: &transport.IndexScanResponse{
			Entries: []transport.IndexScanEntry{
				{Value: "25", Keys: []string{"user/2"}},
			},
		},
	}
	p := &stubPragma{}
	a := newEntityAdapterDirect(kv, idx, p)

	rows, err := a.ScanByIndex(context.Background(), "user", "age", "20", "30")
	if err != nil {
		t.Fatalf("ScanByIndex error: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("ScanByIndex returned %d rows, want 1", len(rows))
	}
}

func TestEntityAdapter_ScanByIndex_Error(t *testing.T) {
	kv := newStubKVClient()
	idx := &stubIndexClient{scanErr: errors.New("scan unavailable")}
	a := newEntityAdapterDirect(kv, idx, &stubPragma{})

	_, err := a.ScanByIndex(context.Background(), "user", "age", "0", "99")
	if err == nil {
		t.Fatal("ScanByIndex should propagate index error")
	}
}

func TestEntityAdapter_QueryByIndex_KeyGetReturnsNil_Skipped(t *testing.T) {
	// When kv.Get returns nil (key not found), the entity should be skipped.
	kv := newStubKVClient() // store is empty
	idx := &stubIndexClient{
		queryResp: &transport.IndexQueryResponse{Keys: []string{"user/1"}},
	}
	a := newEntityAdapterDirect(kv, idx, &stubPragma{})

	rows, err := a.QueryByIndex(context.Background(), "user", "name", "x")
	if err != nil {
		t.Fatalf("QueryByIndex: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows when key not found, got %d", len(rows))
	}
}

func TestEntityAdapter_QueryByIndex_KeyGetError_Skipped(t *testing.T) {
	// When kv.Get returns an error, the entity is silently skipped.
	kv := newStubKVClient()
	kv.getErr = errors.New("temporary error")
	idx := &stubIndexClient{
		queryResp: &transport.IndexQueryResponse{Keys: []string{"user/1"}},
	}
	a := newEntityAdapterDirect(kv, idx, &stubPragma{})

	rows, err := a.QueryByIndex(context.Background(), "user", "name", "x")
	if err != nil {
		t.Fatalf("QueryByIndex: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows when get fails, got %d", len(rows))
	}
}

func TestEntityAdapter_QueryByIndex_InvalidKeyID_Skipped(t *testing.T) {
	// A key whose suffix is not a valid uint64 should be skipped.
	kv := newStubKVClient()
	b, _ := json.Marshal(map[string]any{"x": 1})
	kv.store["user/not-a-number"] = b
	idx := &stubIndexClient{
		queryResp: &transport.IndexQueryResponse{Keys: []string{"user/not-a-number"}},
	}
	a := newEntityAdapterDirect(kv, idx, &stubPragma{})

	rows, err := a.QueryByIndex(context.Background(), "user", "name", "x")
	if err != nil {
		t.Fatalf("QueryByIndex: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for non-numeric key id, got %d", len(rows))
	}
}

func TestEntityAdapter_QueryByIndex_InvalidJSON_Skipped(t *testing.T) {
	// A key whose value is invalid JSON should be silently skipped.
	kv := newStubKVClient()
	kv.store["user/5"] = []byte(`{bad-json}`)
	idx := &stubIndexClient{
		queryResp: &transport.IndexQueryResponse{Keys: []string{"user/5"}},
	}
	a := newEntityAdapterDirect(kv, idx, &stubPragma{})

	rows, err := a.QueryByIndex(context.Background(), "user", "name", "x")
	if err != nil {
		t.Fatalf("QueryByIndex: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for invalid JSON, got %d", len(rows))
	}
}

// ---------------------------------------------------------------------------
// collectionAdapter tests
// ---------------------------------------------------------------------------

func TestCollectionAdapter_InsertGetUpdateDeleteList(t *testing.T) {
	col := newStubCollectionClient()
	a := newCollectionAdapterDirect(col)
	ctx := context.Background()

	id, err := a.InsertDocument(ctx, "mydb", "items", map[string]any{"title": "foo"})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if id == 0 {
		t.Fatal("InsertDocument returned id=0")
	}

	doc, err := a.GetDocument(ctx, "mydb", "items", id)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc["title"] != "foo" {
		t.Errorf("GetDocument title = %v, want foo", doc["title"])
	}

	err = a.UpdateDocument(ctx, "mydb", "items", id, map[string]any{"title": "bar"})
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}

	doc, _ = a.GetDocument(ctx, "mydb", "items", id)
	if doc["title"] != "bar" {
		t.Errorf("after update title = %v, want bar", doc["title"])
	}

	rows, err := a.ListDocuments(ctx, "mydb", "items")
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(rows) == 0 {
		t.Error("ListDocuments should return at least one row")
	}

	err = a.DeleteDocument(ctx, "mydb", "items", id)
	if err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

func TestCollectionAdapter_GetDocument_NotFound_ReturnsNil(t *testing.T) {
	a := newCollectionAdapterDirect(newStubCollectionClient())
	doc, err := a.GetDocument(context.Background(), "db", "col", 9999)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc != nil {
		t.Errorf("GetDocument for non-existent id should return nil, got %v", doc)
	}
}

func TestCollectionAdapter_GetDocument_Error(t *testing.T) {
	col := newStubCollectionClient()
	col.getErr = errors.New("collection unavailable")
	a := newCollectionAdapterDirect(col)

	_, err := a.GetDocument(context.Background(), "db", "col", 1)
	if err == nil {
		t.Fatal("GetDocument should propagate error")
	}
}

func TestCollectionAdapter_InsertDocument_Error(t *testing.T) {
	col := newStubCollectionClient()
	col.insertErr = errors.New("insert failed")
	a := newCollectionAdapterDirect(col)

	_, err := a.InsertDocument(context.Background(), "db", "col", map[string]any{"x": 1})
	if err == nil {
		t.Fatal("InsertDocument should propagate error")
	}
}

func TestCollectionAdapter_UpdateDocument_Error(t *testing.T) {
	col := newStubCollectionClient()
	col.updateErr = errors.New("update failed")
	a := newCollectionAdapterDirect(col)

	err := a.UpdateDocument(context.Background(), "db", "col", 1, map[string]any{})
	if err == nil {
		t.Fatal("UpdateDocument should propagate error")
	}
}

func TestCollectionAdapter_DeleteDocument_Error(t *testing.T) {
	col := newStubCollectionClient()
	col.deleteErr = errors.New("delete failed")
	a := newCollectionAdapterDirect(col)

	err := a.DeleteDocument(context.Background(), "db", "col", 1)
	if err == nil {
		t.Fatal("DeleteDocument should propagate error")
	}
}

func TestCollectionAdapter_ListDocuments_Error(t *testing.T) {
	col := newStubCollectionClient()
	col.listErr = errors.New("list failed")
	a := newCollectionAdapterDirect(col)

	_, err := a.ListDocuments(context.Background(), "db", "col")
	if err == nil {
		t.Fatal("ListDocuments should propagate error")
	}
}

// ---------------------------------------------------------------------------
// datapointAdapter tests
// ---------------------------------------------------------------------------

func TestDatapointAdapter_WriteAndQuery(t *testing.T) {
	ts := &stubTSClient{}
	a := newDatapointAdapterDirect(ts)
	ctx := context.Background()

	// Write a datapoint with tags.
	tags := marshalTSTags([]transport.TSTag{{Key: "env", Value: "prod"}})
	err := a.WriteDatapoint(ctx, "cpu.usage", 1000, 42.5, tags)
	if err != nil {
		t.Fatalf("WriteDatapoint: %v", err)
	}

	rows, err := a.QueryDatapoints(ctx, "cpu.usage", 0, 2000)
	if err != nil {
		t.Fatalf("QueryDatapoints: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("QueryDatapoints returned %d rows, want 1", len(rows))
	}
	if rows[0].Value != 42.5 {
		t.Errorf("value = %v, want 42.5", rows[0].Value)
	}
}

func TestDatapointAdapter_WriteDatapoint_Error(t *testing.T) {
	ts := &stubTSClient{writeErr: errors.New("write failed")}
	a := newDatapointAdapterDirect(ts)

	err := a.WriteDatapoint(context.Background(), "m", 0, 1.0, "")
	if err == nil {
		t.Fatal("WriteDatapoint should propagate error")
	}
}

func TestDatapointAdapter_QueryDatapoints_Error(t *testing.T) {
	ts := &stubTSClient{queryErr: errors.New("query failed")}
	a := newDatapointAdapterDirect(ts)

	_, err := a.QueryDatapoints(context.Background(), "m", 0, 1000)
	if err == nil {
		t.Fatal("QueryDatapoints should propagate error")
	}
}

func TestDatapointAdapter_ScanAllDatapoints(t *testing.T) {
	ts := &stubTSClient{
		metrics: []string{"cpu.usage", "mem.usage"},
		points: []transport.TSDataPoint{
			{Metric: "cpu.usage", TimestampNs: 100, Value: 10},
			{Metric: "mem.usage", TimestampNs: 200, Value: 20},
		},
	}
	a := newDatapointAdapterDirect(ts)

	rows, err := a.ScanAllDatapoints(context.Background())
	if err != nil {
		t.Fatalf("ScanAllDatapoints: %v", err)
	}
	if len(rows) < 2 {
		t.Errorf("ScanAllDatapoints returned %d rows, want >=2", len(rows))
	}
}

func TestDatapointAdapter_ScanAllDatapoints_ListMetricsError(t *testing.T) {
	ts := &stubTSClient{metricsErr: errors.New("metrics unavailable")}
	a := newDatapointAdapterDirect(ts)

	_, err := a.ScanAllDatapoints(context.Background())
	if err == nil {
		t.Fatal("ScanAllDatapoints should propagate metrics error")
	}
}

func TestDatapointAdapter_ScanAllDatapoints_QueryError(t *testing.T) {
	ts := &stubTSClient{
		metrics:  []string{"cpu"},
		queryErr: errors.New("query unavailable"),
	}
	a := newDatapointAdapterDirect(ts)

	_, err := a.ScanAllDatapoints(context.Background())
	if err == nil {
		t.Fatal("ScanAllDatapoints should propagate query error")
	}
}

// ---------------------------------------------------------------------------
// metricsAdapter tests
// ---------------------------------------------------------------------------

func TestMetricsAdapter_ListMetrics(t *testing.T) {
	ts := &stubTSClient{metrics: []string{"cpu.usage", "mem.usage", "disk.io"}}
	a := newMetricsAdapterDirect(ts)

	infos, err := a.ListMetrics(context.Background())
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if len(infos) != 3 {
		t.Errorf("ListMetrics returned %d, want 3", len(infos))
	}
}

func TestMetricsAdapter_ListMetrics_Error(t *testing.T) {
	ts := &stubTSClient{metricsErr: errors.New("unavailable")}
	a := newMetricsAdapterDirect(ts)

	_, err := a.ListMetrics(context.Background())
	if err == nil {
		t.Fatal("ListMetrics should propagate error")
	}
}

func TestMetricsAdapter_GetMetric_Found(t *testing.T) {
	ts := &stubTSClient{metrics: []string{"cpu.usage", "mem.usage"}}
	a := newMetricsAdapterDirect(ts)

	info, err := a.GetMetric(context.Background(), "cpu.usage")
	if err != nil {
		t.Fatalf("GetMetric: %v", err)
	}
	if info == nil || info.Name != "cpu.usage" {
		t.Errorf("GetMetric = %v, want cpu.usage", info)
	}
}

func TestMetricsAdapter_GetMetric_NotFound(t *testing.T) {
	ts := &stubTSClient{metrics: []string{"cpu.usage"}}
	a := newMetricsAdapterDirect(ts)

	info, err := a.GetMetric(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetMetric: %v", err)
	}
	if info != nil {
		t.Errorf("GetMetric for nonexistent metric should return nil, got %v", info)
	}
}

func TestMetricsAdapter_GetMetric_Error(t *testing.T) {
	ts := &stubTSClient{metricsErr: errors.New("ts unavailable")}
	a := newMetricsAdapterDirect(ts)

	_, err := a.GetMetric(context.Background(), "cpu")
	if err == nil {
		t.Fatal("GetMetric should propagate error")
	}
}

// ---------------------------------------------------------------------------
// namespaceAdapter tests
// ---------------------------------------------------------------------------

func TestNamespaceAdapter_CreateListGetDelete(t *testing.T) {
	vec := newStubVectorClient()
	a := newNamespaceAdapterDirect(vec)
	ctx := context.Background()

	err := a.CreateNamespace(ctx, "embeddings", 128, "cosine", "hnsw")
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	infos, err := a.ListNamespaces(ctx)
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("ListNamespaces returned %d, want 1", len(infos))
	}
	if infos[0].Name != "embeddings" {
		t.Errorf("namespace name = %q, want %q", infos[0].Name, "embeddings")
	}

	info, err := a.GetNamespace(ctx, "embeddings")
	if err != nil {
		t.Fatalf("GetNamespace: %v", err)
	}
	if info == nil || info.Dimension != 128 {
		t.Errorf("GetNamespace dimension = %v, want 128", info)
	}

	err = a.DeleteNamespace(ctx, "embeddings")
	if err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
}

func TestNamespaceAdapter_GetNamespace_NotFound(t *testing.T) {
	vec := newStubVectorClient()
	a := newNamespaceAdapterDirect(vec)

	info, err := a.GetNamespace(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetNamespace: %v", err)
	}
	if info != nil {
		t.Errorf("GetNamespace for nonexistent should return nil, got %v", info)
	}
}

func TestNamespaceAdapter_ListNamespaces_Error(t *testing.T) {
	vec := newStubVectorClient()
	vec.err = errors.New("vec unavailable")
	a := newNamespaceAdapterDirect(vec)

	_, err := a.ListNamespaces(context.Background())
	if err == nil {
		t.Fatal("ListNamespaces should propagate error")
	}
}

func TestNamespaceAdapter_GetNamespace_Error(t *testing.T) {
	vec := newStubVectorClient()
	vec.err = errors.New("vec error")
	a := newNamespaceAdapterDirect(vec)

	_, err := a.GetNamespace(context.Background(), "ns")
	if err == nil {
		t.Fatal("GetNamespace should propagate error")
	}
}

func TestNamespaceAdapter_CreateNamespace_Error(t *testing.T) {
	vec := newStubVectorClient()
	vec.err = errors.New("create failed")
	a := newNamespaceAdapterDirect(vec)

	err := a.CreateNamespace(context.Background(), "ns", 64, "l2", "flat")
	if err == nil {
		t.Fatal("CreateNamespace should propagate error")
	}
}

func TestNamespaceAdapter_DeleteNamespace_Error(t *testing.T) {
	vec := newStubVectorClient()
	vec.err = errors.New("delete failed")
	a := newNamespaceAdapterDirect(vec)

	err := a.DeleteNamespace(context.Background(), "ns")
	if err == nil {
		t.Fatal("DeleteNamespace should propagate error")
	}
}

// ---------------------------------------------------------------------------
// vectorAdapter tests
// ---------------------------------------------------------------------------

func TestVectorAdapter_UpsertGetListDelete(t *testing.T) {
	vec := newStubVectorClient()
	a := newVectorAdapterDirect(vec)
	ctx := context.Background()

	// Prepare little-endian float32 bytes for [1.0, 2.0].
	floatBytes := float32sToBytes([]float32{1.0, 2.0})
	meta := `{"label":"test"}`

	err := a.UpsertVector(ctx, "ns", 1, floatBytes, meta)
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	row, err := a.GetVector(ctx, "ns", 1)
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if row == nil {
		t.Fatal("GetVector returned nil for existing vector")
	}
	if row.ID != 1 {
		t.Errorf("GetVector ID = %d, want 1", row.ID)
	}
	if row.Namespace != "ns" {
		t.Errorf("GetVector Namespace = %q, want %q", row.Namespace, "ns")
	}

	rows, err := a.ListVectors(ctx, "ns")
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("ListVectors returned %d rows, want 1", len(rows))
	}

	err = a.DeleteVector(ctx, "ns", 1)
	if err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
}

func TestVectorAdapter_UpsertVector_InvalidBytes(t *testing.T) {
	vec := newStubVectorClient()
	a := newVectorAdapterDirect(vec)

	// 3 bytes is not a multiple of 4 — should return AdapterDecodeError.
	err := a.UpsertVector(context.Background(), "ns", 1, []byte{0x01, 0x02, 0x03}, "")
	if err == nil {
		t.Fatal("UpsertVector with invalid bytes should return error")
	}
}

func TestVectorAdapter_GetVector_NotFound(t *testing.T) {
	vec := newStubVectorClient()
	a := newVectorAdapterDirect(vec)

	row, err := a.GetVector(context.Background(), "ns", 999)
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if row != nil {
		t.Errorf("GetVector for nonexistent vector should return nil, got %v", row)
	}
}

func TestVectorAdapter_GetVector_Error(t *testing.T) {
	vec := newStubVectorClient()
	vec.err = errors.New("vec unavailable")
	a := newVectorAdapterDirect(vec)

	_, err := a.GetVector(context.Background(), "ns", 1)
	if err == nil {
		t.Fatal("GetVector should propagate error")
	}
}

func TestVectorAdapter_ListVectors_Error(t *testing.T) {
	vec := newStubVectorClient()
	vec.err = errors.New("list failed")
	a := newVectorAdapterDirect(vec)

	_, err := a.ListVectors(context.Background(), "ns")
	if err == nil {
		t.Fatal("ListVectors should propagate error")
	}
}

func TestVectorAdapter_DeleteVector_Error(t *testing.T) {
	vec := newStubVectorClient()
	vec.err = errors.New("delete failed")
	a := newVectorAdapterDirect(vec)

	err := a.DeleteVector(context.Background(), "ns", 1)
	if err == nil {
		t.Fatal("DeleteVector should propagate error")
	}
}

func TestVectorAdapter_UpsertVector_NonJSONMetadata_WrapsAsRaw(t *testing.T) {
	vec := newStubVectorClient()
	a := newVectorAdapterDirect(vec)
	ctx := context.Background()

	floatBytes := float32sToBytes([]float32{1.0})
	// Pass a non-JSON string as metadata; should be stored as {_raw: ...}.
	err := a.UpsertVector(ctx, "ns", 2, floatBytes, "plain text label")
	if err != nil {
		t.Fatalf("UpsertVector with non-JSON metadata: %v", err)
	}
	// Verify the vector was stored.
	row, err := a.GetVector(ctx, "ns", 2)
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if row == nil {
		t.Fatal("GetVector returned nil after upsert")
	}
}

// ---------------------------------------------------------------------------
// batchAdapter tests
// ---------------------------------------------------------------------------

func TestBatchAdapter_ExecuteBatch_PutAndDelete(t *testing.T) {
	batchClient := &stubBatchClient{}
	p := &stubPragma{}
	a := newBatchAdapterDirect(batchClient, p)

	ops := []vtab.BufferedOp{
		{Op: vtab.OpPut, Key: "k1", Value: []byte("v1")},
		{Op: vtab.OpDelete, Key: "k2"},
	}
	if err := a.ExecuteBatch(context.Background(), ops); err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(batchClient.executed) != 2 {
		t.Errorf("executed %d ops, want 2", len(batchClient.executed))
	}
}

func TestBatchAdapter_ExecuteBatch_Error(t *testing.T) {
	batchClient := &stubBatchClient{err: errors.New("batch failed")}
	a := newBatchAdapterDirect(batchClient, &stubPragma{})

	err := a.ExecuteBatch(context.Background(), []vtab.BufferedOp{{Op: vtab.OpPut, Key: "k"}})
	if err == nil {
		t.Fatal("ExecuteBatch should propagate error")
	}
}

// ---------------------------------------------------------------------------
// discoveryAdapter tests
// ---------------------------------------------------------------------------

func TestDiscoveryAdapter_DiscoverTables_KVCapability(t *testing.T) {
	admin := &stubAdminClient{
		caps: &transport.CapabilitiesResponse{KV: true},
	}
	col := newStubCollectionClient()
	ts := &stubTSClient{}
	vec := newStubVectorClient()
	a := newDiscoveryAdapterDirect(admin, col, ts, vec, "testdb")

	tables, err := a.DiscoverTables(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTables: %v", err)
	}
	found := false
	for _, tbl := range tables {
		if tbl.Name == "kv" && tbl.Type == vtab.TableKV {
			found = true
		}
	}
	if !found {
		t.Error("DiscoverTables should include kv table when KV capability is enabled")
	}
}

func TestDiscoveryAdapter_DiscoverTables_TimeSeriesAndVector(t *testing.T) {
	admin := &stubAdminClient{
		caps: &transport.CapabilitiesResponse{TimeSeries: true, Vector: true},
	}
	col := newStubCollectionClient()
	ts := &stubTSClient{metrics: []string{"cpu"}}
	vec := newStubVectorClient()
	// Add a namespace so the vector table is discovered.
	vec.namespaces["embeddings"] = &transport.NamespaceInfo{Namespace: "embeddings"}
	vec.vectors["embeddings"] = make(map[uint64]*transport.VectorData)

	a := newDiscoveryAdapterDirect(admin, col, ts, vec, "")

	tables, err := a.DiscoverTables(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTables: %v", err)
	}
	names := make(map[string]bool)
	for _, tbl := range tables {
		names[tbl.Name] = true
	}
	if !names["ts_datapoints"] {
		t.Error("expected ts_datapoints table when time-series metrics exist")
	}
	if !names["vectors"] {
		t.Error("expected vectors table when namespaces exist")
	}
}

func TestDiscoveryAdapter_DiscoverTables_AdminError_StillChecksTimeSeriesAndVector(t *testing.T) {
	// When Capabilities() fails, caps is nil. The code checks caps == nil || caps.TimeSeries
	// and caps == nil || caps.Vector, so both branches run with metrics and namespaces.
	admin := &stubAdminClient{err: errors.New("admin unavailable")}
	col := newStubCollectionClient()
	ts := &stubTSClient{metrics: []string{"cpu"}}
	vec := newStubVectorClient()
	vec.namespaces["ns"] = &transport.NamespaceInfo{Namespace: "ns"}
	vec.vectors["ns"] = make(map[uint64]*transport.VectorData)

	a := newDiscoveryAdapterDirect(admin, col, ts, vec, "")

	tables, err := a.DiscoverTables(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTables should not return error on admin failure, got: %v", err)
	}
	names := make(map[string]bool)
	for _, tbl := range tables {
		names[tbl.Name] = true
	}
	// Both ts_datapoints and vectors should be discovered even without caps.
	if !names["ts_datapoints"] {
		t.Error("expected ts_datapoints when caps is nil and metrics exist")
	}
	if !names["vectors"] {
		t.Error("expected vectors when caps is nil and namespaces exist")
	}
}

func TestDiscoveryAdapter_DiscoverTables_WithDatabase_CollectionsWithSchema(t *testing.T) {
	admin := &stubAdminClient{}
	// Override ListCollections to return a collection with a schema.
	col := &stubCollectionClientWithSchema{
		stubCollectionClient: newStubCollectionClient(),
		collections: []transport.CollectionInfo{
			{
				Name:     "products",
				Database: "mydb",
				Schema:   map[string]string{"name": "TEXT", "price": "REAL"},
			},
		},
	}
	ts := &stubTSClient{}
	vec := newStubVectorClient()
	a := newDiscoveryAdapterDirect(admin, col, ts, vec, "mydb")

	tables, err := a.DiscoverTables(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTables: %v", err)
	}
	found := false
	for _, tbl := range tables {
		if tbl.Name == "products" && tbl.Type == vtab.TableCollection {
			found = true
			// Verify schema columns were added (plus the _id column).
			if len(tbl.Columns) < 3 { // _id + name + price
				t.Errorf("expected >=3 columns for products table, got %d", len(tbl.Columns))
			}
		}
	}
	if !found {
		t.Error("DiscoverTables should include 'products' collection table")
	}
}

// stubCollectionClientWithSchema extends stubCollectionClient with configurable
// ListCollections output to test the collection discovery path.
type stubCollectionClientWithSchema struct {
	*stubCollectionClient
	collections []transport.CollectionInfo
}

func (s *stubCollectionClientWithSchema) ListCollections(_ context.Context, _ string) (*transport.ListCollectionsResponse, error) {
	return &transport.ListCollectionsResponse{Collections: s.collections}, nil
}

var _ transport.CollectionClient = (*stubCollectionClientWithSchema)(nil)
