package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newHomeGroupRegistry builds a raft-isolated tenant "hg" whose home group and
// one ordinary dedicated-raft database ("orders") both have a placement the
// raft backend factory serves, and returns the public registry over it with
// the factory the tenant's home store reads through.
func newHomeGroupRegistry(t *testing.T) (*restServiceRegistry, *RaftBackendFactory) {
	t.Helper()
	host, mux := newSharedTestHostAt(t, t.TempDir(), newTestSystemBarrier(t))
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	root, _ := registerVolumeTenant(t, reg, "hg")
	src.add("hg", database.TenantHomeName, 2410, root)
	src.setDedicated("hg", database.TenantHomeName)
	src.add("hg", "orders", 2411, root)

	r := newRESTRegistry(newFilledRegistry(), reg, "system",
		WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationRaft}),
		withRaftBackendFactory(f))
	return r, f
}

// The home group's placement makes its name resolvable, but no public
// resolver selects it: every service a caller can name a database for refuses
// it with the error an unknown database gets, in any letter case.
func TestRESTServiceRegistry_RefusesTenantHome(t *testing.T) {
	r, _ := newHomeGroupRegistry(t)
	resolvers := map[string]func(db string) error{
		"kv":          func(db string) error { _, err := r.KVService("hg", db); return err },
		"mvcc":        func(db string) error { _, err := r.MVCCService("hg", db); return err },
		"watch":       func(db string) error { _, err := r.WatchService("hg", db); return err },
		"lease":       func(db string) error { _, err := r.LeaseService("hg", db); return err },
		"transaction": func(db string) error { _, err := r.TransactionService("hg", db); return err },
		"batch":       func(db string) error { _, err := r.BatchService("hg", db); return err },
		"lock":        func(db string) error { _, err := r.LockService("hg", db); return err },
	}
	for name, resolve := range resolvers {
		for _, db := range []string{database.TenantHomeName, "__HOME", "does-not-exist"} {
			t.Run(name+"/"+db, func(t *testing.T) {
				err := resolve(db)
				var dnf *rest.DatabaseNotFoundError
				if !errors.As(err, &dnf) {
					t.Fatalf("resolve(%q) = %v (%T), want *rest.DatabaseNotFoundError", db, err, err)
				}
				if dnf.TenantID != "hg" || dnf.Database != db {
					t.Fatalf("DatabaseNotFoundError = %+v, want hg/%s", dnf, db)
				}
			})
		}
	}
}

// Refusing the home group leaves an ordinary dedicated-raft database
// resolvable, and the home store's own source still serves the group.
func TestRESTServiceRegistry_TenantHomeRefusalKeepsOtherPaths(t *testing.T) {
	r, f := newHomeGroupRegistry(t)
	ctx := context.Background()

	kv, err := r.KVService("hg", "orders")
	if err != nil {
		t.Fatalf("KVService(orders): %v", err)
	}
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put(orders): %v", err)
	}
	if got, err := kv.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("Get(orders) = %q, %v; want v", got, err)
	}

	home, err := f.Services(ctx, "hg", database.TenantHomeName)
	if err != nil {
		t.Fatalf("home store source: %v", err)
	}
	if err := home.KV.Put(ctx, "iam/record", []byte("r")); err != nil {
		t.Fatalf("home Put: %v", err)
	}
	if got, err := home.KV.Get(ctx, "iam/record"); err != nil || string(got) != "r" {
		t.Fatalf("home Get = %q, %v; want r", got, err)
	}
}
