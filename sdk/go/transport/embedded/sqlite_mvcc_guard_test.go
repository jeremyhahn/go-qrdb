package embedded

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	mvccGuardDB       = "app.db"
	mvccGuardPageSize = 512
)

// sqliteMVCCGuardTransport builds the embedded transport the way a composer
// does: raw MVCC, transaction and lock services handed to New, which must
// guard the MVCC surface itself. It returns the raw services too so the
// test drives a SQLite store beside the transport.
func sqliteMVCCGuardTransport(t *testing.T) (*Transport, sqlitevfs.ServiceKV, sqlitevfs.ServiceTxn) {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	mvccSvc := service.NewMVCCKVService(host, 1)
	txSvc := service.NewTransactionService(host, 1)
	leaseSvc := service.NewLeaseService(host, 1)
	watchSvc := service.NewWatchService(watch.NewHub())
	lockSvc, err := service.NewLockService(txSvc, leaseSvc, watchSvc, mvccSvc, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewLockService: %v", err)
	}
	tr, err := New(service.NewKVService(host, 1), &mockAdminService{},
		WithBatchService(service.NewBatchService(host, 1)),
		WithTransactionService(txSvc),
		WithMVCCService(mvccSvc),
		WithLeaseService(leaseSvc),
		WithWatchService(watchSvc),
		WithLockService(lockSvc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	kv, ok := mvccSvc.(sqlitevfs.ServiceKV)
	if !ok {
		t.Fatalf("MVCC servicer %T is not a sqlitevfs.ServiceKV", mvccSvc)
	}
	txn, ok := txSvc.(sqlitevfs.ServiceTxn)
	if !ok {
		t.Fatalf("transaction servicer %T is not a sqlitevfs.ServiceTxn", txSvc)
	}
	return tr, kv, txn
}

func mvccGuardStore(t *testing.T, kv sqlitevfs.ServiceKV, txn sqlitevfs.ServiceTxn) (*sqlitevfs.Store, *sqlitevfs.ServiceBackend) {
	t.Helper()
	b, err := sqlitevfs.NewServiceBackend(kv, txn)
	if err != nil {
		t.Fatalf("NewServiceBackend: %v", err)
	}
	s, err := sqlitevfs.NewStore(b, mvccGuardDB)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, b
}

func mvccGuardWrite(t *testing.T, txn *sqlitevfs.Txn, fills ...byte) {
	t.Helper()
	for i, fill := range fills {
		page := bytes.Repeat([]byte{fill}, mvccGuardPageSize)
		if n, err := txn.WriteAt(page, int64(i)*mvccGuardPageSize); err != nil || n != mvccGuardPageSize {
			t.Fatalf("WriteAt page %d = %d, %v", i+1, n, err)
		}
	}
}

func mvccGuardCommit(t *testing.T, s *sqlitevfs.Store, fills ...byte) int64 {
	t.Helper()
	txn, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	mvccGuardWrite(t, txn, fills...)
	rev, err := txn.Commit(t.Context())
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return rev
}

func mvccGuardGeneration(t *testing.T, kv sqlitevfs.ServiceKV, b *sqlitevfs.ServiceBackend) uint64 {
	t.Helper()
	key, err := sqlitevfs.MetaKey(mvccGuardDB)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := kv.CurrentRevision(t.Context())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	value, found, err := b.GetAt(t.Context(), key, rev)
	if err != nil || !found {
		t.Fatalf("meta: found=%v err=%v", found, err)
	}
	var m sqlitevfs.Meta
	if err := m.UnmarshalBinary(value); err != nil {
		t.Fatal(err)
	}
	return m.Generation
}

// TestEmbeddedMVCCPutWithLeaseOfASQLitePageIsRefused: the default MVCC
// service New installs refuses a leased write onto a SQLite page key, so
// no MVCC write path bypasses the store's page and meta invariants.
func TestEmbeddedMVCCPutWithLeaseOfASQLitePageIsRefused(t *testing.T) {
	tr, kv, _ := sqliteMVCCGuardTransport(t)
	key := sqlitePageKey(t)
	grant, err := tr.LeaseGrant(t.Context(), 60)
	if err != nil {
		t.Fatalf("LeaseGrant: %v", err)
	}
	_, err = tr.PutWithLease(t.Context(), key, []byte("x"), grant.ID)
	assertSQLiteKeyRefused(t, "PutWithLease", err, key)
	rev, err := kv.CurrentRevision(t.Context())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if got, err := kv.GetAtRevision(t.Context(), key, rev); err == nil && got != nil {
		t.Fatalf("refused PutWithLease left %q at %q", got.Value, key)
	}
}

// TestEmbeddedMVCCRestoreOfASQLiteDatabaseIsAnAtomicImage: a restore
// through the embedded transport, with a connection open on the database,
// puts the target image back whole, bumps the generation, and fails the
// open connection's commit with ErrSnapshotChanged.
func TestEmbeddedMVCCRestoreOfASQLiteDatabaseIsAnAtomicImage(t *testing.T) {
	tr, kv, txn := sqliteMVCCGuardTransport(t)
	a, b := mvccGuardStore(t, kv, txn)
	r := mvccGuardCommit(t, a, 0x11, 0x22)
	open, err := a.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	mvccGuardWrite(t, open, 0x99)
	other, _ := mvccGuardStore(t, kv, txn)
	mvccGuardCommit(t, other, 0x33, 0x44, 0x55)
	before := mvccGuardGeneration(t, kv, b)

	result, err := tr.RestoreToRevision(t.Context(), &transport.RestoreRevisionRequest{Revision: r, Prefix: []byte("sqlite/")})
	if err != nil {
		t.Fatalf("RestoreToRevision(%d): %v", r, err)
	}
	if result.Applied == 0 {
		t.Fatalf("RestoreToRevision(%d) applied nothing", r)
	}
	if got := mvccGuardGeneration(t, kv, b); got == before {
		t.Fatalf("generation = %d after the restore, want bumped from %d", got, before)
	}
	if _, err := open.Commit(t.Context()); !errors.Is(err, sqlitevfs.ErrSnapshotChanged) {
		t.Fatalf("Commit across a restore err = %v, want ErrSnapshotChanged", err)
	}

	fresh, _ := mvccGuardStore(t, kv, txn)
	read, err := fresh.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = read.Rollback(t.Context()) }()
	if got := read.Size(); got != 2*mvccGuardPageSize {
		t.Fatalf("size after restore = %d, want R's %d", got, 2*mvccGuardPageSize)
	}
	for i, fill := range []byte{0x11, 0x22} {
		buf := make([]byte, mvccGuardPageSize)
		if _, err := read.ReadAt(buf, int64(i)*mvccGuardPageSize); err != nil {
			t.Fatalf("ReadAt page %d: %v", i+1, err)
		}
		if !bytes.Equal(buf, bytes.Repeat([]byte{fill}, mvccGuardPageSize)) {
			t.Fatalf("page %d after restore is not R's image", i+1)
		}
	}
}
