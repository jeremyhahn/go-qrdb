package vector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

var errFault = errors.New("injected kv fault")

// faultKV fails the one operation op on keys under prefix. "Batch" fails a
// PutAllIfAllUnchanged that writes a key under prefix, before any write
// lands, as a refused proposal does.
type faultKV struct {
	*kvstoretest.GuardedMemoryStore
	op, prefix string
}

func (f *faultKV) hit(op, key string) bool {
	return f.op == op && strings.HasPrefix(key, f.prefix)
}

func (f *faultKV) Put(ctx context.Context, key string, value []byte) error {
	if f.hit("Put", key) {
		return errFault
	}
	return f.GuardedMemoryStore.Put(ctx, key, value)
}

func (f *faultKV) Get(ctx context.Context, key string) ([]byte, error) {
	if f.hit("Get", key) {
		return nil, errFault
	}
	return f.GuardedMemoryStore.Get(ctx, key)
}

func (f *faultKV) Delete(ctx context.Context, key string) error {
	if f.hit("Delete", key) {
		return errFault
	}
	return f.GuardedMemoryStore.Delete(ctx, key)
}

func (f *faultKV) Exists(ctx context.Context, key string) (bool, error) {
	if f.hit("Exists", key) {
		return false, errFault
	}
	return f.GuardedMemoryStore.Exists(ctx, key)
}

func (f *faultKV) List(ctx context.Context, prefix string) ([]string, error) {
	if f.hit("List", prefix) {
		return nil, errFault
	}
	return f.GuardedMemoryStore.List(ctx, prefix)
}

func (f *faultKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if f.hit("Scan", prefix) {
		return errFault
	}
	return f.GuardedMemoryStore.Scan(ctx, prefix, fn)
}

func (f *faultKV) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	for _, w := range writes {
		if f.hit("Batch", w.Key) {
			return false, errFault
		}
	}
	return f.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
}

