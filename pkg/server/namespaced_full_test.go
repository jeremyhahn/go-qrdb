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

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// errBoom is the sentinel inner-service error used to exercise the error
// passthrough branch of every namespaced wrapper method.
var errBoom = errors.New("boom")

// kvKV aliases the canonical key-value type returned across the service
// interfaces (service.KeyValue == kv.KeyValue).
type kvKV = kv.KeyValue

// kvKey is a terse constructor for a key-value carrying only a key.
func kvKey(s string) *kvKV { return &kvKV{Key: []byte(s)} }

// ---------------------------------------------------------------------------
// Vector wrapper — full method surface
// ---------------------------------------------------------------------------

// vectorRec records the namespace it receives on every VectorServicer method
// and optionally returns errBoom to drive the error passthrough branch.
type vectorRec struct {
	service.VectorServicer
	lastNS string
	err    error
}

func (m *vectorRec) rec(ns string) error { m.lastNS = ns; return m.err }

func (m *vectorRec) CreateNamespace(_ context.Context, r *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.CreateNamespaceResponse{Namespace: r.Namespace}, nil
}
func (m *vectorRec) DeleteNamespace(_ context.Context, r *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.DeleteNamespaceResponse{Success: true}, nil
}
func (m *vectorRec) ListNamespaces(_ context.Context, r *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	if err := m.rec(r.Prefix); err != nil {
		return nil, err
	}
	return &service.ListNamespacesResponse{Namespaces: []*service.NamespaceInfo{nil, {Namespace: r.Prefix}}}, nil
}
func (m *vectorRec) GetNamespaceInfo(_ context.Context, r *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.GetNamespaceInfoResponse{Namespace: r.Namespace}, nil
}
func (m *vectorRec) UpsertVector(_ context.Context, r *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.UpsertVectorResponse{}, nil
}
func (m *vectorRec) GetVector(_ context.Context, r *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.GetVectorResponse{}, nil
}
func (m *vectorRec) DeleteVector(_ context.Context, r *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.DeleteVectorResponse{}, nil
}
func (m *vectorRec) BatchUpsert(_ context.Context, r *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.BatchUpsertResponse{}, nil
}
func (m *vectorRec) BatchDeleteVectors(_ context.Context, r *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.BatchDeleteVectorsResponse{}, nil
}
func (m *vectorRec) ListVectors(_ context.Context, r *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.ListVectorsResponse{}, nil
}
func (m *vectorRec) UpdateMetadata(_ context.Context, r *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.UpdateVectorMetadataResponse{}, nil
}
func (m *vectorRec) SearchSimilar(_ context.Context, r *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.SearchSimilarResponse{}, nil
}
func (m *vectorRec) SearchByMetadata(_ context.Context, r *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.SearchByMetadataResponse{}, nil
}
func (m *vectorRec) HybridSearch(_ context.Context, r *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.HybridSearchResponse{}, nil
}
func (m *vectorRec) SearchByID(_ context.Context, r *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.SearchByIDResponse{}, nil
}
func (m *vectorRec) RebuildIndex(_ context.Context, r *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.RebuildIndexResponse{}, nil
}
func (m *vectorRec) GetRebuildProgress(_ context.Context, r *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.GetRebuildProgressResponse{Namespace: r.Namespace}, nil
}
func (m *vectorRec) GetIndexStats(_ context.Context, r *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.GetIndexStatsResponse{Namespace: r.Namespace}, nil
}
func (m *vectorRec) CompactNamespace(_ context.Context, r *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.CompactNamespaceResponse{}, nil
}
func (m *vectorRec) GetCompactionProgress(_ context.Context, r *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	if err := m.rec(r.Namespace); err != nil {
		return nil, err
	}
	return &service.GetCompactionProgressResponse{Namespace: r.Namespace}, nil
}

