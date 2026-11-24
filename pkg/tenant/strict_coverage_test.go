package tenant

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// faultyStore is an atomic in-memory store whose reads, lists and guarded
// writes can be made to fail, so the error branches of the strict-tightening
// and cluster-floor paths are driven through the real code.
type faultyStore struct {
	*seal.MemoryBackend
	getErr         map[string]error
	listErr        error
	putAllErr      error
	putAllMiss     bool
	putIfAbsentErr error
	casErr         error
}

func newFaultyStore() *faultyStore {
	return &faultyStore{MemoryBackend: seal.NewMemoryBackend(), getErr: map[string]error{}}
}

func (f *faultyStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err, ok := f.getErr[key]; ok {
		return nil, err
	}
	return f.MemoryBackend.Get(ctx, key)
}

func (f *faultyStore) List(ctx context.Context, prefix string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.MemoryBackend.List(ctx, prefix)
}

func (f *faultyStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if f.putAllErr != nil {
		return false, f.putAllErr
	}
	if f.putAllMiss {
		return false, nil
	}
	return f.MemoryBackend.PutAllIfAllUnchanged(ctx, guards, writes)
}

func (f *faultyStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.putIfAbsentErr != nil {
		return false, f.putIfAbsentErr
	}
	return f.MemoryBackend.PutIfAbsent(ctx, key, value)
}

func (f *faultyStore) CompareAndSwap(ctx context.Context, key string, expected, value []byte) (bool, error) {
	if f.casErr != nil {
		return false, f.casErr
	}
	return f.MemoryBackend.CompareAndSwap(ctx, key, expected, value)
}

// policyOnlyBackend is a clusterPolicyBackend without the batched guarded
// write a fenced floor change needs.
type policyOnlyBackend struct{ inner *seal.MemoryBackend }

func (p policyOnlyBackend) Get(ctx context.Context, key string) ([]byte, error) {
	return p.inner.Get(ctx, key)
}

func (p policyOnlyBackend) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return p.inner.PutIfAbsent(ctx, key, value)
}

func (p policyOnlyBackend) CompareAndSwap(ctx context.Context, key string, expected, value []byte) (bool, error) {
	return p.inner.CompareAndSwap(ctx, key, expected, value)
}

// plainStorage hides every capability but seal.StorageBackend's own.
type plainStorage struct{ seal.StorageBackend }

func storeErrorOp(t *testing.T, err error, op string) {
	t.Helper()
	var se *StoreError
	if !errors.As(err, &se) || se.Op != op {
		t.Fatalf("err = %v, want *StoreError with Op %q", err, op)
	}
}

func TestIsolationEpochs_ReadErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("read boom")

	store := newFaultyStore()
	store.getErr[isolationEpochPrefix+"acme"] = boom
	_, err := isolationEpochs(ctx, store, "acme")
	storeErrorOp(t, err, "isolation-epoch-read")
	if !errors.Is(err, boom) {
		t.Fatalf("isolationEpochs = %v, want it to wrap the read error", err)
	}

	store = newFaultyStore()
	_ = store.Put(ctx, isolationEpochPrefix+"acme", []byte("tenant"))
	store.getErr[clusterEpochKey] = seal.ErrSecretNotFound
	got, err := isolationEpochs(ctx, store, "acme")
	if err != nil || len(got) != 1 || string(got[isolationEpochPrefix+"acme"]) != "tenant" {
		t.Fatalf("isolationEpochs = (%v, %v), want only the tenant epoch", got, err)
	}
}

func TestIsolationTightening_StoreErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	live := markerValue(time.Now().Add(time.Hour))
	marker := tighteningPrefix + "acme/deadbeef"

	cases := []struct {
		name   string
		setup  func() markerStore
		wantOp string
	}{
		{"list absent", func() markerStore {
			s := newFaultyStore()
			s.listErr = seal.ErrSecretNotFound
			return s
		}, ""},
		{"list error", func() markerStore {
			s := newFaultyStore()
			s.listErr = boom
			return s
		}, "isolation-tightening-list"},
		{"marker gone between list and read", func() markerStore {
			s := newFaultyStore()
			_ = s.Put(ctx, marker, live)
			s.getErr[marker] = seal.ErrSecretNotFound
			return s
		}, ""},
		{"read error", func() markerStore {
			s := newFaultyStore()
			_ = s.Put(ctx, marker, live)
			s.getErr[marker] = boom
			return s
		}, "isolation-tightening-read"},
		{"reap error", func() markerStore {
			s := newMemMarkerStore()
			_ = s.Put(ctx, marker, markerValue(time.Unix(1, 0)))
			s.deleteErr = boom
			return s
		}, "isolation-tightening-reap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			on, err := isolationTightening(ctx, tc.setup(), "acme")
			if tc.wantOp == "" {
				if err != nil || on {
					t.Fatalf("isolationTightening = (%v, %v), want (false, nil)", on, err)
				}
				return
			}
			if on {
				t.Fatal("isolationTightening reported a tightening on a store error")
			}
			storeErrorOp(t, err, tc.wantOp)
		})
	}
}