func seededFaultStorage(t *testing.T) (*KVVectorStorage, *faultKV) {
	t.Helper()
	f := &faultKV{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	s := newKVStorage(t, f)
	ctx := context.Background()
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns"}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if err := s.UpsertVector(ctx, "ns", kvVec(1, map[string]string{"k": "v"})); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	return s, f
}

func TestKVVectorStorage_KVFaultsPropagate(t *testing.T) {
	ctx := context.Background()
	filters := []MetadataFilter{{Field: "k", Value: "v", Operator: FilterEqual}}
	calls := map[string]func(s *KVVectorStorage) error{
		"upsert": func(s *KVVectorStorage) error { return s.UpsertVector(ctx, "ns", kvVec(2, nil)) },
		"get": func(s *KVVectorStorage) error {
			_, err := s.GetVector(ctx, "ns", 1)
			return err
		},
		"delete":       func(s *KVVectorStorage) error { return s.DeleteVector(ctx, "ns", 1) },
		"batch-upsert": func(s *KVVectorStorage) error { return s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(3, nil)}) },
		"batch-delete": func(s *KVVectorStorage) error { return s.BatchDeleteVectors(ctx, "ns", []uint64{1}) },
		"scan-ns": func(s *KVVectorStorage) error {
			_, err := s.ScanNamespace(ctx, "ns", nil)
			return err
		},
		"scan-meta": func(s *KVVectorStorage) error {
			_, err := s.ScanByMetadata(ctx, "ns", "k", "v", nil)
			return err
		},
		"scan-filters": func(s *KVVectorStorage) error {
			_, err := s.ScanByMultipleFilters(ctx, "ns", filters, nil)
			return err
		},
		"stats": func(s *KVVectorStorage) error {
			_, err := s.GetStats(ctx, "ns")
			return err
		},
		"create":  func(s *KVVectorStorage) error { return s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns2"}) },
		"update":  func(s *KVVectorStorage) error { return s.UpdateNamespace(ctx, "ns", &NamespaceUpdate{}) },
		"drop-ns": func(s *KVVectorStorage) error { return s.DeleteNamespace(ctx, "ns") },
		"list-ns": func(s *KVVectorStorage) error {
			_, err := s.ListNamespaces(ctx)
			return err
		},
		"info": func(s *KVVectorStorage) error {
			_, err := s.GetNamespaceInfo(ctx, "ns")
			return err
		},
		"next-id": func(s *KVVectorStorage) error {
			_, err := s.NextID("ns")
			return err
		},
		"batch-ids": func(s *KVVectorStorage) error {
			_, err := s.BatchAllocateIDs("ns", 2)
			return err
		},
		"generation": func(s *KVVectorStorage) error {
			_, err := genOf(ctx, s, "ns")
			return err
		},
	}
	type fault struct{ call, op, prefix string }
	faults := []fault{
		{"upsert", "Batch", "v/"},
		{"upsert", "Get", "gen/"},
		{"upsert", "Get", "inc/"},
		{"get", "Get", "v/"},
		{"get", "Get", "inc/"},
		{"delete", "Get", "inc/"},
		{"delete", "Exists", "v/"},
		{"delete", "Batch", "v/"},
		{"delete", "Get", "gen/"},
		{"batch-upsert", "Batch", "v/"},
		{"batch-upsert", "Get", "gen/"},
		{"batch-delete", "Batch", "v/"},
		{"batch-delete", "Get", "gen/"},
		{"scan-ns", "Scan", "v/"},
		{"scan-ns", "Get", "inc/"},
		{"scan-meta", "Scan", "v/"},
		{"scan-filters", "Scan", "v/"},
		{"stats", "Scan", "v/"},
		{"create", "Exists", "ns/"},
		{"create", "Batch", "ns/"},
		{"update", "Get", "inc/"},
		{"update", "Get", "ns/"},
		{"update", "Batch", "ns/"},
		{"drop-ns", "Get", "inc/"},
		{"drop-ns", "Get", "gen/"},
		{"drop-ns", "Batch", "ns/"},
		{"drop-ns", "List", "v/"},
		{"drop-ns", "Delete", "v/"},
		{"list-ns", "Scan", "ns/"},
		{"info", "Get", "ns/"},
		{"info", "Scan", "v/"},
		{"next-id", "Get", "id/"},
		{"next-id", "Batch", "id/"},
		{"batch-ids", "Get", "id/"},
		{"generation", "Get", "gen/"},
		{"generation", "Get", "inc/"},
	}
	for _, fc := range faults {
		t.Run(fc.call+"/"+fc.op+"/"+fc.prefix, func(t *testing.T) {
			s, f := seededFaultStorage(t)
			f.op, f.prefix = fc.op, fc.prefix
			if err := calls[fc.call](s); !errors.Is(err, errFault) {
				t.Fatalf("%s with %s %s failing = %v, want the injected fault", fc.call, fc.op, fc.prefix, err)
			}
		})
	}
}

// TestKVVectorStorage_FailedBatchLeavesDataAndGeneration is Codex round 3
// (c): a write whose batch fails leaves neither its data nor a generation
// step behind, so no replica can see data its index generation does not
// cover.
func TestKVVectorStorage_FailedBatchLeavesDataAndGeneration(t *testing.T) {
	ctx := context.Background()
	writes := map[string]func(s *KVVectorStorage) error{
		"upsert": func(s *KVVectorStorage) error { return s.UpsertVector(ctx, "ns", kvVec(2, nil)) },
		"batch-upsert": func(s *KVVectorStorage) error {
			return s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(2, nil), kvVec(3, nil)})
		},
		"delete":       func(s *KVVectorStorage) error { return s.DeleteVector(ctx, "ns", 1) },
		"batch-delete": func(s *KVVectorStorage) error { return s.BatchDeleteVectors(ctx, "ns", []uint64{1}) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			s, f := seededFaultStorage(t)
			before, err := genOf(ctx, s, "ns")
			if err != nil {
				t.Fatal(err)
			}
			f.op, f.prefix = "Batch", "v/"
			if err := write(s); !errors.Is(err, errFault) {
				t.Fatalf("%s with the batch failing = %v, want the injected fault", name, err)
			}
			f.op = ""
			after, err := genOf(ctx, s, "ns")
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("generation after a failed %s = %d, want %d", name, after, before)
			}
			vecs, err := s.ScanNamespace(ctx, "ns", nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(vecs) != 1 || vecs[0].ID != 1 {
				t.Fatalf("vectors after a failed %s = %v, want only vector 1", name, vecs)
			}
		})
	}
}

