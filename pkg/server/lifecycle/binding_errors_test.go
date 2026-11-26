package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Every wrapping build error names its cause and unwraps to it, so a caller
// matches the cause with errors.Is through the typed wrapper.
func TestBuildErrors_WrapAndUnwrap(t *testing.T) {
	cause := errors.New("boom")
	cases := []struct {
		name string
		err  error
	}{
		{"barrier service", &BarrierServiceError{Err: cause}},
		{"database catalog", &DatabaseCatalogError{Err: cause}},
		{"role service", &RoleServiceError{Err: cause}},
		{"group service", &GroupServiceError{Err: cause}},
		{"rag chunk defaults", &RAGChunkDefaultsError{Err: cause}},
		{"binding open", &BindingOpenError{TenantID: "acme", Database: "orders", Backend: database.BackendPebble, Err: cause}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, cause) {
				t.Fatalf("errors.Is(%T, cause) = false, want true", tc.err)
			}
			if !strings.Contains(tc.err.Error(), "boom") {
				t.Fatalf("%T.Error() = %q, want the cause in it", tc.err, tc.err.Error())
			}
		})
	}
}

// A binding open failure names the tenant, database and backend, and a nil
// cause still renders.
func TestBindingOpenError_NamesTheBinding(t *testing.T) {
	e := &BindingOpenError{TenantID: "acme", Database: "orders", Backend: database.BackendFile}
	msg := e.Error()
	for _, want := range []string{"acme", "orders", "file", "<nil>"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("BindingOpenError.Error() = %q, want %q in it", msg, want)
		}
	}
	if e.Unwrap() != nil {
		t.Fatalf("Unwrap() = %v, want nil", e.Unwrap())
	}
}

// The binding refusals name the tenant (and database) they refuse.
func TestBindingRefusals_NameTheTenant(t *testing.T) {
	cases := []struct {
		err  error
		want []string
	}{
		{&BindingTenantUnavailableError{TenantID: "acme"}, []string{"acme"}},
		{&BindingBarrierUnavailableError{TenantID: "acme"}, []string{"acme"}},
		{&BindingBarrierSealedError{TenantID: "acme"}, []string{"acme"}},
		{&RemoteObjstoreBindingUnsupportedError{TenantID: "acme", Database: "orders", Provider: "s3"}, []string{"orders", "s3"}},
		{&ObjstoreBindingEncryptionUnsupportedError{TenantID: "acme", Database: "orders"}, []string{"orders"}},
		{&IsolationPolicyUnavailableError{}, []string{"isolation policy"}},
		{&ObjstoreBarrierSealedError{}, []string{"sealed"}},
	}
	for _, tc := range cases {
		msg := tc.err.Error()
		for _, want := range tc.want {
			if !strings.Contains(msg, want) {
				t.Fatalf("%T.Error() = %q, want %q in it", tc.err, msg, want)
			}
		}
	}
}

