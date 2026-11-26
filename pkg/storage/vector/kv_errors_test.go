package vector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

var errFault = errors.New("injected kv fault")

// faultKV is a MemoryKVStore that fails one operation on keys under one
// prefix, so each KV error branch of KVVectorStorage can be reached.
type faultKV struct {
	*kvstore.MemoryKVStore
	op, prefix string
}

func (f *faultKV) hit(op, key string) bool {
	return f.op == op && strings.HasPrefix(key, f.prefix)
}

func (f *faultKV) Put(ctx context.Context, key string, value []byte) error {
	if f.hit("Put", key) {
		return errFault
	}
	return f.MemoryKVStore.Put(ctx, key, value)
}

func (f *faultKV) Get(ctx context.Context, key string) ([]byte, error) {
	if f.hit("Get", key) {
		return nil, errFault
	}
	return f.MemoryKVStore.Get(ctx, key)
}

func (f *faultKV) Delete(ctx context.Context, key string) error {
	if f.hit("Delete", key) {
		return errFault
	}
	return f.MemoryKVStore.Delete(ctx, key)
}

func (f *faultKV) Exists(ctx context.Context, key string) (bool, error) {
	if f.hit("Exists", key) {
		return false, errFault
	}
	return f.MemoryKVStore.Exists(ctx, key)
}

func (f *faultKV) List(ctx context.Context, prefix string) ([]string, error) {
	if f.hit("List", prefix) {
		return nil, errFault
	}
	return f.MemoryKVStore.List(ctx, prefix)
}

func (f *faultKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if f.hit("Scan", prefix) {
		return errFault
	}
	return f.MemoryKVStore.Scan(ctx, prefix, fn)
}

func (f *faultKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.hit("PutIfAbsent", key) {
		return false, errFault
	}
	return f.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func (f *faultKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if f.hit("CompareAndSwap", key) {
		return false, errFault
	}
	return f.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

// kvModes builds a store over the fault double with the atomic capability
// exposed, and one with it hidden, so both allocation paths run.
func kvModes() map[string]func(*faultKV) kvstore.KVStore {
	return map[string]func(*faultKV) kvstore.KVStore{
		"atomic": func(f *faultKV) kvstore.KVStore { return f },
		"plain":  func(f *faultKV) kvstore.KVStore { return plainKV{f} },
	}
}

func seededFaultStorage(t *testing.T, wrap func(*faultKV) kvstore.KVStore) (*KVVectorStorage, *faultKV) {
	t.Helper()
	f := &faultKV{MemoryKVStore: kvstore.NewMemoryKVStore()}
	s := newKVStorage(t, wrap(f))
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
			_, err := s.Generation(ctx, "ns")
			return err
		},
	}
	type fault struct{ call, op, prefix, mode string }
	faults := []fault{
		{"upsert", "Put", "v/", ""},
		{"upsert", "Get", "gen/", ""},
		{"upsert", "CompareAndSwap", "gen/", "atomic"},
		{"upsert", "Put", "gen/", "plain"},
		{"get", "Get", "v/", ""},
		{"delete", "Exists", "v/", ""},
		{"delete", "Delete", "v/", ""},
		{"delete", "Get", "gen/", ""},
		{"batch-upsert", "Put", "v/", ""},
		{"batch-upsert", "Get", "gen/", ""},
		{"batch-delete", "Delete", "v/", ""},
		{"batch-delete", "Get", "gen/", ""},
		{"scan-ns", "Scan", "v/", ""},
		{"scan-meta", "Scan", "v/", ""},
		{"scan-filters", "Scan", "v/", ""},
		{"stats", "Scan", "v/", ""},
		{"create", "Exists", "ns/", ""},
		{"create", "PutIfAbsent", "id/", "atomic"},
		{"create", "PutIfAbsent", "ns/", "atomic"},
		{"create", "Put", "id/", "plain"},
		{"create", "Put", "gen/", "plain"},
		{"create", "Put", "ns/", "plain"},
		{"update", "Get", "ns/", ""},
		{"update", "Put", "ns/", ""},
		{"drop-ns", "Exists", "ns/", ""},
		{"drop-ns", "List", "v/", ""},
		{"drop-ns", "Delete", "v/", ""},
		{"drop-ns", "Delete", "id/", ""},
		{"drop-ns", "Delete", "gen/", ""},
		{"drop-ns", "Delete", "ns/", ""},
		{"list-ns", "Scan", "ns/", ""},
		{"info", "Get", "ns/", ""},
		{"info", "Scan", "v/", ""},
		{"next-id", "Get", "id/", ""},
		{"next-id", "CompareAndSwap", "id/", "atomic"},
		{"next-id", "Put", "id/", "plain"},
		{"batch-ids", "Get", "id/", ""},
		{"generation", "Get", "gen/", ""},
	}
	for mode, wrap := range kvModes() {
		for _, fc := range faults {
			if fc.mode != "" && fc.mode != mode {
				continue
			}
			t.Run(mode+"/"+fc.call+"/"+fc.op+"/"+fc.prefix, func(t *testing.T) {
				s, f := seededFaultStorage(t, wrap)
				f.op, f.prefix = fc.op, fc.prefix
				if err := calls[fc.call](s); !errors.Is(err, errFault) {
					t.Fatalf("%s with %s %s failing = %v, want the injected fault", fc.call, fc.op, fc.prefix, err)
				}
			})
		}
	}
}