func TestKVVectorStorage_CorruptRecordsRefused(t *testing.T) {
	ctx := context.Background()
	garbage := []byte{0xff, 0xfe, 0xfd, 0xfc}
	s, f := seededFaultStorage(t)
	mem := f.GuardedMemoryStore
	inc, err := mem.Get(ctx, kvIncKey("ns"))
	if err != nil {
		t.Fatal(err)
	}
	// A key of a longer namespace sharing the prefix is not a vector.
	if err := mem.Put(ctx, kvVectorNSPrefix("ns", inc)+"zz", garbage); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetStats(ctx, "ns"); err != nil {
		t.Fatalf("GetStats with a foreign key = %v", err)
	}
	if err := mem.Put(ctx, kvVectorKey("ns", inc, 7), garbage); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVector(ctx, "ns", 7); err == nil {
		t.Fatal("GetVector of a corrupt record succeeded")
	}
	if _, err := s.ScanNamespace(ctx, "ns", nil); err == nil {
		t.Fatal("ScanNamespace over a corrupt record succeeded")
	}
	if err := mem.Put(ctx, kvNamespaceKey("ns"), garbage); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNamespaceInfo(ctx, "ns"); err == nil {
		t.Fatal("GetNamespaceInfo of a corrupt record succeeded")
	}
	if _, err := s.ListNamespaces(ctx); err == nil {
		t.Fatal("ListNamespaces over a corrupt record succeeded")
	}
	if err := s.UpdateNamespace(ctx, "ns", &NamespaceUpdate{}); err == nil {
		t.Fatal("UpdateNamespace over a corrupt record succeeded")
	}
	if err := mem.Put(ctx, kvIDKey("ns"), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextID("ns"); err == nil {
		t.Fatal("NextID over a short counter succeeded")
	}
	if err := mem.Put(ctx, kvGenKey("ns"), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := genOf(ctx, s, "ns"); err == nil {
		t.Fatal("Generation over a short counter succeeded")
	}
	if err := s.UpsertVector(ctx, "ns", kvVec(4, nil)); err == nil {
		t.Fatal("UpsertVector advancing a short counter succeeded")
	}
	if err := mem.Put(ctx, kvIncKey("ns"), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVector(ctx, "ns", 1); err == nil {
		t.Fatal("GetVector under a short incarnation succeeded")
	}
}

func TestKVVectorStorage_RefusesInvalidArguments(t *testing.T) {
	ctx := context.Background()
	s := newKVStorage(t, kvstoretest.NewGuardedMemoryStore())
	checks := []struct {
		name string
		err  error
		want error
	}{
		{"upsert nil", s.UpsertVector(ctx, "ns", nil), ErrInvalidVector},
		{"upsert id 0", s.UpsertVector(ctx, "ns", kvVec(0, nil)), ErrInvalidID},
		{"upsert missing namespace", s.UpsertVector(ctx, "ns", kvVec(1, nil)), ErrNamespaceNotFound},
		{"batch upsert missing namespace", s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(1, nil)}), ErrNamespaceNotFound},
		{"batch delete missing namespace", s.BatchDeleteVectors(ctx, "ns", []uint64{1}), ErrNamespaceNotFound},
		{"delete id 0", s.DeleteVector(ctx, "ns", 0), ErrInvalidID},
		{"delete missing", s.DeleteVector(ctx, "ns", 9), ErrVectorNotFound},
		{"batch upsert empty", s.BatchUpsertVectors(ctx, "ns", nil), ErrEmptyBatch},
		{"batch upsert nil", s.BatchUpsertVectors(ctx, "ns", []*VectorValue{nil}), ErrInvalidVector},
		{"batch upsert id 0", s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(0, nil)}), ErrInvalidID},
		{"batch delete empty", s.BatchDeleteVectors(ctx, "ns", nil), ErrEmptyBatch},
		{"create nil", s.CreateNamespace(ctx, nil), ErrInvalidNamespace},
		{"create unnamed", s.CreateNamespace(ctx, &NamespaceInfo{}), ErrInvalidNamespace},
		{"update unnamed", s.UpdateNamespace(ctx, "", &NamespaceUpdate{}), ErrInvalidNamespace},
		{"update nil", s.UpdateNamespace(ctx, "ns", nil), ErrInvalidNamespace},
		{"update missing", s.UpdateNamespace(ctx, "ns", &NamespaceUpdate{}), ErrNamespaceNotFound},
		{"drop missing", s.DeleteNamespace(ctx, "ns"), ErrNamespaceNotFound},
	}
	for _, c := range checks {
		if !errors.Is(c.err, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.err, c.want)
		}
	}
	if _, err := s.GetVector(ctx, "ns", 0); !errors.Is(err, ErrInvalidID) {
		t.Errorf("get id 0 = %v", err)
	}
	if _, err := s.GetVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Errorf("get in missing namespace = %v", err)
	}
	if _, err := s.BatchAllocateIDs("ns", 0); !errors.Is(err, ErrInvalidID) {
		t.Errorf("batch ids 0 = %v", err)
	}
	if _, err := genOf(ctx, s, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("generation missing = %v", err)
	}
	if _, err := s.GetNamespaceInfo(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("info missing = %v", err)
	}
	if _, err := s.NextID("ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("next id missing = %v", err)
	}
}

