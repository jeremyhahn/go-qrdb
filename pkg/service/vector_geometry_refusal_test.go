package service

import (
	"context"
	"errors"
	"testing"

	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newRestartVectorStorage opens a PebbleDB-backed vector store at dir.
func newRestartVectorStorage(t *testing.T, dir string) *vectorstorage.PebbleVectorStorage {
	t.Helper()
	cfg := vectorstorage.DefaultConfig(dir)
	store, err := vectorstorage.NewPebbleVectorStorage(cfg, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage(%q): %v", dir, err)
	}
	return store
}

// seedStoredGeometry writes a namespace record and one vector straight to
// storage, as an earlier release that did not check the metric or index name
// at create left them.
func seedStoredGeometry(t *testing.T, dir, logical, metric, indexType string) {
	t.Helper()
	ctx := context.Background()
	store := newRestartVectorStorage(t, dir)
	scoped := scopedNamespace(tenantid.System, logical)
	if err := store.CreateNamespace(ctx, &vectorstorage.NamespaceInfo{
		Name:           scoped,
		Dimension:      2,
		DistanceMetric: metric,
		IndexType:      indexType,
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if err := store.UpsertVector(ctx, scoped, &vectorstorage.VectorValue{ID: 1, Embedding: []float32{1, 0}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// reopenVectorService constructs a service over the store in dir, as a
// process restart does.
func reopenVectorService(t *testing.T, dir string) VectorService {
	t.Helper()
	store := newRestartVectorStorage(t, dir)
	t.Cleanup(func() { _ = store.Close() })
	svc, err := NewVectorService(store, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	return svc
}

func assertUnsupportedGeometry(t *testing.T, op string, err error, field, value string) {
	t.Helper()
	var unsupported *UnsupportedNamespaceGeometryError
	if !errors.As(err, &unsupported) {
		t.Fatalf("%s: err = %v, want *UnsupportedNamespaceGeometryError", op, err)
	}
	if unsupported.Namespace != "legacy" || unsupported.Field != field || unsupported.Value != value {
		t.Fatalf("%s: err = %+v, want namespace legacy, %s %q", op, unsupported, field, value)
	}
}

func TestVectorService_RestartRefusesStoredUnknownGeometry(t *testing.T) {
	cases := []struct {
		name, metric, indexType, field, value string
	}{
		{name: "metric", metric: "HAMMING", indexType: "HNSW", field: "distance metric", value: "HAMMING"},
		{name: "index type", metric: "L2", indexType: "LSH", field: "index type", value: "LSH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			seedStoredGeometry(t, dir, "legacy", tc.metric, tc.indexType)
			svc := reopenVectorService(t, dir)
			ctx := context.Background()

			resp, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "legacy", QueryVector: []float32{1, 0}, K: 1})
			if resp != nil {
				t.Fatalf("SearchSimilar answered %+v, want a refusal, never an empty result", resp)
			}
			assertUnsupportedGeometry(t, "SearchSimilar", err, tc.field, tc.value)

			_, err = svc.HybridSearch(ctx, &HybridSearchRequest{Namespace: "legacy", QueryVector: []float32{1, 0}, K: 1})
			assertUnsupportedGeometry(t, "HybridSearch", err, tc.field, tc.value)

			_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "legacy", ID: 2, Vector: []float32{0, 1}})
			assertUnsupportedGeometry(t, "UpsertVector", err, tc.field, tc.value)

			_, err = svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "legacy", Vectors: []*VectorData{{ID: 3, Vector: []float32{0, 1}}}})
			assertUnsupportedGeometry(t, "BatchUpsert", err, tc.field, tc.value)

			_, err = svc.GetNamespaceInfo(ctx, &GetNamespaceInfoRequest{Namespace: "legacy"})
			assertUnsupportedGeometry(t, "GetNamespaceInfo", err, tc.field, tc.value)

			// The refusal leaves the operator a way out: the namespace drops.
			if _, err := svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "legacy"}); err != nil {
				t.Fatalf("DeleteNamespace: %v", err)
			}
		})
	}
}

func TestUnsupportedNamespaceGeometryError_NamesNamespaceFieldAndValue(t *testing.T) {
	err := &UnsupportedNamespaceGeometryError{Namespace: "docs", Field: "index type", Value: "LSH"}
	want := `service: vector namespace "docs" stores unsupported index type "LSH"; drop and recreate it with a supported geometry`
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestWithClusterIsolationPolicyReader_OperatorAccess drives CreateNamespace
// with no tenant bound (the system tenant) into a namespace that already
// exists. The isolation check runs before the existence check, so the error
// the call returns names which gate stopped it: under operator-access the
// call passes the isolation gate and stops at NamespaceExistsError, under
// strict it stops at TenantRequiredError. Neither path builds an index, so
// the test needs no qrdb_vector build.
func TestWithClusterIsolationPolicyReader_OperatorAccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy tenant.ClusterIsolationPolicy
		check  func(t *testing.T, err error)
	}{
		{"operator-access passes the isolation gate", tenant.ClusterIsolationPolicyOperatorAccess, func(t *testing.T, err error) {
			var exists *NamespaceExistsError
			if !errors.As(err, &exists) {
				t.Fatalf("CreateNamespace error = %v, want *NamespaceExistsError (the isolation gate let the call through)", err)
			}
		}},
		{"strict refuses the system tenant", tenant.ClusterIsolationPolicyStrict, func(t *testing.T, err error) {
			var required *TenantRequiredError
			if !errors.As(err, &required) {
				t.Fatalf("CreateNamespace error = %v, want *TenantRequiredError", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := newVecMockStorage()
			svc, err := NewVectorService(storage, newVecTestLogger(), WithClusterIsolationPolicyReader(
				fixedClusterIsolationPolicyReader{policy: tc.policy}))
			if err != nil {
				t.Fatalf("NewVectorService: %v", err)
			}
			// Seed the namespace only after construction, so start-up loads
			// (and indexes) nothing.
			storage.getNamespaceInfoFn = func(_ context.Context, namespace string) (*vectorstorage.NamespaceInfo, error) {
				return &vectorstorage.NamespaceInfo{Name: namespace, Dimension: 128}, nil
			}
			resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
				Namespace: "test-ns",
				Dimension: 128,
			})
			if resp != nil {
				t.Fatalf("CreateNamespace response = %+v, want nil", resp)
			}
			tc.check(t, err)
		})
	}
}