func TestMarkerLive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name  string
		value []byte
		want  bool
	}{
		{"short value", []byte{1, 2, 3}, false},
		{"past deadline", markerValue(now.Add(-time.Second)), false},
		{"future deadline", markerValue(now.Add(time.Second)), true},
	}
	for _, tc := range cases {
		if got := markerLive(tc.value, now); got != tc.want {
			t.Fatalf("%s: markerLive = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFencedSwap_Outcomes(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("get boom")
	markerKey := tighteningPrefix + "acme/cafe"
	guard := kvstore.Guard{Key: markerKey, Value: markerValue(time.Now().Add(time.Hour))}
	fenced := withTighteningFence(ctx, guard)

	if handled, _, err := fencedSwap(ctx, seal.NewMemoryBackend(), "k", nil, []byte("v")); handled || err != nil {
		t.Fatalf("unfenced fencedSwap = (%v, %v), want not handled", handled, err)
	}
	if handled, swapped, err := fencedSwap(fenced, newMemMarkerStore(), "k", nil, []byte("v")); !handled || swapped || !errors.Is(err, ErrTighteningNotAtomic) {
		t.Fatalf("fencedSwap on a non-batching store = (%v, %v, %v), want ErrTighteningNotAtomic", handled, swapped, err)
	}

	// Marker standing, key changed under it: handled, not swapped.
	store := seal.NewMemoryBackend()
	_ = store.Put(ctx, markerKey, guard.Value)
	_ = store.Put(ctx, "k", []byte("other"))
	if handled, swapped, err := fencedSwap(fenced, store, "k", []byte("expected"), []byte("v")); !handled || swapped || err != nil {
		t.Fatalf("guard miss with marker standing = (%v, %v, %v), want handled, unswapped, nil", handled, swapped, err)
	}
	if got, _ := store.Get(ctx, "k"); string(got) != "other" {
		t.Fatalf("stored value = %q, want it untouched", got)
	}

	// Marker gone: the lease expired.
	if handled, swapped, err := fencedSwap(fenced, seal.NewMemoryBackend(), "k", nil, []byte("v")); !handled || swapped || !errors.Is(err, ErrTighteningLeaseExpired) {
		t.Fatalf("guard miss with marker gone = (%v, %v, %v), want ErrTighteningLeaseExpired", handled, swapped, err)
	}

	// Marker read fails: the error propagates.
	fs := newFaultyStore()
	fs.getErr[markerKey] = boom
	if handled, swapped, err := fencedSwap(fenced, fs, "k", nil, []byte("v")); !handled || swapped || !errors.Is(err, boom) {
		t.Fatalf("marker read error = (%v, %v, %v), want the read error", handled, swapped, err)
	}

	// Marker standing, guard holds: swapped.
	store = seal.NewMemoryBackend()
	_ = store.Put(ctx, markerKey, guard.Value)
	if handled, swapped, err := fencedSwap(fenced, store, "k", nil, []byte("v")); !handled || !swapped || err != nil {
		t.Fatalf("fenced swap = (%v, %v, %v), want swapped", handled, swapped, err)
	}
	if got, _ := store.Get(ctx, "k"); string(got) != "v" {
		t.Fatalf("stored value = %q, want v", got)
	}
}

func TestStrictTighteningErrors_Messages(t *testing.T) {
	if msg := (&SystemAuditRouteError{TenantID: "acme"}).Error(); !strings.Contains(msg, `"acme"`) || !strings.Contains(msg, "system shard") {
		t.Fatalf("SystemAuditRouteError = %q", msg)
	}
	if msg := (&NodeKeyLogsError{TenantID: "acme", Count: 2}).Error(); !strings.Contains(msg, "2 database(s)") {
		t.Fatalf("NodeKeyLogsError without names = %q", msg)
	}
	if msg := (&NodeKeyLogsError{TenantID: "acme", Count: 2, Databases: []string{"orders", "users"}}).Error(); !strings.Contains(msg, "orders, users") {
		t.Fatalf("NodeKeyLogsError with names = %q", msg)
	}
}

func TestIsolationChangeGuard_UnwiredProbesFailClosed(t *testing.T) {
	ctx := context.Background()
	raft := &TenantInfo{ID: "acme", IsolationType: IsolationRaft}
	patch := TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}

	cases := []struct {
		name string
		wire func(g *isolationChangeGuard)
		want error
	}{
		{"node-key probe unwired", func(g *isolationChangeGuard) {
			g.SetNodeKeyLogProbe(&fixedProbe{dbs: map[string][]string{}})
			g.SetAuditRouteProbe(noSystemAuditRoute{})
			g.SetNodeKeyLogProbe(nil)
		}, ErrNodeKeyLogsUnverifiable},
		{"audit-route probe unwired", func(g *isolationChangeGuard) {
			g.SetNodeKeyLogProbe(&fixedProbe{dbs: map[string][]string{}})
			g.SetAuditRouteProbe(noSystemAuditRoute{})
			g.SetAuditRouteProbe(nil)
		}, ErrAuditRouteUnverifiable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var g isolationChangeGuard
			tc.wire(&g)
			committed := false
			err := g.guardedUpdate(ctx, newMemMarkerStore(), raft, patch, func(context.Context) error {
				committed = true
				return nil
			})
			if !errors.Is(err, tc.want) || committed {
				t.Fatalf("guardedUpdate = %v (committed %v), want %v and no commit", err, committed, tc.want)
			}
		})
	}
}

