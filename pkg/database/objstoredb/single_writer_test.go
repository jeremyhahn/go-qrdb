package objstoredb_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// clusterManagers returns two clustered managers, node-a and node-b, that
// share one catalog, as two members of a cluster share the replicated one.
func clusterManagers(t *testing.T) (a, b *database.Manager) {
	t.Helper()
	dirA, dirB := t.TempDir(), t.TempDir()
	cat := databasetest.Catalog(dirA)
	for _, m := range []struct {
		mgr  **database.Manager
		dir  string
		node string
	}{{&a, dirA, "node-a"}, {&b, dirB, "node-b"}} {
		mgr := database.NewManager(nil, m.dir, database.WithBackendFactory(objstoredb.NewBackendFactory()))
		mgr.SetLocalNode(m.node)
		mgr.SetCatalog(cat)
		t.Cleanup(func() { _ = mgr.Close() })
		*m.mgr = mgr
	}
	return a, b
}

// TestCluster_ObjstoreDatabaseOpensOnlyOnItsOwner proves an objstore
// database created on one member opens there alone: the other member,
// sharing the catalog and the provider namespace, skips it and answers with
// the owner.
func TestCluster_ObjstoreDatabaseOpensOnlyOnItsOwner(t *testing.T) {
	ctx := context.Background()
	a, b := clusterManagers(t)
	root := filepath.Join(t.TempDir(), "objects")

	db, err := a.Create(localObjstore("blobs", root))
	if err != nil {
		t.Fatalf("Create on node-a: %v", err)
	}
	if got := db.Info().OwnerNode; got != "node-a" {
		t.Fatalf("OwnerNode = %q, want node-a", got)
	}
	if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_ = b.OpenAll(ctx)
	_, err = b.GetOwned("blobs", "")
	var notReplica *placement.NotAReplicaError
	if !errors.As(err, &notReplica) {
		t.Fatalf("node-b Get = %v, want *placement.NotAReplicaError naming node-a", err)
	}
	if len(notReplica.Replicas) != 1 || notReplica.Replicas[0].NodeID != "node-a" {
		t.Fatalf("node-b refusal names %v, want node-a", notReplica.Replicas)
	}
	got, err := db.Backend().Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("node-a Get = %q, %v; want v", got, err)
	}
}

// TestCluster_ProvisionedObjstoreDatabaseRefused proves a provisioned
// objstore database, which records no owning node, is refused on every
// clustered member rather than opened by each onto one namespace.
func TestCluster_ProvisionedObjstoreDatabaseRefused(t *testing.T) {
	ctx := context.Background()
	a, b := clusterManagers(t)
	root := filepath.Join(t.TempDir(), "objects")

	for _, m := range []*database.Manager{a, b} {
		_, err := m.Provision(ctx, localObjstore("blobs", root))
		var single *database.SingleWriterBackendError
		if !errors.As(err, &single) {
			t.Fatalf("Provision on %s = %v, want *database.SingleWriterBackendError", m.LocalNode(), err)
		}
		if _, err := m.Get("blobs"); err == nil {
			t.Fatalf("%s serves the refused database", m.LocalNode())
		}
	}

	// A standalone node is the only writer: provisioning opens there.
	solo := newManager(t, t.TempDir())
	if _, err := solo.Provision(ctx, localObjstore("blobs", filepath.Join(t.TempDir(), "solo"))); err != nil {
		t.Fatalf("standalone Provision: %v", err)
	}
}