// sealGatedStorage passes every verb through while its gate is open and
// refuses every verb, its batches, snapshots and iterators included, with the
// gate's error once the gate closes.
func TestSealGatedStorage_GateOpenAndClosed(t *testing.T) {
	sealed := &BindingBarrierSealedError{TenantID: "acme"}
	var gateErr error
	s := &sealGatedStorage{Storage: memory.NewStorage(), check: func() error { return gateErr }}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := s.Get([]byte("a")); err != nil || string(v) != "1" {
		t.Fatalf("Get = %q, %v; want 1", v, err)
	}
	if ok, err := s.Exists([]byte("a")); err != nil || !ok {
		t.Fatalf("Exists = %v, %v; want true", ok, err)
	}
	if keys, err := s.List(nil); err != nil || len(keys) != 2 {
		t.Fatalf("List = %d keys, %v; want 2", len(keys), err)
	}
	if err := s.Delete([]byte("b")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.DeleteRange([]byte("x"), []byte("y")); err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}
	it := s.NewIterator(nil)
	if !it.SeekToFirst() {
		t.Fatal("open gate: iterator SeekToFirst = false, want a key")
	}
	_ = it.Close()
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if v, err := snap.Get([]byte("a")); err != nil || string(v) != "1" {
		t.Fatalf("snapshot Get = %q, %v; want 1", v, err)
	}
	sit := snap.NewIterator(nil)
	if !sit.SeekToFirst() {
		t.Fatal("open gate: snapshot iterator SeekToFirst = false, want a key")
	}
	_ = sit.Close()

	gateErr = sealed
	assertSealed := func(verb string, err error) {
		t.Helper()
		if !errors.Is(err, sealed) {
			t.Fatalf("closed gate: %s err = %v, want the gate's error", verb, err)
		}
	}
	_, err = s.Get([]byte("a"))
	assertSealed("Get", err)
	assertSealed("Set", s.Set([]byte("c"), []byte("3")))
	assertSealed("Delete", s.Delete([]byte("a")))
	assertSealed("DeleteRange", s.DeleteRange([]byte("a"), []byte("z")))
	_, err = s.List(nil)
	assertSealed("List", err)
	_, err = s.Exists([]byte("a"))
	assertSealed("Exists", err)
	_, err = s.Snapshot()
	assertSealed("Snapshot", err)
	_, err = snap.Get([]byte("a"))
	assertSealed("snapshot Get", err)
	assertSealedIterator(t, "snapshot iterator", snap.NewIterator(nil), sealed)
	assertSealedIterator(t, "iterator", s.NewIterator(nil), sealed)
	_ = snap.Close()

	b := s.Batch()
	_, err = b.Get([]byte("a"))
	assertSealed("batch Get", err)
	assertSealed("batch Commit", b.Commit())
	assertSealedIterator(t, "batch iterator", b.NewIterator(nil), sealed)
	_ = b.Close()

	// Reopening the gate lets a batch commit again.
	gateErr = nil
	b = s.Batch()
	if err := b.Set([]byte("d"), []byte("4")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("open gate: batch Commit: %v", err)
	}
	if v, err := s.Get([]byte("d")); err != nil || string(v) != "4" {
		t.Fatalf("Get after batch = %q, %v; want 4", v, err)
	}
}

// assertSealedIterator asserts it positions nowhere and reports sealed.
func assertSealedIterator(t *testing.T, name string, it storage.Iterator, sealed error) {
	t.Helper()
	defer func() { _ = it.Close() }()
	if it.SeekToFirst() || it.SeekToLast() || it.Seek([]byte("a")) || it.Next() || it.Prev() || it.Valid() {
		t.Fatalf("closed gate: %s positioned on a key", name)
	}
	if !errors.Is(it.Error(), sealed) {
		t.Fatalf("closed gate: %s Error() = %v, want the gate's error", name, it.Error())
	}
}

// isolationTenants is a tenant service that is no isolation-policy reader.
type isolationTenants struct{ tenant.TenantServicer }

// isolationReaderTenants is a tenant service that reads the policy.
type isolationReaderTenants struct {
	tenant.TenantServicer
	err error
}

func (r isolationReaderTenants) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyUnset, r.err
}