func TestKVVectorStorage_CorruptRecordsRefused(t *testing.T) {
	ctx := context.Background()
	garbage := []byte{0xff, 0xfe, 0xfd, 0xfc}
	for mode, wrap := range kvModes() {
		t.Run(mode, func(t *testing.T) {
			s, f := seededFaultStorage(t, wrap)
			mem := f.MemoryKVStore
			if err := mem.Put(ctx, kvVectorKey("bad", 7), garbage); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetVector(ctx, "bad", 7); err == nil {
				t.Fatal("GetVector of a corrupt record succeeded")
			}
			if _, err := s.ScanNamespace(ctx, "bad", nil); err == nil {
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
			if err := mem.Put(ctx, kvIDKey("ns"), []byte{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.NextID("ns"); err == nil {
				t.Fatal("NextID over a short counter succeeded")
			}
			if err := mem.Put(ctx, kvGenKey("ns"), []byte{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Generation(ctx, "ns"); err == nil {
				t.Fatal("Generation over a short counter succeeded")
			}
			if err := s.UpsertVector(ctx, "ns", kvVec(4, nil)); err == nil {
				t.Fatal("UpsertVector advancing a short counter succeeded")
			}
			// A key of a longer namespace sharing the prefix is not a vector.
			if err := mem.Put(ctx, kvVectorNSPrefix("ns")+"zz", garbage); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetStats(ctx, "ns"); err != nil {
				t.Fatalf("GetStats with a foreign key = %v", err)
			}
		})
	}
}

func TestKVVectorStorage_RefusesInvalidArguments(t *testing.T) {
	ctx := context.Background()
	s := newKVStorage(t, kvstore.NewMemoryKVStore())
	checks := []struct {
		name string
		err  error
		want error
	}{
		{"upsert nil", s.UpsertVector(ctx, "ns", nil), ErrInvalidVector},
		{"upsert id 0", s.UpsertVector(ctx, "ns", kvVec(0, nil)), ErrInvalidID},
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
	if _, err := s.BatchAllocateIDs("ns", 0); !errors.Is(err, ErrInvalidID) {
		t.Errorf("batch ids 0 = %v", err)
	}
	if _, err := s.Generation(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("generation missing = %v", err)
	}
	if _, err := s.GetNamespaceInfo(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("info missing = %v", err)
	}
	plain := newKVStorage(t, plainKV{kvstore.NewMemoryKVStore()})
	if _, err := plain.NextID("ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("plain next id missing = %v", err)
	}
	if _, err := s.NextID("ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("atomic next id missing = %v", err)
	}
}

func TestKVVectorStorage_ClosedAndCanceledEverywhere(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	s := newKVStorage(t, kvstore.NewMemoryKVStore())
	filters := []MetadataFilter{{Field: "k", Value: "v", Operator: FilterEqual}}
	ctxCalls := func(ctx context.Context) map[string]error {
		_, getErr := s.GetVector(ctx, "ns", 1)
		_, scanErr := s.ScanNamespace(ctx, "ns", nil)
		_, metaErr := s.ScanByMetadata(ctx, "ns", "k", "v", nil)
		_, filtErr := s.ScanByMultipleFilters(ctx, "ns", filters, nil)
		_, genErr := s.Generation(ctx, "ns")
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
