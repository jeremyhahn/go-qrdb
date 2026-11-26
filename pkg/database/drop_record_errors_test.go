package database

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

var errDropFault = errors.New("injected catalog store fault")

// dropFaultKV is a MemoryKVStore that fails, or loses, one operation on keys
// under one prefix, so each store branch of the drop record is reachable.
type dropFaultKV struct {
	*kvstore.MemoryKVStore
	op, prefix string
	lose       bool // report the conditional write as not applied, no error
}

func (f *dropFaultKV) hit(op, key string) bool {
	return f.op == op && strings.HasPrefix(key, f.prefix)
}

func (f *dropFaultKV) Get(ctx context.Context, key string) ([]byte, error) {
	if f.hit("Get", key) {
		return nil, errDropFault
	}
	return f.MemoryKVStore.Get(ctx, key)
}

func (f *dropFaultKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.hit("PutIfAbsent", key) {
		if f.lose {
			return false, nil
		}
		return false, errDropFault
	}
	return f.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func (f *dropFaultKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if f.hit("CompareAndSwap", key) {
		return false, errDropFault
	}
	return f.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

func (f *dropFaultKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if f.hit("CompareAndDelete", key) {
		return false, errDropFault
	}
	return f.MemoryKVStore.CompareAndDelete(ctx, key, expected)
}

const dropTenant, dropName = "acme", "orders"

func newDropFaultCatalog(t *testing.T) (*KVCatalog, *dropFaultKV) {
	t.Helper()
	f := &dropFaultKV{MemoryKVStore: kvstore.NewMemoryKVStore()}
	cat, err := NewKVCatalog(f, nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	meta := DatabaseMeta{TenantID: dropTenant, Name: dropName, Backend: BackendMemory, ID: "db-1"}
	if err := cat.Create(context.Background(), meta); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return cat, f
}

func TestDropRecord_StoreFaultsAreTyped(t *testing.T) {
	ctx := context.Background()
	recordKey := DropRecordKey(dropTenant, dropName)
	catalogKey := CatalogKey(dropTenant, dropName)

	t.Run("put reads the record", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		f.op, f.prefix = "Get", recordKey
		var rerr *CatalogReadError
		if err := cat.PutDropRecord(ctx, dropTenant, dropName, "db-1"); !errors.As(err, &rerr) {
			t.Fatalf("PutDropRecord = %v, want *CatalogReadError", err)
		}
	})
	t.Run("put reads the catalog", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		f.op, f.prefix = "Get", catalogKey
		var rerr *CatalogReadError
		if err := cat.PutDropRecord(ctx, dropTenant, dropName, "db-1"); !errors.As(err, &rerr) {
			t.Fatalf("PutDropRecord = %v, want *CatalogReadError", err)
		}
	})
	t.Run("put writes the record", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		f.op, f.prefix = "PutIfAbsent", recordKey
		var werr *CatalogWriteError
		if err := cat.PutDropRecord(ctx, dropTenant, dropName, "db-1"); !errors.As(err, &werr) {
			t.Fatalf("PutDropRecord = %v, want *CatalogWriteError", err)
		}
	})
	t.Run("put contended past every attempt", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		f.op, f.prefix, f.lose = "PutIfAbsent", recordKey, true
		err := cat.PutDropRecord(ctx, dropTenant, dropName, "db-1")
		if !errors.Is(err, errDropRecordContended) {
			t.Fatalf("PutDropRecord = %v, want the contended refusal", err)
		}
	})
	t.Run("put replaces a stale record", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		if err := f.MemoryKVStore.Put(ctx, recordKey, []byte("db-0")); err != nil {
			t.Fatal(err)
		}
		if err := cat.PutDropRecord(ctx, dropTenant, dropName, "db-1"); err != nil {
			t.Fatalf("PutDropRecord over a stale record: %v", err)
		}
		if got, err := cat.DropRecord(ctx, dropTenant, dropName); err != nil || got != "db-1" {
			t.Fatalf("DropRecord = (%q, %v), want db-1", got, err)
		}
		if err := f.MemoryKVStore.Put(ctx, recordKey, []byte("db-0")); err != nil {
			t.Fatal(err)
		}
		f.op, f.prefix = "CompareAndSwap", recordKey
		var werr *CatalogWriteError
		if err := cat.PutDropRecord(ctx, dropTenant, dropName, "db-1"); !errors.As(err, &werr) {
			t.Fatalf("PutDropRecord swap = %v, want *CatalogWriteError", err)
		}
	})
	t.Run("put over a corrupt catalog record", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		if err := f.MemoryKVStore.Put(ctx, catalogKey, []byte{0xff, 0x00, 0x01}); err != nil {
			t.Fatal(err)
		}
		if err := cat.PutDropRecord(ctx, dropTenant, dropName, "db-1"); err == nil {
			t.Fatal("PutDropRecord over a corrupt catalog record succeeded")
		}
	})
	t.Run("read", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		f.op, f.prefix = "Get", recordKey
		var rerr *CatalogReadError
		if _, err := cat.DropRecord(ctx, dropTenant, dropName); !errors.As(err, &rerr) {
			t.Fatalf("DropRecord = %v, want *CatalogReadError", err)
		}
	})
	t.Run("delete", func(t *testing.T) {
		cat, f := newDropFaultCatalog(t)
		f.op, f.prefix = "CompareAndDelete", recordKey
		var werr *CatalogWriteError
		if err := cat.DeleteDropRecord(ctx, dropTenant, dropName, "db-1"); !errors.As(err, &werr) {
			t.Fatalf("DeleteDropRecord = %v, want *CatalogWriteError", err)
		}
	})
}