func TestNamespacedVector_AllMethods_PrefixOnRequest(t *testing.T) {
	rec := &vectorRec{}
	w := newNamespacedVectorService(rec, "p/")
	ctx := context.Background()

	calls := []struct {
		name string
		fn   func() error
	}{
		{"CreateNamespace", func() error {
			r, e := w.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "docs"})
			if e == nil && r.Namespace != "docs" {
				t.Errorf("CreateNamespace stripped = %q", r.Namespace)
			}
			return e
		}},
		{"DeleteNamespace", func() error {
			_, e := w.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{Namespace: "docs"})
			return e
		}},
		{"ListNamespaces", func() error {
			r, e := w.ListNamespaces(ctx, &service.ListNamespacesRequest{Prefix: "docs"})
			if e == nil && r.Namespaces[1].Namespace != "docs" {
				t.Errorf("ListNamespaces stripped = %q", r.Namespaces[1].Namespace)
			}
			return e
		}},
		{"GetNamespaceInfo", func() error {
			r, e := w.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{Namespace: "docs"})
			if e == nil && r.Namespace != "docs" {
				t.Errorf("GetNamespaceInfo stripped = %q", r.Namespace)
			}
			return e
		}},
		{"UpsertVector", func() error {
			_, e := w.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "docs"})
			return e
		}},
		{"GetVector", func() error {
			_, e := w.GetVector(ctx, &service.GetVectorRequest{Namespace: "docs"})
			return e
		}},
		{"DeleteVector", func() error {
			_, e := w.DeleteVector(ctx, &service.DeleteVectorRequest{Namespace: "docs"})
			return e
		}},
		{"BatchUpsert", func() error {
			_, e := w.BatchUpsert(ctx, &service.BatchUpsertRequest{Namespace: "docs"})
			return e
		}},
		{"BatchDeleteVectors", func() error {
			_, e := w.BatchDeleteVectors(ctx, &service.BatchDeleteVectorsRequest{Namespace: "docs"})
			return e
		}},
		{"ListVectors", func() error {
			_, e := w.ListVectors(ctx, &service.ListVectorsRequest{Namespace: "docs"})
			return e
		}},
		{"UpdateMetadata", func() error {
			_, e := w.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{Namespace: "docs"})
			return e
		}},
		{"SearchSimilar", func() error {
			_, e := w.SearchSimilar(ctx, &service.SearchSimilarRequest{Namespace: "docs"})
			return e
		}},
		{"SearchByMetadata", func() error {
			_, e := w.SearchByMetadata(ctx, &service.SearchByMetadataRequest{Namespace: "docs"})
			return e
		}},
		{"HybridSearch", func() error {
			_, e := w.HybridSearch(ctx, &service.HybridSearchRequest{Namespace: "docs"})
			return e
		}},
		{"SearchByID", func() error {
			_, e := w.SearchByID(ctx, &service.SearchByIDRequest{Namespace: "docs"})
			return e
		}},
		{"RebuildIndex", func() error {
			_, e := w.RebuildIndex(ctx, &service.RebuildIndexRequest{Namespace: "docs"})
			return e
		}},
		{"GetRebuildProgress", func() error {
			r, e := w.GetRebuildProgress(ctx, &service.GetRebuildProgressRequest{Namespace: "docs"})
			if e == nil && r.Namespace != "docs" {
				t.Errorf("GetRebuildProgress stripped = %q", r.Namespace)
			}
			return e
		}},
		{"GetIndexStats", func() error {
			r, e := w.GetIndexStats(ctx, &service.GetIndexStatsRequest{Namespace: "docs"})
			if e == nil && r.Namespace != "docs" {
				t.Errorf("GetIndexStats stripped = %q", r.Namespace)
			}
			return e
		}},
		{"CompactNamespace", func() error {
			_, e := w.CompactNamespace(ctx, &service.CompactNamespaceRequest{Namespace: "docs"})
			return e
		}},
		{"GetCompactionProgress", func() error {
			r, e := w.GetCompactionProgress(ctx, &service.GetCompactionProgressRequest{Namespace: "docs"})
			if e == nil && r.Namespace != "docs" {
				t.Errorf("GetCompactionProgress stripped = %q", r.Namespace)
			}
			return e
		}},
	}

	// Happy path: the inner service sees the composite-prefixed namespace.
	for _, c := range calls {
		rec.lastNS = ""
		if err := c.fn(); err != nil {
			t.Fatalf("%s: unexpected err %v", c.name, err)
		}
		if rec.lastNS != "p/docs" {
			t.Errorf("%s: inner namespace = %q, want p/docs", c.name, rec.lastNS)
		}
	}

	// Error path: every method forwards the inner error unchanged.
	rec.err = errBoom
	for _, c := range calls {
		if err := c.fn(); !errors.Is(err, errBoom) {
			t.Errorf("%s: err = %v, want errBoom", c.name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// TimeSeries wrapper — rich query, batch, point, retention, consistency
// ---------------------------------------------------------------------------

type tsRec struct {
	service.TimeSeriesServicer
	lastMetric string
	lastPoints []timeseries.DataPoint
	nilResult  bool
	err        error
}

func (m *tsRec) WriteBatch(_ context.Context, points []timeseries.DataPoint) error {
	m.lastPoints = points
	return m.err
}
func (m *tsRec) QueryWithConsistency(_ context.Context, metric string, _, _ time.Time, _ consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.lastMetric = metric
	if m.err != nil {
		return nil, m.err
	}
	return []timeseries.DataPoint{{Metric: metric}}, nil
}
func (m *tsRec) Aggregate(_ context.Context, metric string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	m.lastMetric = metric
	return nil, m.err
}
func (m *tsRec) AggregateWithConsistency(_ context.Context, metric string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	m.lastMetric = metric
	return nil, m.err
}
func (m *tsRec) WritePoint(_ context.Context, p *timeseries.DataPoint) error {
	if p != nil {
		m.lastMetric = p.Metric
	} else {
		m.lastMetric = "<nil>"
	}
	return m.err
}
func (m *tsRec) QueryRich(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if req != nil {
		m.lastMetric = req.Metric
	} else {
		m.lastMetric = "<nil>"
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.nilResult {
		return nil, nil
	}
	return &timeseries.QueryResult{
		Metric: m.prefixedMetric(req),
		Points: []*timeseries.DataPoint{nil, {Metric: m.prefixedMetric(req)}},
		GroupedResults: []*timeseries.GroupedResult{
			nil,
			{Points: []*timeseries.DataPoint{{Metric: m.prefixedMetric(req)}}},
		},
	}, nil
}
func (m *tsRec) prefixedMetric(req *timeseries.QueryRequest) string {
	if req == nil {
		return ""
	}
	return req.Metric
}
func (m *tsRec) QueryRichWithConsistency(_ context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return m.QueryRich(context.Background(), req)
}
func (m *tsRec) DeleteSeries(_ context.Context, req *timeseries.DeleteRequest) error {
	if req != nil {
		m.lastMetric = req.Metric
	} else {
		m.lastMetric = "<nil>"
	}
	return m.err
}
func (m *tsRec) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	m.lastMetric = metric
	return &timeseries.RetentionPolicy{}, m.err
}
func (m *tsRec) SetRetentionPolicy(_ context.Context, metric string, _ *timeseries.RetentionPolicy) error {
	m.lastMetric = metric
	return m.err
}
func (m *tsRec) ListMetricsWithConsistency(_ context.Context, prefix string, _ int, _ consistency.ReadOptions) ([]string, error) {
	m.lastMetric = prefix
	if m.err != nil {
		return nil, m.err
	}
	return []string{"p/cpu"}, nil
}

func TestNamespacedTimeSeries_RichAndBatch(t *testing.T) {
	rec := &tsRec{}
	w := newNamespacedTimeSeriesService(rec, "p/")
	ctx := context.Background()

	// WriteBatch prefixes each point's metric.
	if err := w.WriteBatch(ctx, []timeseries.DataPoint{{Metric: "cpu"}}); err != nil {
		t.Fatal(err)
	}
	if rec.lastPoints[0].Metric != "p/cpu" {
		t.Errorf("WriteBatch metric = %q", rec.lastPoints[0].Metric)
	}

	// QueryWithConsistency prefixes the metric and strips the returned points.
	pts, err := w.QueryWithConsistency(ctx, "cpu", time.Time{}, time.Time{}, consistency.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/cpu" || pts[0].Metric != "cpu" {
		t.Errorf("QueryWithConsistency inner=%q out=%q", rec.lastMetric, pts[0].Metric)
	}

	// Aggregate / AggregateWithConsistency prefix the metric; results carry none.
	if _, err := w.Aggregate(ctx, "cpu", time.Time{}, time.Time{}, timeseries.AggNone, 0); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/cpu" {
		t.Errorf("Aggregate metric = %q", rec.lastMetric)
	}
	if _, err := w.AggregateWithConsistency(ctx, "mem", time.Time{}, time.Time{}, timeseries.AggNone, 0, consistency.ReadOptions{}); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/mem" {
		t.Errorf("AggregateWithConsistency metric = %q", rec.lastMetric)
	}

	// WritePoint prefixes a non-nil point and forwards a nil point unchanged.
	if err := w.WritePoint(ctx, &timeseries.DataPoint{Metric: "cpu"}); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/cpu" {
		t.Errorf("WritePoint metric = %q", rec.lastMetric)
	}
	if err := w.WritePoint(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "<nil>" {
		t.Errorf("WritePoint(nil) metric = %q", rec.lastMetric)
	}

	// QueryRich prefixes the request metric and strips metrics in the result,
	// including grouped results and nil point entries.
	res, err := w.QueryRich(ctx, &timeseries.QueryRequest{Metric: "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/cpu" {
		t.Errorf("QueryRich inner metric = %q", rec.lastMetric)
	}
	if res.Metric != "cpu" {
		t.Errorf("QueryRich result metric = %q", res.Metric)
	}
	if res.Points[1].Metric != "cpu" {
		t.Errorf("QueryRich point metric = %q", res.Points[1].Metric)
	}
	if res.GroupedResults[1].Points[0].Metric != "cpu" {
		t.Errorf("QueryRich grouped metric = %q", res.GroupedResults[1].Points[0].Metric)
	}

	// QueryRichWithConsistency follows the same path.
	if _, err := w.QueryRichWithConsistency(ctx, &timeseries.QueryRequest{Metric: "cpu"}, consistency.ReadOptions{}); err != nil {
		t.Fatal(err)
	}

	// A nil request is forwarded as nil, and a nil result is returned as nil.
	rec.nilResult = true
	res, err = w.QueryRich(ctx, nil)
	if err != nil || res != nil {
		t.Fatalf("QueryRich(nil) = %v, %v", res, err)
	}
	rec.nilResult = false

	// DeleteSeries prefixes a non-nil request and forwards a nil request.
	if err := w.DeleteSeries(ctx, &timeseries.DeleteRequest{Metric: "cpu"}); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/cpu" {
		t.Errorf("DeleteSeries metric = %q", rec.lastMetric)
	}
	if err := w.DeleteSeries(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "<nil>" {
		t.Errorf("DeleteSeries(nil) metric = %q", rec.lastMetric)
	}

	// Retention policy operations prefix the metric.
	if _, err := w.GetRetentionPolicy("cpu"); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/cpu" {
		t.Errorf("GetRetentionPolicy metric = %q", rec.lastMetric)
	}
	if err := w.SetRetentionPolicy(context.Background(), "mem", &timeseries.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/mem" {
		t.Errorf("SetRetentionPolicy metric = %q", rec.lastMetric)
	}

	// ListMetricsWithConsistency prefixes the search prefix and strips results.
	names, err := w.ListMetricsWithConsistency(ctx, "c", 10, consistency.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastMetric != "p/c" || names[0] != "cpu" {
		t.Errorf("ListMetricsWithConsistency inner=%q out=%v", rec.lastMetric, names)
	}
}

func TestNamespacedTimeSeries_ErrorPassthrough(t *testing.T) {
	rec := &tsRec{err: errBoom}
	w := newNamespacedTimeSeriesService(rec, "p/")
	ctx := context.Background()

	if err := w.WriteBatch(ctx, nil); !errors.Is(err, errBoom) {
		t.Errorf("WriteBatch err = %v", err)
	}
	if _, err := w.QueryWithConsistency(ctx, "c", time.Time{}, time.Time{}, consistency.ReadOptions{}); !errors.Is(err, errBoom) {
		t.Errorf("QueryWithConsistency err = %v", err)
	}
	if _, err := w.QueryRich(ctx, &timeseries.QueryRequest{Metric: "c"}); !errors.Is(err, errBoom) {
		t.Errorf("QueryRich err = %v", err)
	}
	if _, err := w.QueryRichWithConsistency(ctx, &timeseries.QueryRequest{Metric: "c"}, consistency.ReadOptions{}); !errors.Is(err, errBoom) {
		t.Errorf("QueryRichWithConsistency err = %v", err)
	}
	if _, err := w.ListMetricsWithConsistency(ctx, "c", 1, consistency.ReadOptions{}); !errors.Is(err, errBoom) {
		t.Errorf("ListMetricsWithConsistency err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Index wrapper — create, drop, scan, error paths
// ---------------------------------------------------------------------------

type indexRec struct {
	service.IndexServicer
	lastName string
	nilKeys  bool
	err      error
}

func (m *indexRec) CreateIndex(_ context.Context, name, _ string, _ index.IndexConfig) error {
	m.lastName = name
	return m.err
}
func (m *indexRec) DropIndex(_ context.Context, name string) error {
	m.lastName = name
	return m.err
}

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (m *indexRec) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return m.DropIndex(ctx, name)
}
func (m *indexRec) Query(_ context.Context, name string, _ []byte) ([][]byte, error) {
	m.lastName = name
	if m.err != nil {
		return nil, m.err
	}
	if m.nilKeys {
		return nil, nil
	}
	return [][]byte{[]byte("p/doc1")}, nil
}
func (m *indexRec) Scan(_ context.Context, name string, _, _ []byte) (map[string][][]byte, error) {
	m.lastName = name
	if m.err != nil {
		return nil, m.err
	}
	return map[string][][]byte{"v": {[]byte("p/doc1")}}, nil
}
func (m *indexRec) ListIndexes(_ context.Context) ([]string, error) {
	return nil, m.err
}

func TestNamespacedIndex_FullSurface(t *testing.T) {
	rec := &indexRec{}
	w := newNamespacedIndexService(rec, "p/")
	ctx := context.Background()

	if err := w.CreateIndex(ctx, "byEmail", "email", index.IndexConfig{}); err != nil {
		t.Fatal(err)
	}
	if rec.lastName != "p/byEmail" {
		t.Errorf("CreateIndex name = %q", rec.lastName)
	}
	if err := w.DropIndex(ctx, "byEmail"); err != nil {
		t.Fatal(err)
	}
	if rec.lastName != "p/byEmail" {
		t.Errorf("DropIndex name = %q", rec.lastName)
	}

	// Scan prefixes the index name and strips the returned entry keys.
	res, err := w.Scan(ctx, "byEmail", []byte("a"), []byte("z"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastName != "p/byEmail" {
		t.Errorf("Scan name = %q", rec.lastName)
	}
	if string(res["v"][0]) != "doc1" {
		t.Errorf("Scan stripped key = %q", res["v"][0])
	}

	// Query with a nil result preserves nil (stripByteSlices nil branch).
	rec.nilKeys = true
	keys, err := w.Query(ctx, "byEmail", []byte("x"))
	if err != nil || keys != nil {
		t.Fatalf("Query nil = %v, %v", keys, err)
	}
}

func TestNamespacedIndex_ErrorPassthrough(t *testing.T) {
	rec := &indexRec{err: errBoom}
	w := newNamespacedIndexService(rec, "p/")
	ctx := context.Background()
	if err := w.CreateIndex(ctx, "i", "f", index.IndexConfig{}); !errors.Is(err, errBoom) {
		t.Errorf("CreateIndex err = %v", err)
	}
	if _, err := w.Query(ctx, "i", nil); !errors.Is(err, errBoom) {
		t.Errorf("Query err = %v", err)
	}
	if _, err := w.Scan(ctx, "i", nil, nil); !errors.Is(err, errBoom) {
		t.Errorf("Scan err = %v", err)
	}
	if _, err := w.ListIndexes(ctx); !errors.Is(err, errBoom) {
		t.Errorf("ListIndexes err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lease wrapper — Grant/Revoke/KeepAlive + error paths
// ---------------------------------------------------------------------------

type leaseRec struct {
	service.LeaseServicer
	lastID  int64
	lastTTL int64
	nilL    bool
	err     error
}

func (m *leaseRec) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	m.lastTTL = ttl
	if m.err != nil {
		return nil, m.err
	}
	return &lease.Lease{ID: 9}, nil
}
func (m *leaseRec) Revoke(_ context.Context, id int64) error {
	m.lastID = id
	return m.err
}
func (m *leaseRec) KeepAlive(_ context.Context, id int64) (*lease.Lease, error) {
	m.lastID = id
	if m.err != nil {
		return nil, m.err
	}
	if m.nilL {
		return nil, nil
	}
	return &lease.Lease{ID: id, Keys: [][]byte{[]byte("p/k1")}}, nil
}
func (m *leaseRec) TimeToLive(_ context.Context, id int64) (*lease.Lease, error) {
	m.lastID = id
	return nil, m.err
}
func (m *leaseRec) Leases(_ context.Context) ([]*lease.Lease, error) {
	return nil, m.err
}

// TestNamespacedLease_GrantRevokeKeepAlive used to pin that every namespaced
// view operated on the global lease set, which is the cross-tenant flaw
// namespaced_lease_owner_test.go now refutes; the ownership behaviour is
// tested there.

func TestNamespacedLease_ErrorPassthrough(t *testing.T) {
	rec := &leaseRec{err: errBoom}
	w := newNamespacedLeaseService(rec, newSharedLessor(), "p/")
	ctx := context.Background()
	if _, err := w.Grant(ctx, 1); !errors.Is(err, errBoom) {
		t.Errorf("Grant err = %v", err)
	}
	if err := w.Revoke(ctx, 1); !errors.Is(err, errBoom) {
		t.Errorf("Revoke err = %v", err)
	}
	if _, err := w.KeepAlive(ctx, 1); !errors.Is(err, errBoom) {
		t.Errorf("KeepAlive err = %v", err)
	}
	if _, err := w.TimeToLive(ctx, 1); !errors.Is(err, errBoom) {
		t.Errorf("TimeToLive err = %v", err)
	}
	if _, err := w.Leases(ctx); !errors.Is(err, errBoom) {
		t.Errorf("Leases err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Watch wrapper — WatchPrefix, WatchEntity, WatchEntityType, Err
// ---------------------------------------------------------------------------

type watchRec struct {
	service.WatchServicer
	lastKey      string
	lastEntityID string
	w            *fakeWatcher
	entCh        chan service.WatchEntityEvent
	err          error
}

func (m *watchRec) Watch(_ context.Context, key string, _ ...watch.WatchOption) (watch.Watcher, error) {
	m.lastKey = key
	if m.err != nil {
		return nil, m.err
	}
	m.w = newFakeWatcher()
	return m.w, nil
}
func (m *watchRec) WatchPrefix(_ context.Context, prefix string, _ ...watch.WatchOption) (watch.Watcher, error) {
	m.lastKey = prefix
	if m.err != nil {
		return nil, m.err
	}
	m.w = newFakeWatcher()
	return m.w, nil
}
func (m *watchRec) WatchEntity(_ context.Context, req *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	if req != nil {
		m.lastEntityID = req.ID
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.entCh, nil
}
func (m *watchRec) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.entCh, nil
}

func TestNamespacedWatch_PrefixAndErr(t *testing.T) {
	rec := &watchRec{}
	w := newNamespacedWatchService(rec, "p/")
	ctx := context.Background()

	wtr, err := w.WatchPrefix(ctx, "pre", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastKey != "p/pre" {
		t.Errorf("WatchPrefix inner key = %q", rec.lastKey)
	}
	if err := wtr.Err(); err != nil {
		t.Errorf("Err = %v", err)
	}
	_ = wtr.Close()
}

func TestNamespacedWatch_Errors(t *testing.T) {
	rec := &watchRec{err: errBoom}
	w := newNamespacedWatchService(rec, "p/")
	ctx := context.Background()
	if _, err := w.Watch(ctx, "k"); !errors.Is(err, errBoom) {
		t.Errorf("Watch err = %v", err)
	}
	if _, err := w.WatchPrefix(ctx, "p"); !errors.Is(err, errBoom) {
		t.Errorf("WatchPrefix err = %v", err)
	}
	if _, err := w.WatchEntity(ctx, &service.WatchEntityRequest{ID: "x"}); !errors.Is(err, errBoom) {
		t.Errorf("WatchEntity err = %v", err)
	}
	if _, err := w.WatchEntityType(ctx, &service.WatchEntityTypeRequest{}); !errors.Is(err, errBoom) {
		t.Errorf("WatchEntityType err = %v", err)
	}
}

func TestNamespacedWatch_EntityNilRequest(t *testing.T) {
	ch := make(chan service.WatchEntityEvent)
	close(ch)
	rec := &watchRec{entCh: ch}
	w := newNamespacedWatchService(rec, "p/")
	// A nil request is forwarded directly to the inner service.
	if _, err := w.WatchEntity(context.Background(), nil); err != nil {
		t.Fatalf("WatchEntity(nil) err = %v", err)
	}
}

func TestNamespacedWatch_EntityPrefixesAndStrips(t *testing.T) {
	ch := make(chan service.WatchEntityEvent, 2)
	rec := &watchRec{entCh: ch}
	w := newNamespacedWatchService(rec, "p/")

	out, err := w.WatchEntity(context.Background(), &service.WatchEntityRequest{ID: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastEntityID != "p/bob" {
		t.Errorf("inner entity ID = %q, want p/bob", rec.lastEntityID)
	}
	ch <- service.WatchEntityEvent{ID: "p/bob"}
	close(ch)
	ev := <-out
	if ev.ID != "bob" {
		t.Errorf("delivered entity ID = %q, want bob", ev.ID)
	}
}

func TestNamespacedWatch_EntityTypeFilters(t *testing.T) {
	ch := make(chan service.WatchEntityEvent, 3)
	rec := &watchRec{entCh: ch}
	w := newNamespacedWatchService(rec, "p/")

	out, err := w.WatchEntityType(context.Background(), &service.WatchEntityTypeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ch <- service.WatchEntityEvent{ID: "p/in"}    // kept, stripped to "in"
	ch <- service.WatchEntityEvent{ID: "other/x"} // dropped (out of namespace)
	close(ch)

	var got []string
	for ev := range out {
		got = append(got, ev.ID)
	}
	if len(got) != 1 || got[0] != "in" {
		t.Errorf("type watch delivered = %v, want [in]", got)
	}
}

func TestNamespacedWatcher_StripsPrevKvAndNilEvents(t *testing.T) {
	rec := &watchRec{}
	w := newNamespacedWatchService(rec, "p/")
	wtr, err := w.Watch(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	// Mix a nil event with one carrying both Kv and PrevKv under the namespace.
	rec.w.ch <- watch.WatchResponse{Events: []*watch.Event{
		nil,
		{Kv: kvKey("p/k"), PrevKv: kvKey("p/old")},
	}}
	resp := <-wtr.Events()
	if resp.Events[0] != nil {
		t.Errorf("nil event must be preserved as nil")
	}
	if string(resp.Events[1].Kv.Key) != "k" {
		t.Errorf("Kv stripped = %q", resp.Events[1].Kv.Key)
	}
	if string(resp.Events[1].PrevKv.Key) != "old" {
		t.Errorf("PrevKv stripped = %q", resp.Events[1].PrevKv.Key)
	}
	_ = wtr.Close()
}

// ---------------------------------------------------------------------------
// Batch wrapper — ExecuteWithMode, ExecuteEntity
// ---------------------------------------------------------------------------

type batchRec struct {
	service.BatchServicer
	lastKeys []string
	lastIDs  []string
	err      error
}

func (m *batchRec) Execute(_ context.Context, ops []service.Op) (*service.BatchResponse, error) {
	for _, op := range ops {
		m.lastKeys = append(m.lastKeys, string(op.Key))
	}
	return nil, m.err
}
func (m *batchRec) ExecuteWithMode(_ context.Context, ops []service.Op, _ writemode.Mode) (*service.BatchResponse, error) {
	for _, op := range ops {
		m.lastKeys = append(m.lastKeys, string(op.Key))
	}
	if m.err != nil {
		return nil, m.err
	}
	return &service.BatchResponse{Revision: batchRecRevision, Responses: []service.OpResponse{{Kvs: []*kvKV{{Key: []byte("p/k")}}}}}, nil
}

// batchRecRevision is the commit revision batchRec reports, which the
// namespaced wrapper must pass through unchanged.
const batchRecRevision int64 = 7

func (m *batchRec) ExecuteEntity(_ context.Context, req *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	if req != nil {
		for _, op := range req.Ops {
			m.lastIDs = append(m.lastIDs, op.ID)
		}
	}
	return &service.BatchEntityResponse{}, m.err
}

func TestNamespacedBatch_ExecuteWithModeAndEntity(t *testing.T) {
	rec := &batchRec{}
	w := newNamespacedBatchService(rec, "p/")
	ctx := context.Background()

	resp, err := w.ExecuteWithMode(ctx, []service.Op{{Key: []byte("k")}}, writemode.Mode(0))
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastKeys[0] != "p/k" {
		t.Errorf("ExecuteWithMode inner key = %q", rec.lastKeys[0])
	}
	if string(resp.Responses[0].Kvs[0].Key) != "k" {
		t.Errorf("ExecuteWithMode stripped key = %q", resp.Responses[0].Kvs[0].Key)
	}
	if resp.Revision != batchRecRevision {
		t.Errorf("ExecuteWithMode revision = %d, want %d", resp.Revision, batchRecRevision)
	}

	// ExecuteEntity prefixes each entity op ID.
	if _, err := w.ExecuteEntity(ctx, &service.BatchEntityRequest{Ops: []service.BatchEntityOp{{ID: "bob"}}}); err != nil {
		t.Fatal(err)
	}
	if rec.lastIDs[0] != "p/bob" {
		t.Errorf("ExecuteEntity inner ID = %q", rec.lastIDs[0])
	}

	// A nil request is forwarded directly.
	if _, err := w.ExecuteEntity(ctx, nil); err != nil {
		t.Fatalf("ExecuteEntity(nil) err = %v", err)
	}
}

func TestNamespacedBatch_ErrorPassthrough(t *testing.T) {
	rec := &batchRec{err: errBoom}
	w := newNamespacedBatchService(rec, "p/")
	ctx := context.Background()
	if _, err := w.Execute(ctx, nil); !errors.Is(err, errBoom) {
		t.Errorf("Execute err = %v", err)
	}
	if _, err := w.ExecuteWithMode(ctx, nil, writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Errorf("ExecuteWithMode err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Transaction wrapper — ExecuteWithMode, ExecuteEntity (compares + failure)
// ---------------------------------------------------------------------------

type txnRec struct {
	service.TransactionServicer
	cmpKey  string
	okKey   string
	failKey string
	cmpID   string
	okID    string
	failID  string
	err     error
}

func (m *txnRec) Execute(_ context.Context, cmp []service.Compare, ok, fail []service.Op) (*service.TransactionResponse, error) {
	return m.record(cmp, ok, fail)
}
func (m *txnRec) ExecuteWithMode(_ context.Context, cmp []service.Compare, ok, fail []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return m.record(cmp, ok, fail)
}
func (m *txnRec) record(cmp []service.Compare, ok, fail []service.Op) (*service.TransactionResponse, error) {
	if len(cmp) > 0 {
		m.cmpKey = string(cmp[0].Key)
	}
	if len(ok) > 0 {
		m.okKey = string(ok[0].Key)
	}
	if len(fail) > 0 {
		m.failKey = string(fail[0].Key)
	}
	if m.err != nil {
		return nil, m.err
	}
	return &service.TransactionResponse{Responses: []service.OpResponse{{Kvs: []*kvKV{{Key: []byte("p/k")}}}}}, nil
}
func (m *txnRec) ExecuteEntity(_ context.Context, req *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	if req != nil {
		if len(req.Compares) > 0 {
			m.cmpID = req.Compares[0].ID
		}
		if len(req.Success) > 0 {
			m.okID = req.Success[0].ID
		}
		if len(req.Failure) > 0 {
			m.failID = req.Failure[0].ID
		}
	}
	return &service.TransactionEntityResponse{}, m.err
}

func TestNamespacedTransaction_WithModeAndEntity(t *testing.T) {
	rec := &txnRec{}
	w := newNamespacedTransactionService(rec, "p/")
	ctx := context.Background()

	resp, err := w.ExecuteWithMode(ctx,
		[]service.Compare{{Key: []byte("c")}},
		[]service.Op{{Key: []byte("o")}},
		[]service.Op{{Key: []byte("f")}},
		writemode.Mode(0))
	if err != nil {
		t.Fatal(err)
	}
	if rec.cmpKey != "p/c" || rec.okKey != "p/o" || rec.failKey != "p/f" {
		t.Errorf("ExecuteWithMode keys cmp=%q ok=%q fail=%q", rec.cmpKey, rec.okKey, rec.failKey)
	}
	if string(resp.Responses[0].Kvs[0].Key) != "k" {
		t.Errorf("ExecuteWithMode stripped key = %q", resp.Responses[0].Kvs[0].Key)
	}

	// ExecuteEntity prefixes compare, success, and failure entity IDs.
	if _, err := w.ExecuteEntity(ctx, &service.TransactionEntityRequest{
		Compares: []service.EntityCompare{{ID: "c"}},
		Success:  []service.TransactionEntityOp{{ID: "s"}},
		Failure:  []service.TransactionEntityOp{{ID: "f"}},
	}); err != nil {
		t.Fatal(err)
	}
	if rec.cmpID != "p/c" || rec.okID != "p/s" || rec.failID != "p/f" {
		t.Errorf("ExecuteEntity IDs cmp=%q ok=%q fail=%q", rec.cmpID, rec.okID, rec.failID)
	}

	// A nil request is forwarded directly.
	if _, err := w.ExecuteEntity(ctx, nil); err != nil {
		t.Fatalf("ExecuteEntity(nil) err = %v", err)
	}
}

func TestNamespacedTransaction_ErrorPassthrough(t *testing.T) {
	rec := &txnRec{err: errBoom}
	w := newNamespacedTransactionService(rec, "p/")
	ctx := context.Background()
	if _, err := w.Execute(ctx, nil, nil, nil); !errors.Is(err, errBoom) {
		t.Errorf("Execute err = %v", err)
	}
	if _, err := w.ExecuteWithMode(ctx, nil, nil, nil, writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Errorf("ExecuteWithMode err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCC wrapper — full revision surface + error paths
// ---------------------------------------------------------------------------

// mvccRec is a full MVCCKVServicer: it embeds the KV mock for the KV surface
// and records/returns prefixed keys for the MVCC-specific revision methods.
type mvccRec struct {
	*mockKVService
	lastKey            string
	lastStart, lastEnd []byte
	compactRev         int64
	compactPrefix      string
	globalCompacts     int
	restoreReq         *service.RestoreRevisionRequest
	conflictKeys       []string
	err                error
}

func newMVCCRec() *mvccRec { return &mvccRec{mockKVService: newMockKVService()} }

func (m *mvccRec) kvOrErr(key string) (*kvKV, error) {
	m.lastKey = key
	if m.err != nil {
		return nil, m.err
	}
	return &kvKV{Key: []byte(key)}, nil
}
func (m *mvccRec) PutWithLease(_ context.Context, key string, _ []byte, _ int64) (*kvKV, error) {
	return m.kvOrErr(key)
}
func (m *mvccRec) PutWithLeaseAndMode(_ context.Context, key string, _ []byte, _ int64, _ writemode.Mode) (*kvKV, error) {
	return m.kvOrErr(key)
}
func (m *mvccRec) GetRevision(_ context.Context, key string) (*kvKV, error) {
	return m.kvOrErr(key)
}
func (m *mvccRec) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*kvKV, error) {
	return m.kvOrErr(key)
}
func (m *mvccRec) GetAtRevision(_ context.Context, key string, _ int64) (*kvKV, error) {
	return m.kvOrErr(key)
}
func (m *mvccRec) GetAtRevisionWithConsistency(_ context.Context, key string, _ int64, _ consistency.ReadOptions) (*kvKV, error) {
	return m.kvOrErr(key)
}
func (m *mvccRec) Range(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*kvKV, int64, int64, error) {
	m.lastStart, m.lastEnd = start, end
	if m.err != nil {
		return nil, 0, 0, m.err
	}
	return []*kvKV{{Key: []byte("p/x")}}, 1, 1, nil
}
func (m *mvccRec) RangeWithConsistency(_ context.Context, start, end []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kvKV, int64, int64, error) {
	m.lastStart, m.lastEnd = start, end
	if m.err != nil {
		return nil, 0, 0, m.err
	}
	return []*kvKV{{Key: []byte("p/x")}}, 1, 1, nil
}

// Compact is the raw service's store-global form, which the real service
// refuses: a namespaced view must never reach it.
func (m *mvccRec) Compact(context.Context, int64) error {
	m.globalCompacts++
	return &service.CompactScopeRequiredError{}
}

// CompactPrefix is the per-prefix compaction the view compacts through.
func (m *mvccRec) CompactPrefix(_ context.Context, prefix string, rev int64) error {
	m.compactPrefix, m.compactRev = prefix, rev
	return m.err
}
func (m *mvccRec) CurrentRevision(_ context.Context) (int64, error) {
	if m.err != nil {
		return 0, m.err
	}
	return 42, nil
}

// RestoreToRevision records the request the wrapper forwarded and echoes the
// scoped keys back as a plan, so a test can assert both that the request was
// namespaced on the way in and that the plan was stripped on the way out.
func (m *mvccRec) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	m.restoreReq = req
	if m.err != nil {
		return nil, m.err
	}
	result := &service.RestoreRevisionResult{Revision: req.Revision, DryRun: req.DryRun}
	for _, k := range req.Keys {
		result.Changes = append(result.Changes, service.RestoreKeyChange{
			Key:    k,
			Action: service.RestoreActionRevert,
		})
	}
	if req.Prefix != "" {
		result.Changes = append(result.Changes, service.RestoreKeyChange{
			Key:    req.Prefix + "x",
			Action: service.RestoreActionRevert,
		})
	}
	result.Applied = len(result.Changes)
	for _, k := range m.conflictKeys {
		result.Conflicts = append(result.Conflicts, service.RestoreKeyChange{
			Key:    k,
			Action: service.RestoreActionRevert,
		})
	}
	result.Conflicted = len(result.Conflicts)
	return result, nil
}

func TestNamespacedMVCC_FullRevisionSurface(t *testing.T) {
	rec := newMVCCRec()
	w := newNamespacedMVCCService(rec, "p/", nil)
	ctx := context.Background()

	getters := []struct {
		name string
		call func() (*kvKV, error)
	}{
		{"PutWithLeaseAndMode", func() (*kvKV, error) {
			return w.PutWithLeaseAndMode(ctx, "k", []byte("v"), 0, writemode.Mode(0))
		}},
		{"GetRevision", func() (*kvKV, error) { return w.GetRevision(ctx, "k") }},
		{"GetRevisionWithConsistency", func() (*kvKV, error) {
			return w.GetRevisionWithConsistency(ctx, "k", consistency.ReadOptions{})
		}},
		{"GetAtRevision", func() (*kvKV, error) { return w.GetAtRevision(ctx, "k", 3) }},
		{"GetAtRevisionWithConsistency", func() (*kvKV, error) {
			return w.GetAtRevisionWithConsistency(ctx, "k", 3, consistency.ReadOptions{})
		}},
	}
	for _, g := range getters {
		rec.lastKey = ""
		kvp, err := g.call()
		if err != nil {
			t.Fatalf("%s: %v", g.name, err)
		}
		if rec.lastKey != "p/k" {
			t.Errorf("%s: inner key = %q, want p/k", g.name, rec.lastKey)
		}
		if string(kvp.Key) != "k" {
			t.Errorf("%s: returned key = %q, want k", g.name, kvp.Key)
		}
	}

	// RangeWithConsistency prefixes bounds and strips returned keys.
	kvs, _, _, err := w.RangeWithConsistency(ctx, []byte("a"), []byte("z"), consistency.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.lastStart) != "p/a" || string(rec.lastEnd) != "p/z" {
		t.Errorf("RangeWithConsistency bounds = %q,%q", rec.lastStart, rec.lastEnd)
	}
	if string(kvs[0].Key) != "x" {
		t.Errorf("RangeWithConsistency stripped = %q", kvs[0].Key)
	}

	// Compact compacts this namespace's prefix and nothing wider; CurrentRevision
	// forwards unchanged (store-global counter).
	if err := w.Compact(ctx, 11); err != nil || rec.compactRev != 11 || rec.compactPrefix != "p/" || rec.globalCompacts != 0 {
		t.Fatalf("Compact err=%v prefix=%q rev=%d global=%d", err, rec.compactPrefix, rec.compactRev, rec.globalCompacts)
	}
	rev, err := w.CurrentRevision(ctx)
	if err != nil || rev != 42 {
		t.Fatalf("CurrentRevision = %d, %v", rev, err)
	}
}

func TestNamespacedMVCC_ErrorPassthrough(t *testing.T) {
	rec := newMVCCRec()
	rec.err = errBoom
	w := newNamespacedMVCCService(rec, "p/", nil)
	ctx := context.Background()

	errCalls := []struct {
		name string
		fn   func() error
	}{
		{"PutWithLease", func() error { _, e := w.PutWithLease(ctx, "k", nil, 0); return e }},
		{"PutWithLeaseAndMode", func() error {
			_, e := w.PutWithLeaseAndMode(ctx, "k", nil, 0, writemode.Mode(0))
			return e
		}},
		{"GetRevision", func() error { _, e := w.GetRevision(ctx, "k"); return e }},
		{"GetRevisionWithConsistency", func() error {
			_, e := w.GetRevisionWithConsistency(ctx, "k", consistency.ReadOptions{})
			return e
		}},
		{"GetAtRevision", func() error { _, e := w.GetAtRevision(ctx, "k", 1); return e }},
		{"GetAtRevisionWithConsistency", func() error {
			_, e := w.GetAtRevisionWithConsistency(ctx, "k", 1, consistency.ReadOptions{})
			return e
		}},
		{"Range", func() error { _, _, _, e := w.Range(ctx, []byte("a"), []byte("z")); return e }},
		{"RangeWithConsistency", func() error {
			_, _, _, e := w.RangeWithConsistency(ctx, []byte("a"), []byte("z"), consistency.ReadOptions{})
			return e
		}},
		{"Compact", func() error { return w.Compact(ctx, 1) }},
		{"CurrentRevision", func() error { _, e := w.CurrentRevision(ctx); return e }},
	}
	for _, c := range errCalls {
		if err := c.fn(); !errors.Is(err, errBoom) {
			t.Errorf("%s: err = %v, want errBoom", c.name, err)
		}
	}
}