func newServiceWithOptions(t *testing.T, store seal.StorageBackend, opts ...TenantServiceOption) *TenantService {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	opts = append([]TenantServiceOption{WithVolumeAllocator(volumeAllocatorFor(t, reg))}, opts...)
	svc, err := NewTenantService(store, reg, testLogger(), opts...)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc
}

func TestTenantService_ProbeOptionsWireTheGuard(t *testing.T) {
	ctx := context.Background()
	svc := newServiceWithOptions(t, seal.NewMemoryBackend(),
		WithNodeKeyLogProbe(&fixedProbe{dbs: map[string][]string{"acme": {"orders"}}}),
		WithAuditRouteProbe(noSystemAuditRoute{}))
	err := svc.guardedUpdate(ctx, newMemMarkerStore(), &TenantInfo{ID: "acme", IsolationType: IsolationRaft},
		TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}, func(context.Context) error { return nil })
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "acme" {
		t.Fatalf("guardedUpdate through the option-wired probe = %v, want *NodeKeyLogsError for acme", err)
	}

	svc = newServiceWithOptions(t, seal.NewMemoryBackend(),
		WithNodeKeyLogProbe(&fixedProbe{dbs: map[string][]string{}}),
		WithAuditRouteProbe(systemAuditRoute{"acme": true}))
	err = svc.guardedUpdate(ctx, newMemMarkerStore(), &TenantInfo{ID: "acme", IsolationType: IsolationRaft},
		TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}, func(context.Context) error { return nil })
	var sa *SystemAuditRouteError
	if !errors.As(err, &sa) {
		t.Fatalf("guardedUpdate through the option-wired audit probe = %v, want *SystemAuditRouteError", err)
	}
}

func TestServices_IsolationReaders(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	kv := &kvTenantService{store: newMemKVStore(), logger: slog.Default()}

	readers := []struct {
		name string
		tr   IsolationTighteningReader
		er   IsolationEpochReader
	}{
		{"TenantService", svc, svc},
		{"kvTenantService", kv, kv},
	}
	for _, r := range readers {
		if on, err := r.tr.IsolationTightening(ctx, "acme"); err != nil || on {
			t.Fatalf("%s.IsolationTightening = (%v, %v), want (false, nil)", r.name, on, err)
		}
		if got, err := r.er.IsolationEpochs(ctx, "acme"); err != nil || len(got) != 0 {
			t.Fatalf("%s.IsolationEpochs = (%v, %v), want empty", r.name, got, err)
		}
	}
}

func TestStrictIsolationGate_Wiring(t *testing.T) {
	gateOff := errors.New("strict isolation not active")
	svc := newServiceWithOptions(t, seal.NewMemoryBackend(), WithStrictIsolationGate(func() error { return gateOff }))
	if err := RequireStrictIsolation(svc); !errors.Is(err, gateOff) {
		t.Fatalf("RequireStrictIsolation with the option-wired gate = %v, want the gate's error", err)
	}
	svc.SetStrictIsolationGate(func() error { return nil })
	if err := RequireStrictIsolation(svc); err != nil {
		t.Fatalf("RequireStrictIsolation with an open gate = %v, want nil", err)
	}
	svc.SetStrictIsolationGate(nil)
	if svc.StrictIsolationCheck() != nil {
		t.Fatal("StrictIsolationCheck after SetStrictIsolationGate(nil) is not nil")
	}
	if err := RequireStrictIsolation(svc); err == nil || errors.Is(err, gateOff) {
		t.Fatalf("RequireStrictIsolation with no gate = %v, want the feature-not-active refusal", err)
	}
}