func TestManager_DropRecordRoundTrip(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)

	bare := NewManager(logger, t.TempDir())
	if err := bare.RecordDrop(ctx, dropTenant, dropName, "db-1"); err != nil {
		t.Fatalf("RecordDrop without a catalog = %v", err)
	}
	if got, err := bare.DroppedID(ctx, dropTenant, dropName); err != nil || got != "" {
		t.Fatalf("DroppedID without a catalog = (%q, %v)", got, err)
	}
	if err := bare.ClearDropRecord(ctx, dropTenant, dropName, "db-1"); err != nil {
		t.Fatalf("ClearDropRecord without a catalog = %v", err)
	}

	cat, _ := newDropFaultCatalog(t)
	m := NewManager(logger, t.TempDir())
	m.SetCatalog(cat)
	if err := m.RecordDrop(ctx, dropTenant, dropName, "db-1"); err != nil {
		t.Fatalf("RecordDrop: %v", err)
	}
	if got, err := m.DroppedID(ctx, dropTenant, dropName); err != nil || got != "db-1" {
		t.Fatalf("DroppedID = (%q, %v), want db-1", got, err)
	}
	if err := m.ClearDropRecord(ctx, dropTenant, dropName, "db-1"); err != nil {
		t.Fatalf("ClearDropRecord: %v", err)
	}
	if got, err := m.DroppedID(ctx, dropTenant, dropName); err != nil || got != "" {
		t.Fatalf("DroppedID after clear = (%q, %v), want none", got, err)
	}
}

func TestDatabaseErrors_MessagesNameTheirSubject(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&DropRecordSupersededError{TenantID: "acme", Name: "orders", DatabaseID: "db-1"}, "superseded"},
		{&InvalidStorageCodecError{Codec: "bogus"}, "bogus"},
		{&StrictTenantDedicatedWALRequiredError{Name: "orders", Tenant: "acme"}, "dedicated WAL"},
		{&StrictTenantDedicatedWALRequiredError{Name: "orders", Tenant: "acme", Backend: "memory"}, "orders"},
		{&DedicatedWALOffDataPathError{Name: "orders", Tenant: "acme", ServedBy: "the shared store"}, "the shared store"},
		{&IncarnationError{Err: errDropFault}, "incarnation"},
	}
	for _, c := range cases {
		if msg := c.err.Error(); !strings.Contains(msg, c.want) {
			t.Errorf("%T message %q lacks %q", c.err, msg, c.want)
		}
	}
	id, err := NewIncarnation()
	if err != nil || len(id) != 32 {
		t.Fatalf("NewIncarnation = (%q, %v)", id, err)
	}
}