func TestKVVectorStorage_ClosedAndCanceledEverywhere(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	s := newKVStorage(t, kvstoretest.NewGuardedMemoryStore())
	filters := []MetadataFilter{{Field: "k", Value: "v", Operator: FilterEqual}}
	ctxCalls := func(ctx context.Context) map[string]error {
		_, getErr := s.GetVector(ctx, "ns", 1)
		_, scanErr := s.ScanNamespace(ctx, "ns", nil)
		_, metaErr := s.ScanByMetadata(ctx, "ns", "k", "v", nil)
		_, filtErr := s.ScanByMultipleFilters(ctx, "ns", filters, nil)
		_, genErr := genOf(ctx, s, "ns")
		return map[string]error{
			"get":          getErr,
			"delete":       s.DeleteVector(ctx, "ns", 1),
			"batch-upsert": s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(1, nil)}),
			"batch-delete": s.BatchDeleteVectors(ctx, "ns", []uint64{1}),
			"scan":         scanErr,
			"scan-meta":    metaErr,
			"scan-filters": filtErr,
			"generation":   genErr,
		}
	}
	for name, err := range ctxCalls(canceled) {
		if !errors.Is(err, ErrContextCanceled) {
			t.Errorf("%s canceled = %v", name, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	closed := ctxCalls(ctx)
	_, statsErr := s.GetStats(ctx, "ns")
	_, infoErr := s.GetNamespaceInfo(ctx, "ns")
	_, nextErr := s.NextID("ns")
	_, idsErr := s.BatchAllocateIDs("ns", 1)
	_, filtersErr := s.ScanByMultipleFilters(ctx, "ns", nil, nil)
	closed["upsert"] = s.UpsertVector(ctx, "ns", kvVec(1, nil))
	closed["stats"] = statsErr
	closed["info"] = infoErr
	closed["next-id"] = nextErr
	closed["batch-ids"] = idsErr
	closed["scan-filters-none"] = filtersErr
	closed["create"] = s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns"})
	closed["update"] = s.UpdateNamespace(ctx, "ns", &NamespaceUpdate{})
	closed["drop"] = s.DeleteNamespace(ctx, "ns")
	closed["compact"] = s.Compact(ctx, "ns")
	closed["tombstones"] = s.CleanupTombstones(ctx, 0)
	for name, err := range closed {
		if !errors.Is(err, ErrStorageClosed) {
			t.Errorf("%s closed = %v", name, err)
		}
	}
}