func TestSetClusterIsolationPolicy_StoreFaults(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	markerKey := clusterTighteningPrefix + "cafe"
	guard := kvstore.Guard{Key: markerKey, Value: markerValue(time.Now().Add(time.Hour))}
	fenced := withTighteningFence(ctx, guard)

	t.Run("read error", func(t *testing.T) {
		s := newFaultyStore()
		s.getErr[clusterIsolationPolicyKey] = boom
		_, err := getClusterIsolationPolicy(ctx, s)
		storeErrorOp(t, err, "cluster-isolation-policy-get")
		storeErrorOp(t, setClusterIsolationPolicy(ctx, s, ClusterIsolationPolicyStrict), "cluster-isolation-policy-get")
	})
	t.Run("fenced on a non-batching backend", func(t *testing.T) {
		err := setClusterIsolationPolicy(fenced, policyOnlyBackend{seal.NewMemoryBackend()}, ClusterIsolationPolicyStrict)
		if !errors.Is(err, ErrTighteningNotAtomic) {
			t.Fatalf("err = %v, want ErrTighteningNotAtomic", err)
		}
	})
	t.Run("fenced batch error", func(t *testing.T) {
		s := newFaultyStore()
		s.putAllErr = boom
		storeErrorOp(t, setClusterIsolationPolicy(fenced, s, ClusterIsolationPolicyStrict), "cluster-isolation-policy-swap")
	})
	t.Run("fenced lost race with marker standing", func(t *testing.T) {
		s := newFaultyStore()
		_ = s.Put(ctx, markerKey, guard.Value)
		s.putAllMiss = true
		var ce *IsolationPolicyChangeError
		if err := setClusterIsolationPolicy(fenced, s, ClusterIsolationPolicyStrict); !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *IsolationPolicyChangeError", err)
		}
		if _, err := s.MemoryBackend.Get(ctx, clusterIsolationPolicyKey); err == nil {
			t.Fatal("a lost race stored the floor")
		}
	})
	t.Run("fenced with the marker gone", func(t *testing.T) {
		err := setClusterIsolationPolicy(fenced, newFaultyStore(), ClusterIsolationPolicyStrict)
		if !errors.Is(err, ErrTighteningLeaseExpired) {
			t.Fatalf("err = %v, want ErrTighteningLeaseExpired", err)
		}
	})
	t.Run("fenced commit", func(t *testing.T) {
		s := newFaultyStore()
		_ = s.Put(ctx, markerKey, guard.Value)
		if err := setClusterIsolationPolicy(fenced, s, ClusterIsolationPolicyStrict); err != nil {
			t.Fatalf("err = %v", err)
		}
		if got, _ := s.Get(ctx, clusterIsolationPolicyKey); string(got) != string(ClusterIsolationPolicyStrict) {
			t.Fatalf("stored floor = %q, want strict", got)
		}
	})
	t.Run("first write error", func(t *testing.T) {
		s := newFaultyStore()
		s.putIfAbsentErr = boom
		storeErrorOp(t, setClusterIsolationPolicy(ctx, s, ClusterIsolationPolicyStrict), "cluster-isolation-policy-put")
	})
	t.Run("compare-and-swap error", func(t *testing.T) {
		s := newFaultyStore()
		_ = s.Put(ctx, clusterIsolationPolicyKey, []byte(ClusterIsolationPolicyOperatorAccess))
		s.casErr = boom
		storeErrorOp(t, setClusterIsolationPolicy(ctx, s, ClusterIsolationPolicyStrict), "cluster-isolation-policy-swap")
	})
}

func TestTenantService_SetClusterIsolationPolicy_StoreFaults(t *testing.T) {
	ctx := context.Background()
	svc := newServiceWithOptions(t, plainStorage{seal.NewMemoryBackend()})
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); !errors.Is(err, ErrTenantStoreNotAtomic) {
		t.Fatalf("SetClusterIsolationPolicy on a non-atomic store = %v, want ErrTenantStoreNotAtomic", err)
	}

	s := newFaultyStore()
	svc = newServiceWithOptions(t, s)
	s.getErr[clusterIsolationPolicyKey] = errors.New("boom")
	storeErrorOp(t, svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict), "cluster-isolation-policy-get")
}

func TestCheckIsolationPolicyChangeFor_InvalidValue(t *testing.T) {
	err := checkIsolationPolicyChangeFor(&TenantInfo{ID: "acme"}, TenantPatch{IsolationPolicy: "bogus"})
	var inv *InvalidIsolationPolicyError
	if !errors.As(err, &inv) || inv.Value != "bogus" {
		t.Fatalf("checkIsolationPolicyChangeFor = %v, want *InvalidIsolationPolicyError for bogus", err)
	}
}