func TestDatabaseConfig_ValidateRefusals(t *testing.T) {
	cases := []struct {
		name string
		cfg  DatabaseConfig
	}{
		{"bad mode", DatabaseConfig{Name: "orders", Backend: BackendMemory, Mode: "bogus"}},
		{"remote without connection", DatabaseConfig{Name: "orders", Mode: ModeRemote}},
		{"remote with empty connection", DatabaseConfig{Name: "orders", Mode: ModeRemote, Connection: &DatabaseConnectionConfig{}}},
		{"bad backend", DatabaseConfig{Name: "orders", Backend: "bogus"}},
		{"bad sql", DatabaseConfig{Name: "orders", Backend: BackendMemory, SQL: &SQLConfig{Concurrency: "bogus"}}},
		{"bad codec", DatabaseConfig{Name: "orders", Backend: BackendMemory, StorageCodec: "bogus"}},
		{"bad shamir", DatabaseConfig{Name: "orders", Backend: BackendMemory, Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 1, SharesTotal: 1}}},
		{"etcd without listen addr", DatabaseConfig{Name: "orders", Backend: BackendMemory, Protocol: ProtocolConfig{Etcd: &EtcdConfig{Enabled: true}}}},
	}
	for _, c := range cases {
		cfg := c.cfg
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", c.name, c.cfg)
		}
	}
	ok := DatabaseConfig{Name: "orders", Backend: BackendMemory, SQL: &SQLConfig{}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate of a valid config = %v", err)
	}
	if ok.Mode != ModeEmbedded {
		t.Fatalf("empty mode defaulted to %q, want embedded", ok.Mode)
	}
}

func TestManager_ResolveDefaultsCopiesRaft(t *testing.T) {
	raft := &DatabaseRaftConfig{}
	var nilManager *Manager
	got := nilManager.ResolveDefaults(DatabaseConfig{Name: "orders", Raft: raft})
	if got.Raft == raft || got.Raft == nil {
		t.Fatal("ResolveDefaults on a nil manager did not copy the raft config")
	}
	m := NewManager(slog.New(slog.DiscardHandler), t.TempDir())
	got = m.ResolveDefaults(DatabaseConfig{Name: "orders", Raft: raft})
	if got.Raft == raft {
		t.Fatal("ResolveDefaults shared the caller's raft config")
	}
}

func TestRaftOptionSet_NamesTheFirstRaftOnlyOption(t *testing.T) {
	cases := []struct {
		cfg  DatabaseConfig
		want string
	}{
		{DatabaseConfig{}, ""},
		{DatabaseConfig{Replicas: 3}, "replicas"},
		{DatabaseConfig{DedicatedWAL: true}, "dedicated_wal"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{}}, ""},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{ClusterMode: "join"}}, "raft.clusterMode"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{ShardID: 7}}, "raft.shardId"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{ReplicaID: 2}}, "raft.replicaId"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{RaftAddress: "127.0.0.1:1"}}, "raft.raftAddress"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{LocalNodeCount: 3}}, "raft.localNodeCount"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{StateStorage: "file"}}, "raft.stateStorage"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{Discovery: "dns"}}, "raft.discovery"},
	}
	for _, c := range cases {
		cfg := c.cfg
		if got := raftOptionSet(&cfg); got != c.want {
			t.Errorf("raftOptionSet(%+v) = %q, want %q", c.cfg, got, c.want)
		}
	}
	m := NewManager(slog.New(slog.DiscardHandler), t.TempDir())
	m.SetLogReclaimer(nil)
	if m.logReclaimer.Load() != nil {
		t.Fatal("SetLogReclaimer(nil) left a reclaimer attached")
	}
}