// The late isolation policy refuses before the tenant service is built,
// reads no requirement from a service that keeps no policy, and delegates to
// one that does.
func TestLateIsolationPolicy_Branches(t *testing.T) {
	ctx := context.Background()
	var unavailable *IsolationPolicyUnavailableError
	if _, err := (lateIsolationPolicy{}).ClusterIsolationPolicy(ctx); !errors.As(err, &unavailable) {
		t.Fatalf("no tenant func: err = %v, want *IsolationPolicyUnavailableError", err)
	}
	notBuilt := lateIsolationPolicy{tenants: func() tenant.TenantServicer { return nil }}
	if _, err := notBuilt.ClusterIsolationPolicy(ctx); !errors.As(err, &unavailable) {
		t.Fatalf("service not built: err = %v, want *IsolationPolicyUnavailableError", err)
	}
	plain := lateIsolationPolicy{tenants: func() tenant.TenantServicer { return isolationTenants{} }}
	if p, err := plain.ClusterIsolationPolicy(ctx); err != nil || p != tenant.ClusterIsolationPolicyUnset {
		t.Fatalf("non-reader service = %v, %v; want unset, nil", p, err)
	}
	readErr := errors.New("policy read failed")
	reader := lateIsolationPolicy{tenants: func() tenant.TenantServicer { return isolationReaderTenants{err: readErr} }}
	if _, err := reader.ClusterIsolationPolicy(ctx); !errors.Is(err, readErr) {
		t.Fatalf("reader service err = %v, want the reader's error", err)
	}
}

// A binding's encryptor follows its scope: the node's, the tenant's (refused
// while no tenant lookup is wired), and nothing for an unknown scope.
func TestLocalBindingFactory_EncryptorScopes(t *testing.T) {
	nodeEnc := atrest.New(nil)
	f := NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nodeEnc, qrdbconfig.StorageConfig{}, nil)
	if enc, err := f.encryptor(&database.Binding{Encryptor: database.EncryptorNode}); err != nil || enc != nodeEnc {
		t.Fatalf("node scope = %v, %v; want the node encryptor", enc, err)
	}
	var unavailable *BindingBarrierUnavailableError
	if _, err := f.encryptor(&database.Binding{TenantID: "acme", Encryptor: database.EncryptorTenant}); !errors.As(err, &unavailable) {
		t.Fatalf("tenant scope, no lookup: err = %v, want *BindingBarrierUnavailableError", err)
	}
	tenantEnc := atrest.New(nil)
	f = f.WithTenantEncryptors(func(id string) (*atrest.Encryptor, error) {
		if id != "acme" {
			return nil, &BindingBarrierUnavailableError{TenantID: id}
		}
		return tenantEnc, nil
	})
	if enc, err := f.encryptor(&database.Binding{TenantID: "acme", Encryptor: database.EncryptorTenant}); err != nil || enc != tenantEnc {
		t.Fatalf("tenant scope = %v, %v; want the tenant encryptor", enc, err)
	}
	if _, err := f.encryptor(&database.Binding{Encryptor: database.EncryptorScope("volume")}); err == nil ||
		!strings.Contains(err.Error(), "volume") {
		t.Fatalf("unknown scope err = %v, want a refusal naming the scope", err)
	}
}

// registryDataRoots resolves nothing until the barrier registry exists.
func TestRegistryDataRoots_LateRegistry(t *testing.T) {
	if r := registryDataRoots(nil)(); r != nil {
		t.Fatalf("nil getter = %v, want nil", r)
	}
	var reg *seal.BarrierRegistry
	get := func() *seal.BarrierRegistry { return reg }
	if r := registryDataRoots(get)(); r != nil {
		t.Fatalf("registry not built = %v, want nil", r)
	}
	reg = &seal.BarrierRegistry{}
	if r := registryDataRoots(get)(); r == nil {
		t.Fatal("built registry = nil, want the registry")
	}
}

// WithRaftMode marks the factory's node as a raft-mode node; a standalone
// factory leaves it unset.
func TestNodeBindingFactory_RaftMode(t *testing.T) {
	dir := t.TempDir()
	standalone := NewNodeBindingFactory(dir, database.Defaults{}, nil, qrdbconfig.StorageConfig{}, nil, nil)
	if standalone.node.RaftMode {
		t.Fatal("standalone factory: RaftMode = true, want false")
	}
	if raft := NewNodeBindingFactory(dir, database.Defaults{}, nil, qrdbconfig.StorageConfig{}, nil, nil).WithRaftMode(); !raft.node.RaftMode {
		t.Fatal("WithRaftMode: RaftMode = false, want true")
	}
}
